package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcelBPSPublicStoredErrors(t *testing.T) {
	for _, mode := range []string{"", "neutral", "legacy", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(excelBPSErrorPresentationEnv, mode)
			for _, tc := range []struct {
				name, endpoint, message, body string
				sensitive                     bool
			}{
				{"historic", "/basispoints/api/responses", "Excel BPS rejected this request", `{"error":{"code":"basispoints_upstream_error","message":"PRIVATE","param":"PRIVATE"},"debug":"PRIVATE"}`, true},
				{"schema", "", "basispoints function arguments do not satisfy the declared tool schema", `{"response":{"error":{"code":"basispoints_protocol_error","message":"basispoints function arguments invalid","failure_stage":"PRIVATE"}}}`, true},
				{"body only", "", "request failed", `{"error":{"code":"basispoints_future","message":"PRIVATE"}}`, true},
				{"escaped body", "", "request failed", `{"error":{"code":"\u0062asispoints_future","message":"PRIVATE"}}`, true},
				{"html", "/basispoints/api/responses", "request failed", "<html>PRIVATE</html>", true},
				{"config", "", "invalid SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "PRIVATE", true},
				{"legacy title", "", "Excel / BPS unavailable", "PRIVATE", true},
				{"image relay", "", "BPS image relay requires an HTTPS public origin", "PRIVATE", true},
				{"truncated", "", "Excel OAuth not available", "data: {\"type\":\"error\",PRIVATE", true},
				{"other route", "/v1/responses", "Ordinary validation failure", `{"error":{"code":"invalid_request","message":"ordinary"}}`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					owner, upstreamStatus := int64(42), 200
					original := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{
						ID: 5, UserID: &owner, StatusCode: 524, Phase: "upstream", Type: "server_error", Platform: "openai",
						UpstreamEndpoint: tc.endpoint, Message: tc.message, RequestedModel: "requested", Model: "mapped",
						APIKeyName: "my Excel key", GroupName: "my BPS group", Stream: true,
					}, ErrorBody: tc.body, UpstreamStatusCode: &upstreamStatus, UpstreamErrorDetail: "PRIVATE_DIAGNOSTIC"}
					before, err := json.Marshal(original)
					require.NoError(t, err)
					svc := &OpsService{opsRepo: &stubOpsRepoForUserErr{detailToReturn: original}}
					got, err := svc.GetUserErrorRequestDetail(context.Background(), owner, 5)
					require.NoError(t, err)
					if mode == "legacy" || !tc.sensitive {
						require.Equal(t, tc.body, got.ErrorBody)
						require.Equal(t, tc.message, got.Message)
					} else {
						require.NotEqual(t, tc.message, got.Message)
						for _, hidden := range []string{"Excel", "BPS", "basispoints", "PRIVATE", "param", "failure_stage"} {
							require.NotContains(t, got.ErrorBody, hidden)
						}
						require.True(t, json.Valid([]byte(got.ErrorBody)))
					}
					require.Equal(t, 524, got.StatusCode)
					require.Equal(t, 200, *got.UpstreamStatusCode)
					require.Equal(t, "requested", got.Model)
					require.Equal(t, "my Excel key", got.KeyName)
					require.Equal(t, "my BPS group", got.GroupName)
					_, err = svc.GetUserErrorRequestDetail(context.Background(), 999, 5)
					require.Error(t, err)
					observer, err := svc.GetObserverErrorRequestDetail(context.Background(), owner, 5)
					require.NoError(t, err)
					require.Equal(t, tc.body, observer.ErrorBody)
					require.Equal(t, tc.message, observer.Message)
					admin, err := svc.GetErrorLogByID(context.Background(), 5)
					require.NoError(t, err)
					require.Equal(t, tc.body, admin.ErrorBody)
					after, err := json.Marshal(original)
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		})
	}
	require.Nil(t, toPublicUserErrorRequest(nil))
	require.Nil(t, toPublicUserErrorRequestDetail(nil))
}
