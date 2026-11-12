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

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestExcelBPSMessageImageOptions(t *testing.T) {
	for _, tc := range []struct {
		raw                   string
		exists, want, invalid bool
	}{
		{"", false, false, false}, {"false", true, false, false}, {"true", true, true, false}, {"", true, false, true}, {"oops", true, false, true},
	} {
		enabled, err := readExcelMessageImageOption(func(string) (string, bool) { return tc.raw, tc.exists })
		require.Equal(t, tc.want, enabled)
		require.Equal(t, tc.invalid, err != nil)
	}
}

func TestExcelBPSMessageImageForwardAndDisabledReplay(t *testing.T) {
	t.Setenv("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB", "3")
	t.Setenv("SUB2API_BPS_IMAGE_BYTE_COMPACT", "true")
	t.Setenv("SUB2API_BPS_TRIM_OLD_TOOL_IMAGES", "true")
	t.Setenv("SUB2API_BPS_MESSAGE_IMAGE_COMPACT", "false")
	t.Setenv("DATA_DIR", t.TempDir())
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			raw := bytePolicyBody(t, 4, 1)
			raw, err := sjson.SetBytes(raw, "stream", stream)
			require.NoError(t, err)
			original := bytes.Clone(raw)
			svc := openAIClientToolsTestService(nil)
			svc.cache = newImagePolicyMemoryCache()
			repo := &excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageMode: "native", SettingKeyExcelBPSImageLimitPolicy: "auto_compact"}}
			svc.settingService = NewSettingService(repo, svc.cfg)
			uploads, generations := 0, 0
			_, pixels := nativeGatewayBody(t)
			originalImage := append(pixels, make([]byte, (1<<20)-len(pixels))...)
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				if req.URL.String() == basispoints.AttachmentsURL {
					uploads++
					reader, e := req.MultipartReader()
					require.NoError(t, e)
					part, e := reader.NextPart()
					require.NoError(t, e)
					data, e := io.ReadAll(part)
					require.NoError(t, e)
					id := "file-history-small"
					if len(data) == len(originalImage) {
						require.Equal(t, originalImage, data, "current image bytes must be untouched")
						id = "file-current-original"
					} else {
						require.Less(t, len(data), len(originalImage))
					}
					return bpsCompletionResponse(200, `{"openai_file_id":"`+id+`"}`), nil
				}
				generations++
				body, e := io.ReadAll(req.Body)
				require.NoError(t, e)
				require.NotContains(t, string(body), "compaction_trigger")
				require.Contains(t, string(body), "file-history-small")
				require.Contains(t, string(body), "file-current-original")
				return bpsCompletionResponse(200, `data: {"type":"response.completed","response":{"id":"resp-images","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]}],"usage":{"input_tokens":10,"output_tokens":2}}}`+"\n\n"), nil
			}}
			forward := func(raw []byte) (*OpenAIForwardResult, error) {
				c := imagePolicyContext("/v1/responses")
				c.Request.Header.Set("session_id", "ordinary-images-session")
				return svc.forwardExcelBPS(context.Background(), c, excelAccount(), raw, time.Now())
			}
			_, err = forward(raw)
			require.ErrorContains(t, err, "basispoints_image_history_limit")
			require.Zero(t, uploads)
			require.Zero(t, generations)
			t.Setenv("SUB2API_BPS_MESSAGE_IMAGE_COMPACT", "true")
			result, err := forward(raw)
			require.NoError(t, err)
			require.Equal(t, 2, uploads)
			require.Equal(t, 1, generations)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, original, raw)
			// Turning every new policy off still replays already accepted receipts.
			t.Setenv("SUB2API_BPS_MESSAGE_IMAGE_COMPACT", "false")
			t.Setenv("SUB2API_BPS_IMAGE_BYTE_COMPACT", "false")
			t.Setenv("SUB2API_BPS_TRIM_OLD_TOOL_IMAGES", "false")
			t.Setenv("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB", "32")
			repo.values[SettingKeyExcelBPSImageLimitPolicy] = "off"
			result, err = forward(raw)
			require.NoError(t, err)
			require.Equal(t, 2, uploads, "cached compressed and current attachments are reused")
			require.Equal(t, 2, generations)
			require.Equal(t, 10, result.Usage.InputTokens)
		})
	}
}

type messageImageRejectSave struct{ *imagePolicyMemoryCache }

func (c *messageImageRejectSave) SaveBPSImageProgress(context.Context, string, string, string) error {
	return errors.New("CAS conflict")
}

func TestExcelBPSMessageImageCheckpointFailureAndMixedTrim(t *testing.T) {
	ctx := context.Background()
	settings := imageBudgetPolicySettings("auto_compact")
	settings.Mode = "native"
	settings.BudgetOptions = excelBudgetOptions{NativeTotalMiB: 3, ByteCompact: true, TrimOldTools: true, MessageImages: true}
	raw := bytePolicyBody(t, 4, 1)
	svc := &OpenAIGatewayService{cache: &messageImageRejectSave{newImagePolicyMemoryCache()}}
	_, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "cas-fail", true)
	var failure *excelImagePolicyError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "basispoints_image_checkpoint_unavailable", failure.Code)
	// Add oversized old tool results. Ordinary image compression must precede
	// existing tool trimming, and both receipts must replay after disabling it.
	var source map[string]any
	require.NoError(t, json.Unmarshal(raw, &source))
	input := source["input"].([]any)
	url := gjson.GetBytes(raw, "input.0.content.0.image_url").String()
	mixed := append([]any{}, input[:2]...)
	for i := 0; i < 4; i++ {
		id := fmt.Sprint(i)
		mixed = append(mixed, map[string]any{"type": "function_call", "call_id": id, "name": "shot", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": id, "output": []any{map[string]any{"type": "input_image", "image_url": url}}})
	}
	mixed = append(mixed, input[2:]...)
	source["input"] = mixed
	raw, err = json.Marshal(source)
	require.NoError(t, err)
	svc.cache = newImagePolicyMemoryCache()
	state, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "mixed-images", true)
	require.NoError(t, err)
	require.Len(t, state.checkpoints, 2)
	require.NotEmpty(t, state.checkpoints[0].MessageImages)
	require.NotEmpty(t, state.checkpoints[1].Trim)
	require.Zero(t, state.split)
	body, err := state.reconcileBody(raw, settings)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(body), "Earlier tool screenshot omitted"))
	settings.BudgetOptions.MessageImages = false
	settings.Policy = "off"
	replayed, err := svc.prepareExcelImagePolicyWithBudget(ctx, imagePolicyContext("/v1/responses"), raw, settings, "mixed-images", true)
	require.NoError(t, err)
	body2, err := replayed.reconcileBody(raw, settings)
	require.NoError(t, err)
	require.Equal(t, body, body2)
}
