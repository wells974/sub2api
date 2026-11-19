package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelInitialLoginRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OPENAI_REAUTH_WORKER_TOKEN", strings.Repeat("w", 32))
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{OpenAIOAuthReauth: adminhandler.NewOpenAIOAuthReauthHandler(nil)}}
	RegisterAdminRoutes(router.Group("/api/v1"), handlers,
		middleware.AdminAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }),
		middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }), nil, nil)
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/admin/account-ops/relogin/initial-excel-login"},
		{"GET", "/admin/account-ops/relogin/initial-excel-login/1"},
		{"DELETE", "/admin/account-ops/relogin/initial-excel-login/1"},
		{"POST", "/admin/account-ops/initial-excel-login"},
		{"GET", "/admin/account-ops/initial-excel-login/1"},
		{"DELETE", "/admin/account-ops/initial-excel-login/1"},
		{"POST", "/internal/openai-reauth/initial-excel/claim"},
		{"POST", "/internal/openai-reauth/1/initial-excel/result"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(endpoint.method, "/api/v1"+endpoint.path, nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code, endpoint.path)
	}
}
