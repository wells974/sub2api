//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPublicUserErrorEndpoints(t *testing.T) {
	for _, mode := range []string{"", "legacy"} {
		for _, role := range []string{service.RoleUser, service.RoleObserver} {
			t.Run(mode+role, func(t *testing.T) {
				t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
				owner := int64(42)
				repo := &observerErrorRepo{detail: service.OpsErrorLogDetail{OpsErrorLog: service.OpsErrorLog{ID: 7, UserID: &owner, StatusCode: 524, Message: "Excel BPS rejected this request", UpstreamEndpoint: "/basispoints/api/responses", AccountName: "PRIVATE_ACCOUNT"}, ErrorBody: `{"error":{"code":"basispoints_upstream_error","message":"PRIVATE"}}`}}
				settings := service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{service.SettingKeyAllowUserViewErrorRequests: "true"}}, nil)
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				h := NewUsageHandler(nil, nil, ops, settings)
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
					c.Set(string(middleware.ContextKeyUserRole), role)
				})
				router.GET("/usage/errors", h.ListErrors)
				router.GET("/usage/errors/:id", h.GetErrorDetail)
				for _, path := range []string{"/usage/errors?user_id=999", "/usage/errors/7"} {
					w := usageGET(router, path)
					require.Equal(t, 200, w.Code)
					if mode == "legacy" || role == service.RoleObserver {
						require.Contains(t, w.Body.String(), "Excel BPS")
					} else {
						for _, private := range []string{"Excel BPS", "basispoints", "PRIVATE"} {
							require.NotContains(t, w.Body.String(), private)
						}
					}
				}
				require.Equal(t, int64(42), *repo.filter.UserID)
				owner = 999
				require.Equal(t, 404, usageGET(router, "/usage/errors/7").Code)
				require.Contains(t, repo.detail.ErrorBody, "PRIVATE")
			})
		}
	}
}

func TestExcelBPSPublicSettingsAPIAndBootstrap(t *testing.T) {
	for _, mode := range []string{"", "legacy", "invalid"} {
		for _, enabled := range []string{"true", "false"} {
			t.Run(mode+enabled, func(t *testing.T) {
				t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", mode)
				svc := service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{service.SettingKeyExcelBPSEnabled: enabled}}, &config.Config{})
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				ctx.Request = httptest.NewRequest("GET", "/api/v1/settings/public", nil)
				NewSettingHandler(svc, "test").GetPublicSettings(ctx)
				require.Equal(t, 200, rec.Code)
				bootstrap, err := svc.GetPublicSettingsForInjection(context.Background())
				require.NoError(t, err)
				encoded, err := json.Marshal(bootstrap)
				require.NoError(t, err)
				for _, raw := range []string{gjson.Get(rec.Body.String(), "data").Raw, string(encoded)} {
					key := "tool_bridge_enabled"
					if mode == "legacy" {
						key = "excel_bps_enabled"
					} else {
						require.NotContains(t, raw, "excel_bps")
					}
					require.True(t, gjson.Get(raw, key).Exists())
					require.Equal(t, enabled == "true", gjson.Get(raw, key).Bool())
				}
			})
		}
	}
}
