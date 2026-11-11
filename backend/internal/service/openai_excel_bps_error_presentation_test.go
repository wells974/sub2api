package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestExcelBPSPublicErrorBoundary(t *testing.T) {
	for _, mode := range []string{"", "neutral", "legacy", "invalid-private-value"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(excelBPSErrorPresentationEnv, mode)
			core, logs := observer.New(zap.InfoLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			setOpsUpstreamError(c, 524, "original upstream diagnostic", "original detail")
			for _, test := range []struct{ code, message, want string }{
				{"basispoints_upstream_error", "Excel BPS rejected this request", "request_failed"},
				{"basispoints_auth_pending", "Excel authorization is in progress", "service_unavailable"},
				{"basispoints_request_invalid", "basispoints tool schema rejected: invalid_schema", "invalid_tool_schema"},
				{"basispoints_request_invalid", "basispoints inline images exceed the 32 MiB request limit", "image_limit_exceeded"},
				{"basispoints_future_private_error", "private provider message", "request_failed"},
			} {
				original := gin.H{"type": "server_error", "code": test.code, "message": test.message, "param": "private-path", "failure_stage": "private-stage", "rejected_tool": "private-tool"}
				before, _ := json.Marshal(original)
				got := PresentExcelBPSError(c, 524, original)
				after, _ := json.Marshal(original)
				require.Equal(t, before, after)
				if mode == "legacy" {
					require.Equal(t, original, got)
					continue
				}
				require.Len(t, got, 3)
				require.Equal(t, test.want, got["code"])
				wire, _ := json.Marshal(got)
				for _, hidden := range []string{"basispoints", "Excel", "BPS", "private", "failure_stage", "param"} {
					require.NotContains(t, string(wire), hidden)
				}
			}
			require.Equal(t, 524, c.GetInt(OpsUpstreamStatusCodeKey))
			require.Equal(t, "original upstream diagnostic", c.GetString(OpsUpstreamErrorMessageKey))
			if mode != "legacy" {
				events := logs.FilterMessage("bps_error_presentation").All()
				require.Len(t, events, 5)
				require.Contains(t, events[0].ContextMap()["internal_error"], "basispoints_upstream_error")
			}
		})
	}
}

func TestExcelBPSPublicErrorForwardStatusAndDiagnostics(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		for _, status := range []int{400, 401, 403, 500, 524} {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				t.Setenv(excelBPSErrorPresentationEnv, mode)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_UPSTREAM"}}`))}}
				svc := openAIClientToolsTestService(upstream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","input":"hello"}`))
				require.ErrorContains(t, err, "basispoints_upstream_error")
				require.Equal(t, status, rec.Code)
				require.Len(t, upstream.requests, 1)
				require.True(t, IsResponseCommitted(c))
				require.Equal(t, status, c.GetInt(OpsUpstreamStatusCodeKey))
				require.Contains(t, c.GetString(OpsUpstreamErrorMessageKey), "Excel BPS")
				if mode == "legacy" {
					require.Equal(t, "basispoints_upstream_error", gjson.Get(rec.Body.String(), "error.code").String())
				} else {
					require.NotContains(t, rec.Body.String(), "basispoints")
					require.NotContains(t, rec.Body.String(), "Excel")
				}
				require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			})
		}
	}
}

func TestExcelBPSPublicErrorLocalSchemaAndRoute(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	upstream := &httpUpstreamRecorder{}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","input":"hello","tools":[{"type":"function","name":"check","parameters":{"$ref":"https://private.invalid/schema"}}]}`))
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Equal(t, 400, rec.Code)
	require.Equal(t, "invalid_tool_schema", gjson.Get(rec.Body.String(), "error.code").String())
	require.False(t, gjson.Get(rec.Body.String(), "error.param").Exists())
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	require.ErrorIs(t, writeExcelOAuthRouteError(c), errExcelOAuthRouteUnavailable)
	require.Equal(t, 400, rec.Code)
	require.Equal(t, "model_unavailable", gjson.Get(rec.Body.String(), "error.code").String())
}

func TestExcelBPSPublicImagesErrorKeepsOriginal(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(excelBPSErrorPresentationEnv, mode)
			original := &OpenAIImagesUpstreamError{StatusCode: 403, ErrorType: "invalid_request_error", Code: "basispoints_upstream_error", Message: "Excel BPS rejected this request", Param: "private-path"}
			before := *original
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			require.True(t, writeExcelBPSImagesError(c, original))
			require.Equal(t, before, *original)
			require.Equal(t, 403, rec.Code)
			if mode == "legacy" {
				require.Contains(t, rec.Body.String(), "basispoints_upstream_error")
			} else {
				require.NotContains(t, rec.Body.String(), "basispoints")
				require.False(t, gjson.Get(rec.Body.String(), "error.param").Exists())
			}
		})
	}
}

func TestExcelBPSPublicImagesForward(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024","n":1}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_PROVIDER"}}`))}}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	_, err = svc.ForwardImages(context.Background(), c, excelBPSImagesAccount(), body, parsed, "")
	var original *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &original)
	require.Equal(t, "basispoints_upstream_error", original.Code)
	require.Equal(t, 403, rec.Code)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "request_failed", gjson.Get(rec.Body.String(), "error.code").String())
	require.NotContains(t, rec.Body.String(), "PRIVATE_PROVIDER")
}

func TestExcelBPSPublicErrorsLeaveAcceptedStreamsForNextPhase(t *testing.T) {
	// Stage one must not modify the internal error event that bridge parsing,
	// billing and the second-stage SSE presentation will consume.
	var legacy string
	for _, mode := range []string{"legacy", "neutral"} {
		t.Setenv(excelBPSErrorPresentationEnv, mode)
		upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(200, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixed\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\"}}}\n\n")}
		svc := openAIClientToolsTestService(upstream)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","stream":true,"input":"hello"}`))
		require.Error(t, err)
		if mode == "legacy" {
			legacy = rec.Body.String()
		} else {
			require.Equal(t, legacy, rec.Body.String())
		}
	}
}

func TestExcelBPSPublicErrorCompactedKeepaliveStaysLegacy(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 524, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()
	value, _ := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, value.(*openAICompactSSEKeepalive).beat())
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","input":"hello"}`))
	require.Error(t, err)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "basispoints_upstream_error")
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
}

func TestExcelBPSPublicErrorDiagnosticRedaction(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	PresentExcelBPSError(c, 502, gin.H{"code": "basispoints_upstream_error", "message": "Excel BPS Bearer private-bearer https://private-user:private-pass@example.com/?token=private-query", "access_token": "private-key"})
	entries := logs.FilterMessage("bps_error_presentation").All()
	require.Len(t, entries, 1)
	diagnostic := entries[0].ContextMap()["internal_error"].(string)
	require.Contains(t, diagnostic, "basispoints_upstream_error")
	for _, secret := range []string{"private-bearer", "private-user", "private-pass", "private-query", "private-key"} {
		require.NotContains(t, diagnostic, secret)
	}
}
