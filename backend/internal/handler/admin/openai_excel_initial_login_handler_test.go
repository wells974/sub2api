package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelInitialWorkerEndpointsRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(openAIOAuthReauthWorkerTokenEnv, strings.Repeat("w", 32))
	h := &OpenAIOAuthReauthHandler{}
	router := gin.New()
	router.POST("/claim", h.ClaimExcelInitialLogin)
	router.POST("/:task_id/initial-excel/:action", h.ExcelInitialLoginWorkerResult)
	for _, path := range []string{"/claim", "/1/initial-excel/active", "/1/initial-excel/result", "/1/initial-excel/fail"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"worker_id":"worker","credentials":{"password":"PRIVATE_VALUE"}}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.NotContains(t, rec.Body.String(), "PRIVATE_VALUE")
		})
	}
}

func TestExcelInitialLoginRejectsMalformedInputWithoutEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/start", (&OpenAIReloginHandler{}).StartExcelInitialLogin)
	for _, body := range []string{`{"password":"PRIVATE_VALUE","proxy_id":"wrong"}`, `{"password":"PRIVATE_VALUE` + strings.Repeat("x", 17000) + `"}`} {
		req := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.NotContains(t, rec.Body.String(), "PRIVATE_VALUE")
	}
}
