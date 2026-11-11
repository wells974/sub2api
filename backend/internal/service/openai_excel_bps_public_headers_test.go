package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSPublicImageHeadersKeepInternalEvidence(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(excelBPSErrorPresentationEnv, mode)
			resp := openAIImagesJSONResponse()
			resp.Header = http.Header{
				"Content-Type": {"application/json"}, "Retry-After": {"7"}, "X-Request-Id": {"req-opaque"},
				"X-Basispoints-Route": {"private"}, "X-Codex2api-Upstream": {"private"},
				"Location": {"https://bps.openai.com/private"}, "X-Bps-Debug": {"private"},
				"X-Safe-Extension": {"preserved"},
			}
			original := resp.Header.Clone()
			upstream := &httpUpstreamRecorder{resp: resp}
			svc := newOpenAIImagesTestService(upstream)
			svc.responseHeaderFilter = responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{
				Enabled: true, AdditionalAllowed: []string{"X-Basispoints-Route", "X-Codex2api-Upstream", "X-Bps-Debug", "X-Safe-Extension"},
			})
			body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024","n":1}`)
			c, rec := newOpenAIImagesTestContext(t, body)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, excelBPSImagesAccount(), body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, 200, rec.Code)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, original, resp.Header)
			require.Equal(t, original, result.ResponseHeaders)
			require.Equal(t, "req-opaque", result.RequestID)
			for _, key := range []string{"X-Basispoints-Route", "X-Codex2api-Upstream", "X-Bps-Debug", "Location"} {
				if mode == "legacy" {
					require.Equal(t, original.Get(key), rec.Header().Get(key))
				} else {
					require.Empty(t, rec.Header().Get(key))
				}
			}
			for _, key := range []string{"Retry-After", "X-Request-Id", "X-Safe-Extension"} {
				require.Equal(t, original.Get(key), rec.Header().Get(key))
			}
		})
	}
}
