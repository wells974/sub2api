package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSPublicOpsUsesOriginalError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		var legacy *service.OpsInsertErrorLogInput
		for _, mode := range []string{"legacy", ""} {
			t.Run(mode+map[bool]string{true: "/sse", false: "/json"}[stream], func(t *testing.T) {
				t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
				setupOpsErrorLogTestQueue(t, 2)
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.POST("/v1/responses", func(c *gin.Context) {
					c.Set(opsAccountIDKey, int64(300))
					service.SetOpsUpstreamError(c, 200, "original upstream evidence", "original detail")
					original := gin.H{"type": "server_error", "code": "basispoints_stream_incomplete", "message": "Excel BPS stream ended before completion", "param": "original-path"}
					body := service.PresentExcelBPSError(c, 502, original)
					if stream {
						c.Header("Content-Type", "text/event-stream")
						payload, err := json.Marshal(gin.H{"type": "response.failed", "response": gin.H{"status": "failed", "error": body}})
						require.NoError(t, err)
						_, err = c.Writer.WriteString("event: response.failed\ndata: " + string(payload) + "\n\n")
						require.NoError(t, err)
					} else {
						c.JSON(502, gin.H{"error": body})
					}
				})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", nil))
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
				require.Len(t, repo.entries, 1)
				entry := repo.entries[0]
				require.Equal(t, "Excel BPS stream ended before completion", entry.ErrorMessage)
				require.Contains(t, entry.ErrorBody, "basispoints_stream_incomplete")
				require.Contains(t, entry.ErrorBody, "original-path")
				if mode == "legacy" {
					legacy = entry
				} else {
					require.NotContains(t, rec.Body.String(), "basispoints")
					require.NotContains(t, rec.Body.String(), "Excel")
					require.Equal(t, legacy.ErrorType, entry.ErrorType)
					require.Equal(t, legacy.ErrorPhase, entry.ErrorPhase)
					require.Equal(t, legacy.ErrorOwner, entry.ErrorOwner)
					require.Equal(t, legacy.ErrorSource, entry.ErrorSource)
					require.Equal(t, legacy.StatusCode, entry.StatusCode)
					require.Equal(t, legacy.UpstreamStatusCode, entry.UpstreamStatusCode)
					require.Equal(t, legacy.IsBusinessLimited, entry.IsBusinessLimited)
				}
			})
		}
	}
}
