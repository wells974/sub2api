package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// The originating failover error stays intact for account-switch policy and
// Ops. Do not tag the whole request: a later attempt may use another route.
func (h *OpenAIGatewayHandler) handleExcelBPSClientError(c *gin.Context, status int, errorType, code, message string, streamStarted bool) {
	h.handleStreamingAwareErrorWithCode(c, status, errorType, code, message, streamStarted, false,
		func(errorType, code, message string) (string, string, string) {
			body := service.PresentExcelBPSError(c, status, gin.H{"type": errorType, "code": code, "message": message})
			errorType, _ = body["type"].(string)
			code, _ = body["code"].(string)
			message, _ = body["message"].(string)
			return errorType, code, message
		})
}
