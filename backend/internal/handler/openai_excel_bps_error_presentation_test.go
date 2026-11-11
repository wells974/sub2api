package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPublicFailoverPresentation(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
			for _, tc := range []struct {
				reason service.GatewayFailureReason
				status int
				code   string
			}{
				{service.ExcelBPSRateLimitedReason, 429, "rate_limit_exceeded"},
				{service.ExcelBPSImageRequestBusyReason, 503, "service_unavailable"},
				{service.ExcelBPSImageBodyTooLargeReason, 413, "request_too_large"},
			} {
				err := &service.UpstreamFailoverError{Reason: tc.reason, StatusCode: tc.status, ClientStatusCode: tc.status, ClientMessage: "Excel BPS original diagnostic", ResponseHeaders: http.Header{"Retry-After": {"7"}}}
				before := *err
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				h := &OpenAIGatewayHandler{}
				h.handleFailoverExhausted(c, err, false)
				require.Equal(t, before, *err)
				require.Equal(t, tc.status, rec.Code)
				require.Equal(t, "7", rec.Header().Get("Retry-After"))
				if mode == "legacy" {
					require.Equal(t, string(tc.reason), gjson.Get(rec.Body.String(), "error.code").String())
				} else {
					require.Equal(t, tc.code, gjson.Get(rec.Body.String(), "error.code").String())
					require.NotContains(t, rec.Body.String(), "Excel")
				}
				if tc.reason == service.ExcelBPSRateLimitedReason {
					require.Equal(t, "Excel BPS original diagnostic", c.GetString(service.OpsUpstreamErrorMessageKey))
				}
			}
		})
	}
}

func TestExcelBPSPublicProtocolErrorExits(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/chat/completions", "/v1/messages"} {
		for _, mode := range []string{"", "legacy"} {
			t.Run(path+mode, func(t *testing.T) {
				t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, nil)
				c.Writer.WriteHeader(200)
				c.Writer.Flush()
				h := &OpenAIGatewayHandler{}
				h.handleExcelBPSClientError(c, 429, "rate_limit_error", "basispoints_rate_limited", "Excel BPS original", true)
				require.Equal(t, 200, rec.Code)
				ops, ok := service.GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, "Excel BPS original", ops.Message)
				require.Equal(t, 429, ops.IntendedStatus)
				event := "error"
				if strings.Contains(path, "/responses") {
					event = "response.failed"
				}
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: "+event+"\n"))
				if mode == "legacy" {
					require.Contains(t, rec.Body.String(), "basispoints_rate_limited")
				} else {
					require.NotContains(t, rec.Body.String(), "basispoints")
					require.NotContains(t, rec.Body.String(), "Excel")
					require.Contains(t, rec.Body.String(), "rate_limit_exceeded")
				}
			})
		}
	}
}

func TestExcelBPSPublicFailoverStartedStream(t *testing.T) {
	t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", "")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	h := &OpenAIGatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{Reason: service.ExcelBPSRateLimitedReason, StatusCode: 429, ClientStatusCode: 429, ClientMessage: "Excel BPS rate limit exceeded, please retry later"}, true)
	require.NotContains(t, rec.Body.String(), "basispoints_rate_limited")
	require.Contains(t, rec.Body.String(), "rate_limit_exceeded")
	ops, ok := service.GetOpsStreamError(c)
	require.True(t, ok)
	require.Contains(t, ops.Message, "Excel BPS")
	require.Equal(t, 429, ops.IntendedStatus)
}
