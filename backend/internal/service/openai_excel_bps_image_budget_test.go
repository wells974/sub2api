//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestExcelBPSBudgetOptionsAreIndependent(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	}
	defaults, err := readExcelBudgetOptions(lookup(nil))
	require.NoError(t, err)
	require.Equal(t, excelBudgetOptions{NativeTotalMiB: 32}, defaults)
	for _, values := range []map[string]string{{"SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB": "0"}, {"SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB": "99999"}, {"SUB2API_BPS_IMAGE_BYTE_COMPACT": "oops"}, {"SUB2API_BPS_TRIM_OLD_TOOL_IMAGES": ""}} {
		_, err := readExcelBudgetOptions(lookup(values))
		require.Error(t, err)
	}
	settings := imageBudgetPolicySettings("auto_compact")
	settings.Mode = "native"
	settings.Limits.MaxTotalMiB = 128
	require.EqualValues(t, 32<<20, excelImageBudget(settings).MaxTotalBytes, "relay settings must not raise native limits")
	settings.BudgetOptions.NativeTotalMiB = 48
	require.EqualValues(t, 48<<20, excelImageBudget(settings).MaxTotalBytes)
	settings.Mode = "relay"
	require.EqualValues(t, 128<<20, excelImageBudget(settings).MaxTotalBytes)
}

func bytePolicyBody(t *testing.T, old, current int) []byte {
	t.Helper()
	_, pixels := nativeGatewayBody(t)
	pixels = append(pixels, make([]byte, (1<<20)-len(pixels))...)
	return bytes.ReplaceAll(imagePolicyRequest(t, old, current), []byte("data:image/png;base64,synthetic"), []byte("data:image/png;base64,"+base64.StdEncoding.EncodeToString(pixels)))
}

func TestExcelBPSByteCompactionPartitions(t *testing.T) {
	settings := imageBudgetPolicySettings("auto_compact")
	settings.Mode = "native"
	settings.BudgetOptions = excelBudgetOptions{NativeTotalMiB: 3, ByteCompact: true}
	svc := &OpenAIGatewayService{cache: newImagePolicyMemoryCache()}
	ctx := context.Background()
	for _, tc := range []struct {
		old, current int
		want         string
	}{{2, 2, ""}, {4, 1, "basispoints_image_history_limit"}, {1, 4, "basispoints_image_batch_too_large"}} {
		state, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), bytePolicyBody(t, tc.old, tc.current), settings, "byte-budget", true)
		if tc.want != "" {
			var policyErr *excelImagePolicyError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, tc.want, policyErr.Code)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, 2, state.split)
		require.Equal(t, 6, state.uploadTotalMiB(settings))
	}
	settings.BudgetOptions.ByteCompact = false
	state, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), bytePolicyBody(t, 2, 2), settings, "disabled", true)
	require.NoError(t, err)
	require.Zero(t, state.split)
	// The 75% trigger must not reject a first turn that still fits the budget.
	settings.BudgetOptions.ByteCompact = true
	var first map[string]any
	require.NoError(t, json.Unmarshal(bytePolicyBody(t, 0, 3), &first))
	first["input"] = first["input"].([]any)[2:]
	firstRaw, _ := json.Marshal(first)
	state, err = svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), firstRaw, settings, "first-turn", true)
	require.NoError(t, err)
	require.Zero(t, state.split)
	settings.BudgetOptions.ByteCompact = true
	_, err = svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), bytePolicyBody(t, 2, 2), settings, "untrusted", false)
	var policyErr *excelImagePolicyError
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, 503, policyErr.Status)
}

func TestExcelBPSBudgetForwardPartitionsAndReplay(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB", "3")
	t.Setenv("SUB2API_BPS_IMAGE_BYTE_COMPACT", "true")
	t.Setenv("SUB2API_BPS_TRIM_OLD_TOOL_IMAGES", "false")
	for _, mode := range []string{"native", "relay"} {
		for _, stream := range []bool{false, true} {
			for _, oversizedWindow := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/oversized=%t", mode, stream, oversizedWindow), func(t *testing.T) {
					svc := openAIClientToolsTestService(nil)
					t.Cleanup(func() {
						if svc.excelBPSImages != nil {
							require.NoError(t, svc.excelBPSImages.Close())
						}
					})
					svc.cache = newImagePolicyMemoryCache()
					repo := &excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageMode: mode, SettingKeyExcelBPSImageBaseURL: "https://images.example", SettingKeyExcelBPSImageLimitPolicy: "auto_compact", SettingKeyExcelBPSImageMaxImageMiB: "3", SettingKeyExcelBPSImageMaxTotalMiB: "3"}}
					svc.settingService = NewSettingService(repo, svc.cfg)
					raw := bytePolicyBody(t, 2, 2)
					raw, _ = sjson.SetBytes(raw, "model", "gpt-6-astra")
					raw, _ = sjson.SetBytes(raw, "stream", stream)
					history, err := basispoints.InspectImageHistory(raw)
					require.NoError(t, err)
					window := []any{map[string]any{"type": "compaction", "encrypted_content": "opaque-byte-state"}}
					if oversizedWindow {
						window = append(window, history.Input[0])
					}
					phase := 0
					svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
						body, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						if req.URL.String() == basispoints.AttachmentsURL {
							return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"openai_file_id":"file-byte-budget"}`))}, nil
						}
						phase++
						output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "continued"}}}}
						inputTokens, outputTokens := 10, 2
						if phase == 1 {
							require.Contains(t, string(body), "compaction_trigger")
							output = window
							inputTokens, outputTokens = 7, 3
						} else {
							require.NotContains(t, string(body), "compaction_trigger")
							require.Contains(t, string(body), "opaque-byte-state")
						}
						count := 0
						for _, item := range gjson.GetBytes(body, "input").Array() {
							for _, part := range item.Get("content").Array() {
								if part.Get("type").String() == "input_image" {
									count++
								}
							}
						}
						require.Equal(t, 2, count, "only the actual partition may be sent")
						wire, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-byte-budget", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}}})
						return bpsCompletionResponse(200, "data: "+string(wire)+"\n\n"), nil
					}}
					forward := func(body []byte) (*OpenAIForwardResult, error) {
						c := imagePolicyContext("/v1/responses")
						c.Request.Header.Set("session_id", "byte-budget-session")
						return svc.forwardExcelBPS(context.Background(), c, excelAccount(), body, time.Now())
					}
					result, err := forward(raw)
					if oversizedWindow {
						require.Error(t, err)
						require.Equal(t, 1, phase)
						require.NotNil(t, result)
						require.Equal(t, 7, result.Usage.InputTokens)
						return
					}
					require.NoError(t, err)
					require.Equal(t, 2, phase)
					require.Equal(t, 17, result.Usage.InputTokens)
					require.Equal(t, 5, result.Usage.OutputTokens)
					// Disable policy and change thresholds; existing checkpoints must still
					// remove stale history and preserve the whole prior new-image tail.
					repo.values[SettingKeyExcelBPSImageLimitPolicy] = "off"
					repo.values[SettingKeyExcelBPSImageWarningRemaining] = "7"
					next := append(append([]any{}, history.Input...), window...)
					next = append(next, map[string]any{"role": "assistant", "content": "continued"}, map[string]any{"role": "user", "content": "next"})
					nextRaw, err := sjson.SetBytes(raw, "input", next)
					require.NoError(t, err)
					result, err = forward(nextRaw)
					require.NoError(t, err)
					require.Equal(t, 3, phase)
					require.Equal(t, 10, result.Usage.InputTokens)
				})
			}
		}
	}
}

type imageTrimConflictCache struct{ *imagePolicyMemoryCache }

func (c *imageTrimConflictCache) SaveBPSImageProgress(context.Context, string, string, string) error {
	return errors.New("CAS conflict")
}

func TestExcelBPSOldScreenshotTrimPersistsBeforeForward(t *testing.T) {
	settings := imageBudgetPolicySettings("auto_compact")
	settings.Mode = "native"
	settings.BudgetOptions = excelBudgetOptions{NativeTotalMiB: 3, ByteCompact: true, TrimOldTools: true}
	var source map[string]any
	require.NoError(t, json.Unmarshal(bytePolicyBody(t, 1, 1), &source))
	parts := source["input"].([]any)[0].(map[string]any)["content"]
	var input []any
	for i := 0; i < 4; i++ {
		input = append(input, map[string]any{"type": "function_call", "name": "shot", "call_id": fmt.Sprint(i), "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": fmt.Sprint(i), "output": parts})
	}
	input = append(input, map[string]any{"role": "assistant", "content": "seen"}, map[string]any{"role": "user", "content": parts})
	source["input"] = input
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	ctx := context.Background()
	cache := newImagePolicyMemoryCache()
	svc := &OpenAIGatewayService{cache: cache}
	p, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "trim", true)
	require.NoError(t, err)
	require.Zero(t, p.split)
	require.Equal(t, 1, p.history.Count)
	require.Len(t, p.checkpoints, 1)
	require.NotEmpty(t, cache.progress[p.scope], "trim must persist even if generation fails")
	require.Equal(t, source["input"].([]any)[9], p.history.Input[9], "new image must survive")
	settings.Policy = "off"
	settings.BudgetOptions.TrimOldTools = false
	settings.WarningRemaining = 6
	p, err = svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "trim", true)
	require.NoError(t, err)
	require.Equal(t, 1, p.history.Count)
	settings.Policy = "auto_compact"
	settings.BudgetOptions.TrimOldTools = true
	svc.cache = &imageTrimConflictCache{newImagePolicyMemoryCache()}
	_, err = svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "trim", true)
	var policyErr *excelImagePolicyError
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, "basispoints_image_checkpoint_unavailable", policyErr.Code)
	// A protected historical user image cannot be trimmed to rescue the request.
	protectedCache := newImagePolicyMemoryCache()
	svc.cache = protectedCache
	userParts := []any{}
	for i := 0; i < 4; i++ {
		userParts = append(userParts, parts.([]any)...)
	}
	source["input"] = append([]any{map[string]any{"role": "user", "content": userParts}}, input...)
	protectedRaw, marshalErr := json.Marshal(source)
	require.NoError(t, marshalErr)
	_, err = svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), protectedRaw, settings, "protected", true)
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, "basispoints_image_history_limit", policyErr.Code)
	require.Empty(t, protectedCache.progress, "unusable trimming must not be persisted")
}

func TestExcelBPSImageStateMigratesLegacyKey(t *testing.T) {
	ctx := context.Background()
	settings := imageBudgetPolicySettings("auto_compact")
	cache := newImagePolicyMemoryCache()
	legacy := "scope/policy:auto_compact/limit:20/warn:8/reserve:3"
	cache.progress[legacy] = `{"Progress":{"Count":1,"Digest":"test"}}`
	scope, value, err := loadImagePolicyState(ctx, cache, "scope", settings)
	require.NoError(t, err)
	require.Equal(t, cache.progress[legacy], value)
	require.Equal(t, "scope/image-history:v2", scope)
	settings.Policy = "off"
	settings.Limits.MaxImages = 40
	settings.WarningRemaining = 9
	next, nextValue, err := loadImagePolicyState(ctx, cache, "scope", settings)
	require.NoError(t, err)
	require.Equal(t, scope, next)
	require.Equal(t, value, nextValue)
}

func imageBudgetPolicySettings(policy string) excelImageBudgetSettings {
	return excelImageBudgetSettings{ExcelBPSImageRelaySettings: imagePolicySettings(policy)}
}
