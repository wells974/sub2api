package handler

import (
	"net/http"
	"net/http/httptest"
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

func TestExcelBPSPublicFailoverLeavesStartedStream(t *testing.T) {
	t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", "")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	h := &OpenAIGatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{Reason: service.ExcelBPSRateLimitedReason, StatusCode: 429, ClientStatusCode: 429, ClientMessage: "Excel BPS rate limit exceeded, please retry later"}, true)
	require.Contains(t, rec.Body.String(), "basispoints_rate_limited")
}
