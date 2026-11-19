package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIReloginHandler) StartExcelInitialLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		service.OpenAIReloginCredentials
		ProxyID *int64 `json:"proxy_id"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid BPS login request")
		return
	}
	job, err := h.svc.StartExcelInitialLogin(c.Request.Context(), input.OpenAIReloginCredentials, input.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, job)
}

func excelInitialID(c *gin.Context) (int64, bool) {
	raw := c.Param("id")
	if raw == "" {
		raw = c.Param("task_id")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid login task ID")
		return 0, false
	}
	return id, true
}

func (h *OpenAIReloginHandler) GetExcelInitialLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := excelInitialID(c)
	if !ok {
		return
	}
	job, err := h.reauth.GetExcelInitialLogin(id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}
func (h *OpenAIReloginHandler) DeleteExcelInitialLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := excelInitialID(c)
	if !ok {
		return
	}
	h.reauth.DeleteExcelInitialLogin(id)
	response.Success(c, gin.H{"deleted": true})
}

func (h *OpenAIOAuthReauthHandler) ClaimExcelInitialLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		WorkerID string `json:"worker_id" binding:"required,max=128"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid worker request")
		return
	}
	claim, err := h.service.ClaimExcelInitialLogin(input.WorkerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, claim)
}

// All task callbacks use a separate namespace from persisted account re-login.
func (h *OpenAIOAuthReauthHandler) ExcelInitialLoginWorkerResult(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.requireWorker(c) {
		return
	}
	id, ok := excelInitialID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var input struct {
		WorkerID    string         `json:"worker_id" binding:"required,max=128"`
		Credentials map[string]any `json:"credentials"`
		ErrorCode   string         `json:"error_code"`
		ErrorStage  string         `json:"error_stage"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid worker request")
		return
	}
	action := c.Param("action")
	if action == "active" {
		response.Success(c, gin.H{"active": h.service.ExcelInitialLoginActive(id, input.WorkerID)})
		return
	}
	if action != "result" && action != "fail" {
		response.NotFound(c, "Unknown login action")
		return
	}
	if action == "result" && input.Credentials == nil {
		response.BadRequest(c, "Credentials are required")
		return
	}
	if action == "fail" {
		if err := h.service.FailExcelInitialLogin(id, input.WorkerID, input.ErrorCode, input.ErrorStage); err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"status": "failed"})
		return
	}
	if err := h.service.FinishExcelInitialLogin(id, input.WorkerID, input.Credentials); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"status": "succeeded"})
}
