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
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func schemaGatewayRequest(stream bool) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"reasoning":{"effort":"high"},"input":"check count","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}}]}`, stream))
}

func TestExcelBPSSchemaRepairDefaultsAndConfig(t *testing.T) {
	for _, enabled := range []string{"", "false", "true", "invalid"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", enabled)
			t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "30")
			upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(200, excelBPSRepairWire(t, "first", "Run", `{"name":"lookup","arguments":{}}`))}
			svc := openAIClientToolsTestService(upstream)
			body := schemaGatewayRequest(false)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			if enabled == "true" {
				t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "0")
			}
			_, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.Error(t, err)
			if enabled == "invalid" || enabled == "true" {
				require.Empty(t, upstream.requests)
			} else {
				require.Len(t, upstream.requests, 1)
			}
		})
	}
}

func TestExcelBPSSchemaRepairRouteUsageAndBoundaries(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "30")
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"success", "invalid", "visible", "http_error", "partial_error", "zero_terminal"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "req-schema")))
				wire := excelBPSRepairWire(t, "initial", "Run", `{"name":"lookup","arguments":{"count":"PRIVATE_ARGUMENT"}}`)
				if scenario == "visible" {
					wire = "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"already delivered\"}\n\n" + wire
				}
				first := &excelBPSRepairBody{Reader: strings.NewReader(wire)}
				code := `{"name":"lookup","arguments":{"count":1}}`
				if scenario == "invalid" {
					code = `{"name":"lookup","arguments":{}}`
				}
				correction := excelBPSRepairWire(t, "corrected", "Run", code)
				status := 200
				if scenario == "http_error" {
					status = 500
					correction = `{"error":{"message":"PRIVATE_PROVIDER_ERROR"}}`
				}
				if scenario == "partial_error" || scenario == "zero_terminal" {
					correction = "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n"
					if scenario == "zero_terminal" {
						correction += "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"
					}
				}
				second := &excelBPSRepairBody{Reader: strings.NewReader(correction)}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: first}, {StatusCode: status, Header: http.Header{}, Body: second}}}
				checked := &excelBPSRepairUpstream{httpUpstreamRecorder: upstream, bodies: []*excelBPSRepairBody{first, second}}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = checked
				body := schemaGatewayRequest(stream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				account := excelAccount()
				account.Concurrency = 1
				result, err := svc.Forward(ctx, c, account, body)
				success := scenario == "success" || scenario == "visible" && !stream
				if success {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				require.NotNil(t, result)
				require.True(t, first.closed.Load())
				attempts, wantInput, wantOutput := 2, 20, 4
				if scenario == "visible" && stream {
					attempts, wantInput, wantOutput = 1, 10, 2
				}
				if scenario == "http_error" {
					wantInput, wantOutput = 10, 2
				}
				if scenario == "partial_error" || scenario == "zero_terminal" {
					wantInput, wantOutput = 14, 3
				}
				require.Len(t, upstream.requests, attempts)
				require.Equal(t, wantInput, result.Usage.InputTokens)
				require.Equal(t, wantOutput, result.Usage.OutputTokens)
				require.NotContains(t, rec.Body.String(), "PRIVATE_")
				if attempts == 2 {
					require.True(t, second.closed.Load())
					require.Equal(t, upstream.requests[0].URL, upstream.requests[1].URL)
					require.Equal(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
					require.Equal(t, upstream.requests[0].Header.Get("Chatgpt-Account-Id"), upstream.requests[1].Header.Get("Chatgpt-Account-Id"))
					require.Contains(t, string(upstream.bodies[1]), "invalid_client_tool_arguments")
					require.Contains(t, string(upstream.bodies[1]), "Correct its invalid JSON Schema arguments")
					require.Equal(t, gjson.GetBytes(upstream.bodies[0], "reasoning_effort").String(), gjson.GetBytes(upstream.bodies[1], "reasoning_effort").String())
				}
				if success {
					require.Equal(t, "resp_initial", result.ResponseID)
					if stream {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.function_call_arguments.done"))
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
					}
				} else {
					require.NotContains(t, rec.Body.String(), "event: response.function_call_arguments")
				}
				events := logs.FilterMessage("bps_schema_repair").All()
				require.Len(t, events, 1)
				encoded, marshalErr := json.Marshal(events[0].ContextMap())
				require.NoError(t, marshalErr)
				require.NotContains(t, string(encoded), "PRIVATE_")
				require.Equal(t, "req-schema", gjson.GetBytes(encoded, "request_id").String())
				require.Equal(t, attempts == 2, gjson.GetBytes(encoded, "schema_repair.attempted").Bool())
			})
		}
	}
}

// Reports usage then blocks until closed; cancellation occurs only after the
// repair reader has observed a complete usage event, avoiding timing sleeps.
type schemaCancellationBody struct {
	prefix *strings.Reader
	closed chan struct{}
	once   sync.Once
	cancel context.CancelFunc
}

func (b *schemaCancellationBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	b.cancel()
	<-b.closed
	return 0, io.ErrClosedPipe
}
func (b *schemaCancellationBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestExcelBPSSchemaRepairCancellationUsage(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "30")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			partial := &schemaCancellationBody{prefix: strings.NewReader("data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n"), closed: make(chan struct{}), cancel: cancel}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{bpsCompletionResponse(200, excelBPSRepairWire(t, "initial", "Run", `{"name":"lookup","arguments":{}}`)), {StatusCode: 200, Header: http.Header{}, Body: partial}}}
			svc := openAIClientToolsTestService(upstream)
			body := schemaGatewayRequest(stream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
			result, err := svc.Forward(ctx, c, excelAccount(), body)
			require.ErrorIs(t, err, context.Canceled)
			require.Len(t, upstream.requests, 2)
			require.NotNil(t, result)
			require.Equal(t, 14, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.True(t, result.ClientDisconnect)
			require.False(t, result.Usage.CacheReadKnown)
			require.NotContains(t, rec.Body.String(), "event: response.function_call_arguments")
		})
	}
}
