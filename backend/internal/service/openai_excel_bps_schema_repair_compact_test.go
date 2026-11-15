//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestExcelBPSSchemaRepairCompactionUsage(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS", "30")
	for _, scenario := range []string{"success", "failure", "cancel", "visible_window", "dispatch_failure"} {
		t.Run(scenario, func(t *testing.T) {
			svc := openAIClientToolsTestService(nil)
			svc.cache = newImagePolicyMemoryCache()
			repo := &excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageMode: "native", SettingKeyExcelBPSImageLimitPolicy: "auto_compact"}}
			svc.settingService = NewSettingService(repo, svc.cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, pixels := nativeGatewayBody(t)
			body := bytes.ReplaceAll(imagePolicyRequest(t, 19, 2), []byte("data:image/png;base64,synthetic"), []byte("data:image/png;base64,"+base64.StdEncoding.EncodeToString(pixels)))
			body, err := sjson.SetBytes(body, "model", "gpt-5.6-sol")
			require.NoError(t, err)
			body, err = sjson.SetBytes(body, "stream", true)
			require.NoError(t, err)
			body, err = sjson.SetRawBytes(body, "tools", []byte(gjson.GetBytes(schemaGatewayRequest(true), "tools").Raw))
			require.NoError(t, err)
			phase := 0
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				if req.URL.String() == basispoints.AttachmentsURL {
					return bpsCompletionResponse(200, `{"openai_file_id":"file-schema-compact"}`), nil
				}
				phase++
				if phase == 1 {
					output := []any{map[string]any{"type": "compaction", "encrypted_content": "opaque-schema-compact"}}
					if scenario == "visible_window" {
						output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "retained history"}}})
					}
					wire, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "compact", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 7, "output_tokens": 3, "input_tokens_details": map[string]any{"cache_write_tokens": 3, "cache_write_tokens_details": map[string]int{"ttl_30m_tokens": 1, "ttl_12h_tokens": 2}}}}})
					require.NoError(t, err)
					return bpsCompletionResponse(200, "data: "+string(wire)+"\n\n"), nil
				}
				if phase == 2 && scenario == "dispatch_failure" {
					return bpsCompletionResponse(500, `{"error":{"message":"test dispatch failure"}}`), nil
				}
				if phase == 3 && scenario == "cancel" {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &schemaCancellationBody{prefix: strings.NewReader("data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":1,\"input_tokens_details\":{\"cache_write_tokens\":1,\"cache_write_tokens_details\":{\"ttl_30m_tokens\":0,\"ttl_12h_tokens\":1}}}}}\n\n"), closed: make(chan struct{}), cancel: cancel}}, nil
				}
				args := `{"name":"lookup","arguments":{}}`
				id := "initial"
				if phase == 3 {
					id = "corrected"
					if scenario == "success" {
						args = `{"name":"lookup","arguments":{"count":1}}`
					}
				}
				if phase == 3 {
					request, _ := io.ReadAll(req.Body)
					require.Contains(t, string(request), "invalid_client_tool_arguments")
				}
				wire := excelBPSRepairWire(t, id, "Run", args)
				lines := strings.Split(wire, "\n")
				for i, line := range lines {
					if strings.HasPrefix(line, "data: ") {
						payload, err := sjson.SetRaw(strings.TrimPrefix(line, "data: "), "response.usage.input_tokens_details", `{"cache_write_tokens":2,"cache_write_tokens_details":{"ttl_30m_tokens":1,"ttl_12h_tokens":1}}`)
						require.NoError(t, err)
						lines[i] = "data: " + payload
					}
				}
				return bpsCompletionResponse(200, strings.Join(lines, "\n")), nil
			}}
			c := imagePolicyContext("/v1/responses")
			c.Request = c.Request.WithContext(ctx)
			c.Request.Header.Set("session_id", "schema-with-compact")
			result, err := svc.forwardExcelBPS(ctx, c, excelAccount(), body, time.Now())
			if scenario == "success" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotNil(t, result)
			wantCreation, want30m, want12h := 7, 3, 4
			switch scenario {
			case "dispatch_failure":
				wantCreation, want30m, want12h = 3, 1, 2
				require.Equal(t, 2, phase)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
			case "visible_window":
				wantCreation, want30m, want12h = 5, 2, 3
				require.Equal(t, 2, phase)
				require.Equal(t, 17, result.Usage.InputTokens)
				require.Equal(t, 5, result.Usage.OutputTokens)
			case "cancel":
				wantCreation, want30m, want12h = 6, 2, 4
				require.Equal(t, 3, phase)
				require.Equal(t, 21, result.Usage.InputTokens)
				require.Equal(t, 6, result.Usage.OutputTokens)
			default:
				require.Equal(t, 3, phase)
				require.Equal(t, 27, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.OutputTokens)
			}
			require.Equal(t, wantCreation, result.Usage.CacheCreationInputTokens)
			require.Equal(t, want30m, result.Usage.CacheCreation30mInputTokens)
			require.Equal(t, want12h, result.Usage.CacheCreation12hInputTokens)
		})
	}
}
