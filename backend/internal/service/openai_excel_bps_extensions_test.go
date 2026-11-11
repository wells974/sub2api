//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestExcelBPSExtensionsDisabledMatchLegacyImagePolicy(t *testing.T) {
	for _, policy := range []string{"off", "warn", "auto_compact"} {
		for _, images := range [][2]int{{0, 0}, {0, 21}, {19, 2}, {21, 1}, {3, 1}} {
			for _, cacheFailure := range []bool{false, true} {
				for _, trusted := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%v/cache_error=%t/trusted=%t", policy, images, cacheFailure, trusted), func(t *testing.T) {
						legacyCache, currentCache := newImagePolicyMemoryCache(), newImagePolicyMemoryCache()
						if cacheFailure {
							legacyCache.err, currentCache.err = errors.New("cache unavailable"), errors.New("cache unavailable")
						}
						legacyService := &OpenAIGatewayService{cache: legacyCache}
						currentService := &OpenAIGatewayService{cache: currentCache}
						settings := imageBudgetPolicySettings(policy)
						settings.Mode = "native"
						settings.BudgetOptions.NativeTotalMiB = 32
						body := imagePolicyRequest(t, images[0], images[1])
						ctx := context.Background()
						want, wantErr := legacyService.prepareExcelImagePolicy(ctx, imagePolicyContext("/v1/responses"), body, settings.ExcelBPSImageRelaySettings, "disabled", trusted)
						got, gotErr := currentService.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), body, settings, "disabled", trusted)
						require.Equal(t, fmt.Sprint(wantErr), fmt.Sprint(gotErr), "preserve original errors, including precedence and wording")
						if wantErr != nil {
							return
						}
						require.False(t, got.extended)
						require.Equal(t, want.split, got.split)
						require.Equal(t, want.compact, got.compact)
						require.Equal(t, want.maxImages, got.maxImages)
						require.Equal(t, want.scope, got.scope)
						require.Equal(t, want.reconciledBody, got.reconciledBody)
						require.Equal(t, 32, got.uploadTotalMiB(settings), "legacy count compaction must not double the aggregate byte budget")
						want.finish(ctx, false)
						got.finish(ctx, false)
						require.Equal(t, legacyCache.progress, currentCache.progress, "disabled mode must not migrate or create v2 state")
						require.Equal(t, legacyCache.warning, currentCache.warning)
					})
				}
			}
		}
	}
}

func TestExcelBPSExtensionsTrimCompactCatalogSchemaAndDisable(t *testing.T) {
	t.Setenv("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB", "3")
	t.Setenv("SUB2API_BPS_IMAGE_BYTE_COMPACT", "true")
	t.Setenv("SUB2API_BPS_TRIM_OLD_TOOL_IMAGES", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "30")
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	svc := openAIClientToolsTestService(nil)
	cache := newImagePolicyMemoryCache()
	svc.cache = cache
	repo := &excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageMode: "native", SettingKeyExcelBPSImageLimitPolicy: "auto_compact"}}
	svc.settingService = NewSettingService(repo, svc.cfg)
	var images map[string]any
	require.NoError(t, json.Unmarshal(bytePolicyBody(t, 2, 2), &images))
	base := images["input"].([]any)
	parts := base[0].(map[string]any)["content"].([]any)
	input := []any{base[0]}
	for i := 0; i < 5; i++ {
		input = append(input, map[string]any{"type": "function_call", "name": "optional_01", "call_id": fmt.Sprintf("shot_%d", i), "arguments": `{"count":1}`}, map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("shot_%d", i), "output": []any{parts[0]}})
	}
	input = append(input, base[1], base[2])
	source := onDemandGatewaySource(true)
	source["tools"].([]any)[1].(map[string]any)["defer_loading"] = false
	source["input"] = input
	body, err := json.Marshal(source)
	require.NoError(t, err)
	window := map[string]any{"type": "compaction", "encrypted_content": "extensions-compacted"}
	phase := 0
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		if req.URL.String() == basispoints.AttachmentsURL {
			return bpsCompletionResponse(200, `{"openai_file_id":"file-extensions"}`), nil
		}
		phase++
		wire, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		if phase == 1 {
			require.Contains(t, string(wire), "compaction_trigger")
			require.NotContains(t, string(wire), "__sub2api_tool_catalog")
			payload, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "compact", "status": "completed", "output": []any{window}, "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}}})
			require.NoError(t, err)
			return bpsCompletionResponse(200, "data: "+string(payload)+"\n\n"), nil
		}
		require.Contains(t, string(wire), "extensions-compacted")
		require.NotContains(t, string(wire), "shot_", "old image history must stay removed after checkpoint replay")
		if phase < 4 {
			require.Contains(t, string(wire), "__sub2api_tool_catalog")
		} else {
			require.NotContains(t, string(wire), "__sub2api_tool_catalog")
		}
		if phase == 2 {
			return bpsCompletionResponse(200, excelBPSRepairWire(t, "bad", "Run", `{"name":"optional_00","arguments":{"count":"wrong"}}`)), nil
		}
		if phase == 3 {
			require.Contains(t, string(wire), "invalid_client_tool_arguments")
		}
		return bpsCompletionResponse(200, excelBPSRepairWire(t, fmt.Sprint(phase), "Run", `{"name":"optional_00","arguments":{"count":1}}`)), nil
	}}
	forward := func(body []byte) (*OpenAIForwardResult, error) {
		c := imagePolicyContext("/v1/responses")
		c.Request = c.Request.WithContext(ctx)
		c.Request.Header.Set("session_id", "all-extensions")
		return svc.forwardExcelBPS(ctx, c, excelAccount(), body, time.Now())
	}
	result, err := forward(body)
	require.NoError(t, err)
	require.Equal(t, 3, phase)
	require.Equal(t, 27, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Len(t, logs.FilterMessage("bps_image_history_trim").All(), 1)
	require.Len(t, logs.FilterMessage("bps_schema_repair").All(), 1)
	require.Len(t, logs.FilterMessage("bps_tool_catalog").All(), 1)
	entries := logs.FilterMessage("bps_request_diagnostics").All()
	require.Len(t, entries, 1)
	stats, err := json.Marshal(entries[0].ContextMap())
	require.NoError(t, err)
	require.Equal(t, int64(5<<20), gjson.GetBytes(stats, "input_images.tool_output_inline_bytes").Int())
	require.Equal(t, int64(4<<20), gjson.GetBytes(stats, "input_images.message_inline_bytes").Int())
	require.NotContains(t, string(stats), "PRIVATE_POLICY")
	// Turning everything off stops new transformations while the already saved
	// trim/compact chain still reconciles the client's original full history.
	t.Setenv("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB", "32")
	t.Setenv("SUB2API_BPS_IMAGE_BYTE_COMPACT", "false")
	t.Setenv("SUB2API_BPS_TRIM_OLD_TOOL_IMAGES", "false")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "false")
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "false")
	repo.values[SettingKeyExcelBPSImageLimitPolicy] = "off"
	source["input"] = append(append([]any{}, input...), window, map[string]any{"role": "user", "content": "next"})
	next, err := json.Marshal(source)
	require.NoError(t, err)
	result, err = forward(next)
	require.NoError(t, err)
	require.Equal(t, 4, phase)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Len(t, logs.FilterMessage("bps_image_history_trim").All(), 1)
	require.Len(t, logs.FilterMessage("bps_schema_repair").All(), 1)
	require.Len(t, logs.FilterMessage("bps_tool_catalog").All(), 1)
	// Original caller-owned request bytes are untouched.
	require.True(t, bytes.Contains(body, []byte("shot_0")))
	require.True(t, strings.Contains(string(body), "data:image/png;base64,"))
}
