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

func TestExcelBPSRequestDiagnosticsIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"success", "arguments", "compile", "image"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "req-observation")))
				args := `{"name":"check","arguments":{"count":1}}`
				if scenario == "arguments" {
					args = `{"name":"check","arguments":{"count":"PRIVATE_ARGUMENT"}}`
				}
				upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(http.StatusOK, excelBPSRepairWire(t, "observation", "Run", args))}
				svc := openAIClientToolsTestService(upstream)
				input := `"PRIVATE_PROMPT"`
				schema := `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`
				if scenario == "compile" {
					schema = `{"$ref":"https://example.invalid/?token=PRIVATE_TOKEN"}`
				}
				if scenario == "image" {
					input = `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,PRIVATE_INVALID"}]}]`
				}
				body := []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":%s,"tools":[{"type":"function","name":"check","parameters":%s}]}`, stream, input, schema))
				before := bytes.Clone(body)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				result, err := svc.Forward(ctx, c, excelAccount(), body)
				if scenario == "success" {
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rec.Code)
				} else {
					require.Error(t, err)
				}
				require.Equal(t, before, body)
				events := logs.FilterMessage("bps_request_diagnostics").All()
				require.Len(t, events, 1)
				encoded, marshalErr := json.Marshal(events[0].ContextMap())
				require.NoError(t, marshalErr)
				diagnostic := string(encoded)
				require.Equal(t, "req-observation", gjson.Get(diagnostic, "request_id").String())
				require.Equal(t, scenario != "success", gjson.Get(diagnostic, "forward_failed").Bool())
				require.NotContains(t, diagnostic, "PRIVATE_")
				require.NotContains(t, rec.Body.String(), "schema_failure")
				require.NotContains(t, rec.Body.String(), "diagnostics_version")
				switch scenario {
				case "success", "arguments":
					require.Len(t, upstream.requests, 1, "diagnostics must not add retries")
					require.NotNil(t, result)
					require.EqualValues(t, 10, result.Usage.InputTokens)
					require.EqualValues(t, 2, result.Usage.OutputTokens)
					require.True(t, gjson.Get(diagnostic, "catalog_prepared").Bool())
					require.EqualValues(t, 1, gjson.Get(diagnostic, "catalog.tool_count").Int())
					require.Positive(t, gjson.Get(diagnostic, "catalog.prompt_bytes").Int())
					if scenario == "arguments" {
						require.Equal(t, "tool_arguments", gjson.Get(diagnostic, "schema_phase").String())
						require.Equal(t, "type", gjson.Get(diagnostic, "schema_failure.keyword").String())
						require.Equal(t, "/count", gjson.Get(diagnostic, "schema_failure.path").String())
						require.Contains(t, rec.Body.String(), "basispoints function arguments do not satisfy the declared tool schema or validation resource limits")
					} else {
						require.False(t, gjson.Get(diagnostic, "schema_phase").Exists())
					}
				case "compile":
					require.Empty(t, upstream.requests)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.False(t, gjson.Get(diagnostic, "catalog_prepared").Bool())
					require.Equal(t, "catalog_compile", gjson.Get(diagnostic, "schema_phase").String())
					require.Equal(t, "external_reference_blocked", gjson.Get(diagnostic, "schema_reason").String())
				case "image":
					require.Empty(t, upstream.requests)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.EqualValues(t, 1, gjson.Get(diagnostic, "input_images.inline_count").Int())
					require.EqualValues(t, 1, gjson.Get(diagnostic, "input_images.unknown_inline_byte_count").Int())
				}
			})
		}
	}
}
