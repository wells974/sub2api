package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A managed BPS parent keeps the legacy authorization marker because it never
// receives a legacy Excel grant. The request is sent with the projected
// connection account, but usage is recorded with the scheduled parent.
func managedBPSBillingParent(option any) *Account {
	extra := map[string]any{
		"openai_excel_bps":              true,
		ExcelBPSAuthorizationPendingKey: true,
		"openai_connections_managed":    true,
		"openai_excel_bps_mihomo":       false,
		"openai_excel_bps_models":       []any{"gpt-6-astra"},
	}
	if option != nil {
		extra["openai_excel_bps_cache_creation_as_input"] = option
	}
	return &Account{ID: 42, Name: "managed-bps", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{"access_token": "synthetic-legacy-token", "chatgpt_account_id": "synthetic-workspace"}, Extra: extra}
}

func managedBPSBillingUsageService() (*OpenAIGatewayService, *openAIRecordUsageLogRepoStub, *openAIRecordUsageBillingRepoStub) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6-astra": {
			InputCostPerToken: 5e-6, OutputCostPerToken: 30e-6,
			CacheCreationInputTokenCost: 6.25e-6, CacheCreationInputTokenCostExplicit: true,
			CacheReadInputTokenCost: 0.5e-6,
		},
	}})
	return svc, usageRepo, billingRepo
}

func TestManagedBPSConnectionBillsCacheCreationAsShownToClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, option := range []bool{true, false} {
			for _, ttl := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%t/option=%t/ttl=%t", stream, option, ttl), func(t *testing.T) {
					reauth, repo := connectionTypeService(AccountTypeOAuth)
					repo.managed = true
					connection := &OpenAIAuthConnection{ID: "2b6f0f4e-7c3d-4b8e-9f21-5a7c9d1e3f40", AccountID: 42, Kind: "bps", Status: "active", RefreshState: "idle",
						Revision: 1, Preferred: true, Capabilities: connectionCapabilities("bps", "")}
					require.NoError(t, reauth.encryptConnectionCredentials(connection, map[string]any{
						"access_token":       "synthetic-connection-token",
						"chatgpt_account_id": "synthetic-workspace",
						"client_id":          openai.ExcelClientID,
						"expires_at":         strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
					}))
					repo.rows = []*OpenAIAuthConnection{connection}

					parent := managedBPSBillingParent(option)
					require.False(t, parent.IsExcelBPSEnabled(), "fixture must carry the legacy authorization marker")

					usagePayload := excelBPSUsageWithCreation
					if !ttl {
						for _, details := range []string{"input_tokens_details", "prompt_tokens_details"} {
							var err error
							usagePayload, err = sjson.Delete(usagePayload, details+".cache_write_tokens_details")
							require.NoError(t, err)
						}
					}
					wire := `data: {"type":"response.completed","response":{"id":"resp_managed_bps_usage","status":"completed","model":"gpt-6-astra","output":[],"usage":` + usagePayload + `}}` + "\n\n"
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
					gateway := openAIClientToolsTestService(upstream)
					gateway.excelOAuthReauth = reauth
					body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"managed billing regression"}`, stream))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

					result, err := gateway.Forward(context.Background(), c, parent, body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
					require.Equal(t, "Bearer synthetic-connection-token", upstream.lastReq.Header.Get("Authorization"), "the request must be sent through the managed connection")
					require.Equal(t, connection.ID, c.GetString("openai_auth_connection_id"))
					require.Equal(t, 200, result.Usage.CacheCreationInputTokens, "retain the upstream measurement for local billing")

					response := rec.Body.Bytes()
					if stream {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if strings.HasPrefix(line, "data: ") {
								response = []byte(gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw)
							}
						}
					}
					shown, ok := extractOpenAIUsageFromJSONBytes(response)
					require.True(t, ok)
					wantInput, wantCreation := 700, 200
					if option {
						wantInput, wantCreation = 900, 0
					}
					require.Equal(t, wantCreation, shown.CacheCreationInputTokens)
					want30m, want12h := 0, 0
					cacheCost := float64(wantCreation) * 6.25e-6
					if ttl && !option {
						want30m, want12h = 150, 50
						// Upstream's default 12h write price is twice its 30m price.
						cacheCost = 150*6.25e-6 + 50*12.5e-6
					}
					require.Equal(t, want30m, shown.CacheCreation30mInputTokens)
					require.Equal(t, want12h, shown.CacheCreation12hInputTokens)

					usageSvc, usageRepo, billingRepo := managedBPSBillingUsageService()
					require.NoError(t, usageSvc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: result, APIKey: &APIKey{ID: 1001}, User: &User{ID: 2001}, Account: parent,
					}))
					require.NotNil(t, usageRepo.lastLog)
					log := usageRepo.lastLog
					require.Equal(t, shown.CacheCreationInputTokens, log.CacheCreationTokens, "billing must match the usage returned to the client")
					require.Equal(t, wantInput, log.InputTokens)
					require.Equal(t, wantCreation, log.CacheCreationTokens)
					require.Equal(t, want30m, log.CacheCreation30mTokens)
					require.Equal(t, want12h, log.CacheCreation12hTokens)
					require.Equal(t, 100, shown.CacheReadInputTokens)
					require.Equal(t, shown.CacheReadInputTokens, log.CacheReadTokens)
					require.InDelta(t, float64(wantInput)*5e-6, log.InputCost, 1e-12)
					require.InDelta(t, cacheCost, log.CacheCreationCost, 1e-12)
					// The response reports gross input. The ledger partitions that same
					// request into mutually exclusive buckets before posting one debit.
					require.Equal(t, shown.InputTokens, log.InputTokens+log.CacheCreationTokens+log.CacheReadTokens)
					require.Equal(t, shown.OutputTokens, log.OutputTokens)
					wantTotal := float64(wantInput)*5e-6 + cacheCost + 100*0.5e-6 + 50*30e-6
					require.InDelta(t, wantTotal, log.TotalCost, 1e-12)
					require.Equal(t, 1, billingRepo.calls)
					require.NotNil(t, billingRepo.lastCmd)
					require.InDelta(t, wantTotal*1.1, billingRepo.lastCmd.BalanceCost, 1e-12)
					require.Equal(t, wantInput, billingRepo.lastCmd.InputTokens)
					require.Equal(t, wantCreation, billingRepo.lastCmd.CacheCreationTokens)
				})
			}
		}
	}
}

func TestExcelBPSCacheCreationBilledAsInputScope(t *testing.T) {
	bpsResult := &OpenAIForwardResult{UpstreamEndpoint: "/basispoints/api/responses"}
	legacy := func(option any) *Account {
		extra := map[string]any{"openai_excel_bps": true}
		if option != nil {
			extra["openai_excel_bps_cache_creation_as_input"] = option
		}
		return &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
	}
	for _, tt := range []struct {
		name    string
		account *Account
		result  *OpenAIForwardResult
		want    bool
	}{
		{"legacy BPS enabled", legacy(true), bpsResult, true},
		{"legacy BPS option off", legacy(false), bpsResult, false},
		{"legacy BPS option missing", legacy(nil), bpsResult, false},
		{"legacy BPS pending keeps option hidden", func() *Account {
			a := legacy(true)
			a.Extra[ExcelBPSAuthorizationPendingKey] = true
			return a
		}(), bpsResult, false},
		{"legacy BPS disabled", func() *Account { a := legacy(true); a.Extra["openai_excel_bps"] = false; return a }(), bpsResult, false},
		{"managed parent option on", managedBPSBillingParent(true), bpsResult, true},
		{"managed parent option off", managedBPSBillingParent(false), bpsResult, false},
		{"managed parent option missing", managedBPSBillingParent(nil), bpsResult, false},
		{"managed parent invalid option", managedBPSBillingParent("true"), bpsResult, false},
		{"managed parent native result", managedBPSBillingParent(true), &OpenAIForwardResult{UpstreamEndpoint: "/v1/responses"}, false},
		{"legacy native result", legacy(true), &OpenAIForwardResult{UpstreamEndpoint: "/v1/responses"}, false},
		{"nil account", nil, bpsResult, false},
		{"nil result", managedBPSBillingParent(true), nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, excelBPSCacheCreationBilledAsInput(tt.account, tt.result))
		})
	}
}

// A gateway request may use two model generations for one corrected tool call.
// Only the first generation's progress snapshots carry its billable usage.
func TestExcelBPSSchemaRepairBillsProgressAndCorrectionOnce(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	const progress = `{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":20,"cache_write_tokens_details":{"ttl_30m_tokens":15,"ttl_12h_tokens":5}}}`
	const corrected = `{"prompt_tokens":60,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":10,"cache_write_tokens_details":{"ttl_30m_tokens":8,"ttl_12h_tokens":2}}}`
	terminal := func(t *testing.T, id, args, usage string) string {
		wire := excelBPSRepairWire(t, id, "Run", args)
		_, data, ok := strings.Cut(wire, "data: ")
		require.True(t, ok)
		data, err := sjson.SetRaw(strings.TrimSpace(data), "response.usage", usage)
		require.NoError(t, err)
		return "data: " + data + "\n\n"
	}
	for _, stream := range []bool{false, true} {
		for _, asInput := range []bool{false, true} {
			for _, envelope := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%t/asInput=%t/envelope=%t", stream, asInput, envelope), func(t *testing.T) {
					snapshot := "data: " + `{"type":"response.in_progress","response":{"id":"resp_first","usage":` + progress + `}}` + "\n\n"
					firstWire := terminal(t, "first", `{"name":"lookup","arguments":{}}`, `{"input_tokens":0,"output_tokens":0}`)
					if envelope {
						// Some providers repeat the first generation at the envelope level.
						firstWire = strings.Replace(firstWire, "data: {", "data: {\"usage\":"+progress+",", 1)
					}
					first := bpsCompletionResponse(200, snapshot+snapshot+firstWire)
					first.Header.Set("X-Request-ID", "request-with-schema-repair")
					next := bpsCompletionResponse(200, terminal(t, "next", `{"name":"lookup","arguments":{"count":1}}`, corrected))
					upstream := &httpUpstreamRecorder{responses: []*http.Response{first, next}}
					gateway := openAIClientToolsTestService(upstream)
					account := excelAccount()
					account.Extra["openai_excel_bps_cache_creation_as_input"] = asInput
					body, err := sjson.SetBytes(schemaGatewayRequest(stream), "model", "gpt-6-astra")
					require.NoError(t, err)
					body, err = sjson.DeleteBytes(body, "service_tier")
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					result, err := gateway.Forward(t.Context(), c, account, body)
					require.NoError(t, err)
					require.Len(t, upstream.requests, 2)
					shown := catalogGatewayTerminal(t, rec.Body.String(), stream)
					require.Equal(t, "lookup", shown.Get("output.0.name").String())
					require.EqualValues(t, 174, shown.Get("usage.total_tokens").Int())
					usage, ok := openAIUsageFromGJSON(shown.Get("usage"))
					require.True(t, ok)
					require.Equal(t, 160, usage.InputTokens)
					require.Equal(t, 14, usage.OutputTokens)
					require.Equal(t, 8, usage.CacheReadInputTokens)
					require.Equal(t, 30, result.Usage.CacheCreationInputTokens)

					billing, logs, debits := managedBPSBillingUsageService()
					require.NoError(t, billing.RecordUsage(t.Context(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 1001}, User: &User{ID: 2001}, Account: account}))
					require.Equal(t, 1, debits.calls)
					log, debit := logs.lastLog, debits.lastCmd
					require.Equal(t, "request-with-schema-repair", log.RequestID)
					require.Equal(t, log.RequestID, debit.RequestID)
					if stream && envelope {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").Str == "response.completed" {
								require.JSONEq(t, shown.Get("usage").Raw, gjson.Get(data, "usage").Raw)
							}
						}
					}
					writes, short, long := 30, 23, 7
					wantDebit := 0.00139178 // 0.001391775 at the eight-place ledger scale
					if asInput {
						writes, short, long = 0, 0, 0
						wantDebit = 0.00130240
					}
					input := 160 - 8 - writes
					require.Equal(t, writes, usage.CacheCreationInputTokens)
					require.Equal(t, short, usage.CacheCreation30mInputTokens)
					require.Equal(t, long, usage.CacheCreation12hInputTokens)
					require.Equal(t, []int{input, 14, 8, writes, short, long}, []int{log.InputTokens, log.OutputTokens, log.CacheReadTokens, log.CacheCreationTokens, log.CacheCreation30mTokens, log.CacheCreation12hTokens})
					cost := float64(input)*5e-6 + 14*30e-6 + 8*0.5e-6 + float64(short)*6.25e-6 + float64(long)*12.5e-6
					require.Equal(t, []int{input, 14, 8, writes}, []int{debit.InputTokens, debit.OutputTokens, debit.CacheReadTokens, debit.CacheCreationTokens})
					require.InDelta(t, cost, log.TotalCost, 1e-12)
					require.NotNil(t, debit.QuotedActualCost)
					require.InDelta(t, cost*1.1, *debit.QuotedActualCost, 1e-12)
					require.Equal(t, wantDebit, debit.BalanceCost)
				})
			}
		}
	}
}
