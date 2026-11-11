//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

func TestExcelBPSOnDemandCatalogAfterImageCompaction(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	svc := openAIClientToolsTestService(nil)
	svc.cache = newImagePolicyMemoryCache()
	repo := &excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageMode: "native", SettingKeyExcelBPSImageLimitPolicy: "auto_compact"}}
	svc.settingService = NewSettingService(repo, svc.cfg)
	_, pixels := nativeGatewayBody(t)
	body := bytes.ReplaceAll(imagePolicyRequest(t, 19, 2), []byte("data:image/png;base64,synthetic"), []byte("data:image/png;base64,"+base64.StdEncoding.EncodeToString(pixels)))
	body, err := sjson.SetBytes(body, "model", "gpt-5.6-sol")
	require.NoError(t, err)
	body, err = sjson.SetBytes(body, "stream", true)
	require.NoError(t, err)
	toolJSON, err := json.Marshal(onDemandGatewaySource(true)["tools"])
	require.NoError(t, err)
	body, err = sjson.SetRawBytes(body, "tools", toolJSON)
	require.NoError(t, err)
	phase := 0
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		if req.URL.String() == basispoints.AttachmentsURL {
			return bpsCompletionResponse(200, `{"openai_file_id":"file-catalog-compact"}`), nil
		}
		phase++
		request, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		if phase == 1 {
			require.Contains(t, string(request), "Do not call Excel, Office, workbook or connector tools.", "compact keeps its original no-tool preparation")
			require.NotContains(t, string(request), "__sub2api_tool_catalog")
			return bpsCompletionResponse(200, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"compact\",\"status\":\"completed\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque-catalog-compact\"}],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"), nil
		}
		require.Equal(t, 2, phase, "metadata disclosure does not submit another generation")
		require.Contains(t, string(request), "__sub2api_tool_catalog")
		require.NotContains(t, string(request), "PRIVATE_POLICY")
		return bpsCompletionResponse(200, excelBPSRepairWire(t, "docs", "Run", `{"name":"optional_00","arguments":{"count":1}}`)), nil
	}}
	c := imagePolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "catalog-with-compact")
	result, err := svc.forwardExcelBPS(context.Background(), c, excelAccount(), body, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, phase)
	require.Equal(t, 17, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}
