package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPublicCompactTerminalModes(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(excelBPSErrorPresentationEnv, mode)
			upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(200, bpsFailureWire(t, "response.failed", "rate_limit_exceeded", "", 0))}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
			MarkOpenAICompactClientStream(c)
			stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
			defer stop()
			value, _ := c.Get(openAICompactSSEKeepaliveKey)
			require.True(t, value.(*openAICompactSSEKeepalive).beat())
			result, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","input":"hello"}`))
			require.Error(t, err)
			require.Equal(t, 200, rec.Code)
			require.EqualValues(t, 7, result.Usage.InputTokens)
			require.EqualValues(t, 2, result.Usage.OutputTokens)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := strings.TrimPrefix(line, "data: ")
				if gjson.Get(payload, "type").String() != "response.failed" {
					continue
				}
				require.NotEmpty(t, gjson.Get(payload, "response.id").String())
				require.Positive(t, gjson.Get(payload, "response.created_at").Int())
				require.Equal(t, "failed", gjson.Get(payload, "response.status").String())
				require.Equal(t, "[]", gjson.Get(payload, "response.output").Raw)
				require.Equal(t, "rate_limit_exceeded", gjson.Get(payload, "response.error.code").String())
			}
			ops, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, 429, ops.IntendedStatus)
			require.Contains(t, ops.Message, "Excel BPS")
			if mode == "legacy" {
				require.Contains(t, rec.Body.String(), "Excel BPS")
			} else {
				require.NotContains(t, rec.Body.String(), "Excel BPS")
			}
		})
	}
}

func TestExcelBPSPublicEventPreservesEnvelope(t *testing.T) {
	for _, kind := range []string{"error", "response.failed", "response.cancelled", "response.incomplete"} {
		for _, mode := range []string{"", "legacy"} {
			t.Run(kind+mode, func(t *testing.T) {
				t.Setenv(excelBPSErrorPresentationEnv, mode)
				payload := fmt.Sprintf(`{"type":%q,"sequence_number":9,"status":429,"usage":{"input_tokens":9007199254740993},"error":{"status":429,"code":"rate_limit_exceeded","message":"Excel BPS private","param":"private"},"response":{"id":"resp_original","status":"incomplete","output":[{"text":"Excel is user content"}],"usage":{"output_tokens":12},"error":{"code":"basispoints_protocol_error","message":"private","failure_stage":"private"},"incomplete_details":{"reason":"max_output_tokens","private":"Excel"}}}`, kind)
				before := []byte(payload)
				got := presentExcelBPSErrorEvent(nil, before)
				require.Equal(t, payload, string(before))
				if mode == "legacy" {
					require.Equal(t, payload, string(got))
					return
				}
				for _, path := range []string{"type", "sequence_number", "status", "usage", "response.id", "response.status", "response.output", "response.usage"} {
					require.Equal(t, gjson.Get(payload, path).Raw, gjson.GetBytes(got, path).Raw, path)
				}
				require.NotContains(t, string(got), "private")
				require.NotContains(t, string(got), "basispoints")
				require.Equal(t, "rate_limit_exceeded", gjson.GetBytes(got, "error.code").String())
				require.EqualValues(t, 429, gjson.GetBytes(got, "error.status").Int())
				require.Equal(t, "max_output_tokens", gjson.GetBytes(got, "response.incomplete_details.reason").String())
			})
		}
	}
}

func TestExcelBPSPublicEventLeavesSuccessfulContent(t *testing.T) {
	t.Setenv(excelBPSErrorPresentationEnv, "")
	for _, line := range []string{": keepalive", "", "event: response.failed", "data: [DONE]",
		`data: {"type":"response.output_text.delta","delta":"Excel BPS basispoints private"}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"text":"Excel BPS"}],"usage":{"input_tokens":9007199254740993}}}`} {
		require.Equal(t, line, presentExcelBPSErrorLine(nil, line))
	}
}

func TestExcelBPSPublicTerminalModes(t *testing.T) {
	for _, scenario := range []string{"rate", "cancelled", "incomplete", "eof", "schema"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				var previousUsage OpenAIUsage
				var previousOps OpsStreamError
				var previousError string
				for _, mode := range []string{"legacy", ""} {
					t.Setenv(excelBPSErrorPresentationEnv, mode)
					wire := bpsFailureWire(t, "response.failed", "rate_limit_exceeded", "", 0)
					switch scenario {
					case "cancelled":
						wire = bpsFailureWire(t, "response.cancelled", "", "", 0)
					case "incomplete":
						wire = `data: {"type":"response.incomplete","response":{"id":"resp_fixed","status":"incomplete","usage":{"input_tokens":7,"output_tokens":2},"incomplete_details":{"reason":"Excel BPS private"}}}` + "\n\n"
					case "eof":
						wire = `data: {"type":"response.output_text.delta","delta":"visible content"}` + "\n\n"
					case "schema":
						wire = excelBPSRepairWire(t, "original", "Run", `{"name":"shell","arguments":{"cmd":7}}`)
					}
					upstream := &httpUpstreamRecorder{resp: bpsCompletionResponse(200, wire)}
					svc := openAIClientToolsTestService(upstream)
					account := excelAccount()
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
					body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"hello","tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`, stream))
					result, err := svc.Forward(context.Background(), c, account, body)
					require.Error(t, err)
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, scenario == "rate", svc.isExcelBPSCoolingDown(account, "gpt-6-astra"))
					status := 502
					if scenario == "rate" {
						status = 429
					}
					if stream {
						status = 200
					}
					require.Equal(t, status, rec.Code)
					observed, _ := GetOpsStreamError(c)
					if mode == "legacy" {
						previousUsage, previousOps, previousError = result.Usage, observed, err.Error()
					} else {
						require.Equal(t, previousUsage, result.Usage)
						require.Equal(t, previousError, err.Error())
						require.Equal(t, previousOps.Code, observed.Code)
						require.Equal(t, previousOps.Message, observed.Message)
						require.Equal(t, previousOps.IntendedStatus, observed.IntendedStatus)
						require.Equal(t, previousOps.BridgeFailure, observed.BridgeFailure)
						for _, secret := range []string{"Excel", "BPS", "basispoints", "failure_stage", "repair_skipped_reason"} {
							require.NotContains(t, rec.Body.String(), secret)
						}
						if stream {
							terminal := result.UpstreamTerminalEvent
							if terminal == "" {
								terminal = "response.failed"
							}
							require.Equal(t, 1, strings.Count(rec.Body.String(), "event: "+terminal))
						}
					}
				}
			})
		}
	}
}
