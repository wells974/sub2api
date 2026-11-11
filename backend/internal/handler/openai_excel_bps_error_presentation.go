package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// The originating failover error stays intact for account-switch policy and
// Ops. Do not tag the whole request: a later attempt may use another route.
func (h *OpenAIGatewayHandler) handleExcelBPSClientError(c *gin.Context, status int, errorType, code, message string, streamStarted bool) {
	// Stop the keepalive before deciding whether an ordinary JSON exit is
	// possible. Phase two owns the already-committed SSE error presentation.
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		streamStarted = true
	}
	if !streamStarted {
		body := service.PresentExcelBPSError(c, status, gin.H{"type": errorType, "code": code, "message": message})
		errorType, _ = body["type"].(string)
		code, _ = body["code"].(string)
		message, _ = body["message"].(string)
	}
	h.handleStreamingAwareErrorWithCode(c, status, errorType, code, message, streamStarted, false)
}
