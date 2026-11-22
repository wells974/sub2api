package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Run the merged service boundary with both extensions enabled, actual server
// search, deferred-tool disclosure, then a client result on the next request.
func TestExcelBPSSearchOnDemandCatalogRoundTrip(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	for _, stream := range []bool{false, true} {
		for _, forced := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/forced=%t", stream, forced), func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				ctx := logger.IntoContext(context.Background(), zap.New(core))
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					excelSearchCallResponse(),
					{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"output":"found","results":[{"url":"https://example.test","title":"Fixture"}]}`))},
					bpsCompletionResponse(200, excelBPSRepairWire(t, "disclosure", "Run", `{"name":"optional_00","arguments":{"count":1}}`)),
					bpsCompletionResponse(200, excelBPSRepairWire(t, "action", "Run", `{"name":"optional_00","arguments":{"count":1}}`)),
				}}
				checked := &excelBPSRepairUpstream{httpUpstreamRecorder: upstream}
				for _, response := range upstream.responses {
					tracked := &excelBPSRepairBody{Reader: response.Body}
					response.Body = tracked
					checked.bodies = append(checked.bodies, tracked)
				}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = checked
				account := excelAccount()
				account.Concurrency = 1
				account.Extra[ExcelBPSWebSearchKey] = true
				account.Extra[ExcelBPSWebSearchAutoKey] = true
				source := onDemandGatewaySource(stream)
				source["service_tier"] = "priority"
				if forced {
					source["tools"] = append(source["tools"].([]any), map[string]any{"type": "web_search"})
					source["tool_choice"] = map[string]any{"type": "web_search"}
				}
				for turn := 0; turn < 2; turn++ {
					body, err := json.Marshal(source)
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
					result, err := svc.Forward(ctx, c, account, body)
					require.NoError(t, err, rec.Body.String())
					require.NotNil(t, result)
					terminal := catalogGatewayTerminal(t, rec.Body.String(), stream)
					items := terminal.Get("output").Array()
					if turn == 0 {
						require.Len(t, upstream.requests, 3, "search must not trigger a correction")
						require.Equal(t, 20, result.Usage.InputTokens)
						require.Equal(t, 4, result.Usage.OutputTokens)
						require.Len(t, items, 2)
						require.Equal(t, "web_search_call", items[0].Get("type").String())
						require.Equal(t, "js", items[1].Get("name").String())
						require.Equal(t, "mcp__node_repl", items[1].Get("namespace").String())
						code := gjson.Parse(items[1].Get("arguments").String()).Get("code").String()
						require.True(t, strings.HasPrefix(code, "nodeRepl.write("))
						var disclosure string
						require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(code, "nodeRepl.write("), ");")), &disclosure))
						history := []any{map[string]any{"role": "user", "content": "search and use optional tool"}}
						for _, item := range items {
							var value map[string]any
							require.NoError(t, json.Unmarshal([]byte(item.Raw), &value))
							history = append(history, value)
						}
						source["input"] = append(history, map[string]any{"type": "function_call_output", "call_id": items[1].Get("call_id").String(), "output": disclosure})
						// Existing search history must remain replayable after opt-out.
						account.Extra[ExcelBPSWebSearchKey] = false
						account.Extra[ExcelBPSWebSearchAutoKey] = false
						delete(source, "tool_choice")
						if forced {
							tools := source["tools"].([]any)
							source["tools"] = tools[:len(tools)-1]
						}
					} else {
						require.Len(t, upstream.requests, 4)
						require.Equal(t, 10, result.Usage.InputTokens)
						require.Len(t, items, 1)
						require.Equal(t, "optional_00", items[0].Get("name").String())
						require.JSONEq(t, `{"count":1}`, items[0].Get("arguments").String())
					}
				}
				require.Equal(t, "/backend-api/codex/alpha/search", upstream.requests[1].URL.Path)
				for i, request := range upstream.requests {
					require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
					require.False(t, gjson.GetBytes(upstream.bodies[i], "service_tier").Exists())
					require.True(t, checked.bodies[i].closed.Load())
				}
				events := logs.FilterMessage("bps_tool_catalog").All()
				require.Len(t, events, 2)
				for _, event := range events {
					encoded, err := json.Marshal(event.ContextMap()["catalog"])
					require.NoError(t, err)
					require.True(t, gjson.GetBytes(encoded, "active").Bool())
				}
				require.NotContains(t, gjson.GetBytes(upstream.bodies[3], "input.0").String(), "__sub2api_web_search", "private search tool must not enter the inherited catalog")
			})
		}
	}
}

func TestExcelBPSNativeClientToolSearchThenSchemaRepair(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				bpsCompletionResponse(200, excelBPSRepairWire(t, "lookup", "Lookup", `{"name":"tool_search","arguments":{"query":"optional_00"}}`)),
				bpsCompletionResponse(200, excelBPSRepairWire(t, "bad", "Run", `{"name":"optional_00","arguments":{"count":"one"}}`)),
				bpsCompletionResponse(200, excelBPSRepairWire(t, "fixed", "Run", `{"name":"optional_00","arguments":{"count":1}}`)),
			}}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			source := onDemandGatewaySource(stream)
			tools := source["tools"].([]any)
			source["tools"] = append(tools, map[string]any{"type": "tool_search", "execution": "client",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}})
			source["tool_choice"] = map[string]any{"type": "tool_search"}
			forward := func() gjson.Result {
				body, err := json.Marshal(source)
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				_, err = svc.Forward(t.Context(), c, account, body)
				require.NoError(t, err, rec.Body.String())
				terminal := catalogGatewayTerminal(t, rec.Body.String(), stream)
				require.Len(t, terminal.Get("output").Array(), 1)
				return terminal.Get("output.0")
			}
			call := forward()
			require.Equal(t, "tool_search_call", call.Get("type").String())
			require.Equal(t, "optional_00", call.Get("arguments.query").String())
			require.Len(t, upstream.requests, 1)
			delete(source, "tool_choice")
			source["input"] = []any{json.RawMessage(call.Raw), map[string]any{"type": "tool_search_output",
				"execution": "client", "call_id": call.Get("call_id").String(), "tools": []any{tools[1]}}}
			action := forward()
			require.Equal(t, "function_call", action.Get("type").String())
			require.Equal(t, "optional_00", action.Get("name").String())
			require.JSONEq(t, `{"count":1}`, action.Get("arguments").String())
			require.Len(t, upstream.requests, 3)
		})
	}
}

// Search intentionally does not reuse the ordinary correction callback: it
// references a different native conversation after each completed search round.
func TestExcelBPSSearchSchemaFailureDoesNotRepairOrReplay(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	for _, mode := range []string{"neutral", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprint(stream), func(t *testing.T) {
					core, logs := observer.New(zap.InfoLevel)
					ctx := logger.IntoContext(context.Background(), zap.New(core))
					upstream := &httpUpstreamRecorder{responses: []*http.Response{
						excelSearchCallResponse(),
						{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"output":"found","results":[]}`))},
						bpsCompletionResponse(200, excelBPSRepairWire(t, "invalid", "Run", `{"name":"lookup","arguments":{"count":"PRIVATE_VALUE"}}`)),
					}}
					svc := openAIClientToolsTestService(upstream)
					account := excelAccount()
					account.Extra[ExcelBPSWebSearchKey] = true
					account.Extra[ExcelBPSWebSearchAutoKey] = true
					body := schemaGatewayRequest(stream)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
					result, err := svc.Forward(ctx, c, account, body)
					require.Error(t, err)
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 3)
					require.Equal(t, 20, result.Usage.InputTokens)
					require.Equal(t, 4, result.Usage.OutputTokens)
					require.NotContains(t, rec.Body.String(), "response.function_call_arguments")
					require.NotContains(t, rec.Body.String(), "PRIVATE_VALUE")
					if mode == "neutral" {
						require.NotContains(t, strings.ToLower(rec.Body.String()), "basispoints")
					} else {
						require.Contains(t, rec.Body.String(), `"failure_stage":"bridge_tool_validation"`)
					}
					require.True(t, account.IsExcelBPSEnabled())
					observed, ok := GetOpsStreamError(c)
					require.True(t, ok)
					require.NotNil(t, observed.BridgeFailure)
					require.Equal(t, "bridge_tool_validation", observed.BridgeFailure.FailureStage)
					require.Equal(t, "search_no_repair", observed.BridgeFailure.RepairSkippedReason)
					require.Equal(t, "response.completed", observed.BridgeFailure.UpstreamTerminalEvent)
					require.NotNil(t, observed.BridgeFailure.ToolArguments)
					require.Equal(t, "/count", observed.BridgeFailure.ToolArguments.Path)
					require.Equal(t, "type", observed.BridgeFailure.ToolArguments.Rule)
					events := logs.FilterMessage("bps_request_diagnostics").All()
					require.Len(t, events, 1)
					diagnostic, err := json.Marshal(events[0].ContextMap())
					require.NoError(t, err)
					require.Equal(t, "tool_arguments", gjson.GetBytes(diagnostic, "schema_phase").String())
					require.Equal(t, "/count", gjson.GetBytes(diagnostic, "schema_failure.path").String())
					require.NotContains(t, string(diagnostic), "PRIVATE_VALUE")
				})
			}
		})
	}
}
