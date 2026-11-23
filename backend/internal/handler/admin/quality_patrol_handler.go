package admin

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountOpsHandler) GetQualityPatrol(c *gin.Context) {
	v, err := h.svc.QualityPatrolSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Could not load quality notification settings")
		return
	}
	response.Success(c, v)
}

func (h *AccountOpsHandler) SaveQualityPatrol(c *gin.Context) {
	var v service.QualityPatrolConfig
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid quality notification settings")
		return
	}
	if err := h.svc.SaveQualityPatrol(c.Request.Context(), v); err != nil {
		if errors.Is(err, service.ErrAccountOpsConfigValidation) {
			response.BadRequest(c, err.Error())
		} else {
			response.Error(c, http.StatusServiceUnavailable, "Could not save quality notification settings")
		}
		return
	}
	response.Success(c, gin.H{"ok": true})
}
