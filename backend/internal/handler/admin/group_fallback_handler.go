package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountOpsHandler) GetGroupFallback(c *gin.Context) {
	state, err := h.svc.GroupFallbackState(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *AccountOpsHandler) GroupFallbackOptions(c *gin.Context) {
	options, err := h.svc.GroupFallbackOptions(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, options)
}
func (h *AccountOpsHandler) SaveGroupFallback(c *gin.Context) {
	var config service.GroupFallbackConfig
	if c.ShouldBindJSON(&config) != nil {
		response.BadRequest(c, "Invalid group fallback settings")
		return
	}
	saved, err := h.svc.SaveGroupFallback(c.Request.Context(), config)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, saved)
}
