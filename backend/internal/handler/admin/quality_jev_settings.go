package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetQualityJev(c *gin.Context) {
	data, err := h.settingService.GetQualityJevSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

func (h *SettingHandler) SaveQualityJev(c *gin.Context) {
	var data service.QualityJevSettingsView
	if c.ShouldBindJSON(&data) != nil {
		response.BadRequest(c, "Invalid Jev settings")
		return
	}
	if err := h.settingService.SaveQualityJevSettings(c.Request.Context(), data); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}
