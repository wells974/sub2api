package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPublicReviewDiagnosticVariants(t *testing.T) {
	for _, body := range []string{
		`{"metadata":{"vendor":"\u0062asispoints"},"error":{"message":"unknown"}}`,
		"event: error\ndata: {\"metadata\":{\"vendor\":\"\\u0045xcel/BPS\"}}\n\n",
		`{"error":{"message":"Excel-BPS failed"}}`,
		`{"error":{"code":"OPENAI_EXCEL_AUTH_PENDING"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			for _, mode := range []string{"", "legacy"} {
				t.Setenv(excelBPSErrorPresentationEnv, mode)
				record := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{Message: "unknown", StatusCode: 502}, ErrorBody: body}
				got := toPublicUserErrorRequestDetail(record)
				if mode == "legacy" {
					require.Equal(t, body, got.ErrorBody)
				} else {
					require.NotEqual(t, body, got.ErrorBody)
					require.True(t, json.Valid([]byte(got.ErrorBody)))
					require.False(t, hasExcelBPSDiagnosticMarker(got.ErrorBody))
				}
				require.Equal(t, body, record.ErrorBody)
			}
		})
	}
	for _, body := range []string{`{"error":{"message":"Excel file is too large"}}`, `{"message":"10 Mbps transfer failed"}`} {
		require.False(t, hasExcelBPSDiagnosticMarker(body))
	}
	for _, code := range []string{"OPENAI_EXCEL_AUTH_PENDING", "openai_excel_auth_pending"} {
		t.Setenv(excelBPSErrorPresentationEnv, "")
		record := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{Type: code, Message: code, StatusCode: 503}}
		require.Equal(t, "service_unavailable", gjson.Get(toPublicUserErrorRequestDetail(record).ErrorBody, "error.code").String())
	}
}

func TestExcelBPSPublicReviewHeaderVariants(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	resp := &http.Response{Header: http.Header{"Server": {"Excel"}, "X-Provider": {"BPS"}, "X-Excel-Product": {"internal"}, "Retry-After": {"5"}, "X-Request-Id": {"opaque"}}}
	original := resp.Header.Clone()
	got := presentExcelBPSImageResponse(nil, resp)
	for _, key := range []string{"Server", "X-Provider", "X-Excel-Product"} {
		require.Empty(t, got.Header.Get(key))
	}
	require.Equal(t, "5", got.Header.Get("Retry-After"))
	require.Equal(t, "opaque", got.Header.Get("X-Request-Id"))
	require.Equal(t, original, resp.Header)
}

func TestExcelBPSPublicReviewOpsReceiptIsolation(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		t.Setenv(excelBPSErrorPresentationEnv, mode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		original := gin.H{"type": "server_error", "code": "basispoints_upstream_error", "message": "Excel BPS original", "param": "private"}
		public := PresentExcelBPSError(c, 502, original)
		wire, err := json.Marshal(gin.H{"error": public})
		require.NoError(t, err)
		before := string(wire)
		internal := RestoreOpsErrorPresentation(c, wire)
		require.Equal(t, "Excel BPS original", gjson.GetBytes(internal, "error.message").String())
		require.Equal(t, before, string(wire))
		other := []byte(`{"error":{"code":"service_unavailable","message":"a later route failed","type":"server_error"}}`)
		require.Equal(t, other, RestoreOpsErrorPresentation(c, other))
		if mode == "legacy" {
			require.Equal(t, wire, internal)
		}
	}
}
