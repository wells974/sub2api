package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func onDemandGatewaySource(stream bool) map[string]any {
	tools := []any{map[string]any{"type": "namespace", "name": "mcp__node_repl", "tools": []any{map[string]any{
		"type": "function", "name": "js", "description": "Node JavaScript. nodeRepl.write(value) prints text.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false},
	}}}}
	for i := 0; i < 24; i++ {
		tools = append(tools, map[string]any{"type": "function", "name": fmt.Sprintf("optional_%02d", i), "description": strings.Repeat("PRIVATE_POLICY Read the complete tool policy. ", 10), "parameters": map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "required": []string{"count"}}})
	}
	return map[string]any{"model": "gpt-5.6-sol", "reasoning": map[string]any{"effort": "high"}, "stream": stream, "input": "use the optional tool", "tools": tools}
}

func catalogGatewayTerminal(t *testing.T, body string, stream bool) gjson.Result {
	t.Helper()
	if !stream {
		return gjson.Parse(body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			event := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if event.Get("type").String() == "response.completed" {
				return event.Get("response")
			}
		}
	}
	t.Fatalf("no completed event: %s", body)
	return gjson.Result{}
}

func TestExcelBPSOnDemandCatalogRoundTrip(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	for _, stream := range []bool{false, true} {
		for _, lookup := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/lookup=%t", stream, lookup), func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				ctx := logger.IntoContext(context.Background(), zap.New(core))
				firstCode := `{"name":"optional_00","arguments":{"count":1}}`
				if lookup {
					firstCode = `{"name":"__sub2api_tool_catalog","arguments":{"names":["optional_00"]}}`
				}
				first := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, "docs", "Lookup", firstCode))}
				second := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, "action", "Run", `{"name":"optional_00","arguments":{"count":1}}`))}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: first}, {StatusCode: 200, Header: http.Header{}, Body: second}}}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = &excelBPSRepairUpstream{httpUpstreamRecorder: upstream, bodies: []*excelBPSRepairBody{first, second}}
				account := excelAccount()
				account.Concurrency = 1
				source := onDemandGatewaySource(stream)
				for turn := 0; turn < 2; turn++ {
					body, err := json.Marshal(source)
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
					result, err := svc.Forward(ctx, c, account, body)
					require.NoError(t, err, rec.Body.String())
					require.NotNil(t, result)
					require.Equal(t, 10, result.Usage.InputTokens)
					require.Equal(t, 2, result.Usage.OutputTokens)
					require.Len(t, upstream.requests, turn+1, "disclosure must not add a server-side request")
					terminal := catalogGatewayTerminal(t, rec.Body.String(), stream)
					items := terminal.Get("output").Array()
					require.Len(t, items, 1)
					if turn == 0 {
						require.Equal(t, "js", items[0].Get("name").String())
						require.Equal(t, "mcp__node_repl", items[0].Get("namespace").String())
						code := gjson.Parse(items[0].Get("arguments").String()).Get("code").String()
						require.True(t, strings.HasPrefix(code, "nodeRepl.write("))
						var payload string
						require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(code, "nodeRepl.write("), ");")), &payload))
						var client map[string]any
						require.NoError(t, json.Unmarshal([]byte(items[0].Raw), &client))
						source["input"] = []any{map[string]any{"role": "user", "content": "use the optional tool"}, client, map[string]any{"type": "function_call_output", "call_id": client["call_id"], "output": payload}}
					} else {
						require.Equal(t, "optional_00", items[0].Get("name").String())
						require.JSONEq(t, `{"count":1}`, items[0].Get("arguments").String())
					}
				}
				require.True(t, first.closed.Load())
				require.True(t, second.closed.Load())
				for _, req := range upstream.requests {
					require.Equal(t, "/basispoints/api/responses", req.URL.Path)
					require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
					require.Equal(t, "excel", req.Header.Get("X-Openai-Internal-Basispoints-Client-Agent-Profile"))
				}
				require.Equal(t, gjson.GetBytes(upstream.bodies[0], "input.0").Raw, gjson.GetBytes(upstream.bodies[1], "input.0").Raw, "stable prologue")
				require.Equal(t, "high", gjson.GetBytes(upstream.bodies[1], "reasoning_effort").String())
				events := logs.FilterMessage("bps_tool_catalog").All()
				require.Len(t, events, 2)
				encoded, err := json.Marshal(events[1].ContextMap())
				require.NoError(t, err)
				require.True(t, gjson.GetBytes(encoded, "catalog.active").Bool())
				require.Equal(t, int64(1), gjson.GetBytes(encoded, "catalog.disclosed_count").Int())
				require.NotContains(t, string(encoded), "PRIVATE_POLICY")
				require.Empty(t, logs.FilterMessage("bps_schema_repair").All())
			})
		}
	}
}

func TestExcelBPSOnDemandCatalogDefaultAndInvalidConfig(t *testing.T) {
	t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", "legacy") // Preserve the original wire-contract regression.
	for _, setting := range []string{"", "false", "invalid"} {
		t.Run(setting, func(t *testing.T) {
			t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", setting)
			upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(200, excelBPSRepairWire(t, "action", "Run", `{"name":"optional_00","arguments":{"count":1}}`))}
			svc := openAIClientToolsTestService(upstream)
			body, err := json.Marshal(onDemandGatewaySource(false))
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			_, err = svc.Forward(context.Background(), c, excelAccount(), body)
			if setting == "invalid" {
				require.Error(t, err)
				require.Empty(t, upstream.requests)
				require.Contains(t, rec.Body.String(), "basispoints_tool_catalog_settings_invalid")
			} else {
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "optional_00", gjson.Get(rec.Body.String(), "output.0.name").String())
				require.Contains(t, string(upstream.bodies[0]), "PRIVATE_POLICY")
				require.NotContains(t, string(upstream.bodies[0]), "__sub2api_tool_catalog")
			}
		})
	}
}

func TestExcelBPSOnDemandCatalogControlledExperiment(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	ctx := context.WithValue(context.Background(), controlledExperimentContextKey{}, &controlledExperimentMode{channel: "bps"})
	enabled, err := excelBPSOnDemandCatalogEnabled(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
}
