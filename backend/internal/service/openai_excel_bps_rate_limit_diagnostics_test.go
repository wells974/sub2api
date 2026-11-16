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

func TestExcelBPSRateLimitDiagnosticsForwardAndIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "req-rate-limit"), zap.String("client_request_id", "client-rate-limit")))
			message := "Token limit: used 29000, requested 22000; retry in 30s; test-token test-account private-refresh https://example.invalid/?token=private-query owner@example.invalid"
			payload, err := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"code": "rate_limit_exceeded", "type": "tokens", "message": message, "input": "PRIVATE_PROMPT"}})
			require.NoError(t, err)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: error\ndata: " + string(payload) + "\n\n"))}}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			account.Credentials["refresh_token"] = "private-refresh"
			body := []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":"PRIVATE_PROMPT"}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
			_, _ = svc.Forward(ctx, c, account, body)
			require.Len(t, upstream.requests, 1, "diagnostics must not retry")
			require.True(t, svc.isExcelBPSCoolingDown(account, "gpt-6.1-sol"))
			events := logs.FilterMessage("bps_stream_rate_limited").All()
			require.Len(t, events, 1)
			fields := events[0].ContextMap()
			require.Equal(t, "req-rate-limit", fields["request_id"])
			require.Equal(t, "client-rate-limit", fields["client_request_id"])
			require.EqualValues(t, account.ID, fields["account_id"])
			detail := fields["errors"].(string)
			require.Equal(t, "tokens", gjson.Get(detail, "upstream_type").String())
			require.Equal(t, "rate_limit_exceeded", gjson.Get(detail, "upstream_code").String())
			require.Contains(t, detail, "used 29000, requested 22000; retry in 30s")
			for _, private := range []string{"test-token", "test-account", "private-refresh", "private-query", "owner@example.invalid", "PRIVATE_PROMPT"} {
				require.NotContains(t, detail, private)
			}
			require.NotContains(t, rec.Body.String(), "requested 22000")
			require.NotContains(t, rec.Body.String(), "upstream_type")
			var stored []*OpsInsertSystemLogInput
			sink := NewOpsSystemLogSink(&opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
				stored = inputs
				return int64(len(inputs)), nil
			}})
			event := &logger.LogEvent{Level: events[0].Level.String(), Message: events[0].Message, Fields: fields}
			require.True(t, sink.shouldIndex(event), "must appear in backend system logs")
			_, err = sink.flushBatch(ctx, []*logger.LogEvent{event})
			require.NoError(t, err)
			require.Len(t, stored, 1)
			require.Equal(t, "req-rate-limit", stored[0].RequestID)
			require.Equal(t, detail, gjson.Get(stored[0].ExtraJSON, "errors").String(), "the existing UI errors field must survive storage")
		})
	}
}

func TestSanitizeBPSRateLimitDiagnostic(t *testing.T) {
	for _, secret := range []string{
		"Bearer private-bearer", "https://u:p@example.invalid/path?token=private-query",
		"person@example.invalid", "sk-private-api-key", "org-private-org", "proj_private-project",
		"eyJhbGciOiJub25lIn0.eyJzdWIiOiJwcml2YXRlIn0.signature",
		`password="private password with spaces"`, "api_key=private-key", "password=private-password", "cookie: session=private-session; csrf=private-csrf",
		"prompt: private user content with spaces", `"input":"private user content"`,
		"file-private-file", "/api/bps-images/private-capability", "KNOWN-SECRET",
	} {
		t.Run(secret, func(t *testing.T) {
			got := sanitizeBPSRateLimitDiagnostic("Token limit 30000; retry in 30s; "+secret, []string{"KNOWN-SECRET"})
			require.Contains(t, got, "Token limit 30000; retry in 30s")
			require.NotContains(t, got, "private")
			require.NotContains(t, got, secret)
			require.NotContains(t, got, "KNOWN-SECRET")
			require.NotContains(t, got, "eyJ")
		})
	}
	require.Equal(t, "a b c", sanitizeBPSRateLimitDiagnostic("a\nb\u202ec", nil))
	require.LessOrEqual(t, len(sanitizeBPSRateLimitDiagnostic(strings.Repeat("a", 4096), nil)), 2100)
	require.Equal(t, "[omitted: oversized upstream field]", sanitizeBPSRateLimitDiagnostic(strings.Repeat("a", 9000), nil))
}
