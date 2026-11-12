package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestExcelBPSLatencyIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("DATA_DIR", t.TempDir())
	for _, stream := range []bool{true, false} {
		for _, scenario := range []string{"tool", "arguments", "image", "http_failure"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "false")
				t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", "neutral")
				core, logs := observer.New(zap.InfoLevel)
				ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "latency-integration")))
				args := `{"name":"check","arguments":{"count":1}}`
				if scenario == "arguments" {
					args = `{"name":"check","arguments":{"count":"PRIVATE_ARGUMENT"}}`
				}
				status := http.StatusOK
				if scenario == "http_failure" {
					status = http.StatusBadRequest
				}
				upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(status, excelBPSRepairWire(t, "latency", "Run", args))}
				svc := openAIClientToolsTestService(upstream)
				input := `"PRIVATE_PROMPT"`
				if scenario == "image" {
					input = `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,PRIVATE_INVALID"}]}]`
				}
				body := []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":%s,"tools":[{"type":"function","name":"check","parameters":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}}]}`, stream, input))
				before := bytes.Clone(body)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				result, err := svc.Forward(ctx, c, excelAccount(), body)
				if scenario == "tool" {
					require.NoError(t, err)
					require.Equal(t, 200, rec.Code)
					require.EqualValues(t, 10, result.Usage.InputTokens)
					require.EqualValues(t, 2, result.Usage.OutputTokens)
					require.NotNil(t, result.FirstTokenMs, "existing first-token metric remains populated")
				} else {
					require.Error(t, err)
				}
				require.Equal(t, before, body)
				require.NotContains(t, rec.Body.String(), "stages_ms")
				require.NotContains(t, rec.Body.String(), "first_tool_ms")
				events := logs.FilterMessage("bps_latency_diagnostics").All()
				require.Len(t, events, 1)
				raw, err := json.Marshal(events[0].ContextMap())
				require.NoError(t, err)
				require.NotContains(t, string(raw), "PRIVATE")
				d := gjson.ParseBytes(raw)
				require.Equal(t, "latency-integration", d.Get("request_id").String())
				require.Equal(t, stream, d.Get("stream").Bool())
				require.Equal(t, scenario != "tool", d.Get("forward_failed").Bool())
				require.True(t, d.Get("latency.stages_ms.setup").Exists())
				if scenario == "image" {
					require.Empty(t, upstream.requests)
					require.False(t, d.Get("latency.upstream.first_event_ms").Exists())
					require.False(t, d.Get("latency.stages_ms.generation_headers").Exists())
				} else {
					require.Len(t, upstream.requests, 1, "observability must not add upstream calls")
					require.True(t, d.Get("latency.stages_ms.generation_headers").Exists())
				}
				if scenario == "tool" {
					require.Equal(t, "tool_only", d.Get("latency.upstream.output_type").String())
					if stream {
						require.Equal(t, "tool_only", d.Get("latency.client_write.output_type").String())
						require.True(t, d.Get("latency.client_write.first_tool_ms").Exists())
						require.False(t, d.Get("latency.client_write.first_text_ms").Exists())
					} else {
						require.Equal(t, "none", d.Get("latency.client_write.output_type").String())
						require.True(t, d.Get("latency.json_return_ms").Exists())
					}
				}
			})
		}
	}
}
