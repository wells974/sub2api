package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/errorpresentation"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const excelBPSErrorPresentationEnv = "SUB2API_BPS_ERROR_PRESENTATION"

var excelBPSErrorPresentationWarning sync.Once

// PresentExcelBPSError is an explicit BPS output boundary, not a request-wide
// flag. Call it only after internal routing/diagnostic decisions, and never on
// a shared error object or on the input to a bridge/repair parser.
// Phase one handles ordinary error exits; accepted response events are handled
// by the original stream path until the separate event-presentation phase.
func PresentExcelBPSError(c *gin.Context, status int, original gin.H) gin.H {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	legacy, valid := errorpresentation.LegacyMode(os.Getenv(excelBPSErrorPresentationEnv))
	if !valid {
		excelBPSErrorPresentationWarning.Do(func() {
			logger.FromContext(ctx).Warn("invalid BPS error presentation mode; using neutral presentation")
		})
	}
	if legacy {
		return original
	}
	code, _ := original["code"].(string)
	message, _ := original["message"].(string)
	errorType, _ := original["type"].(string)
	public := errorpresentation.Public(excelBPSPublicErrorKind(code, message, status), status, errorType)
	// Keep bounded internal diagnostics under the existing storage redactor.
	// Do not overwrite Ops upstream status, attempt history or billing state.
	encoded, _ := json.Marshal(gin.H{"error": original})
	safe := excelBPSSanitizeErrorBody(string(encoded), "", &Account{})
	diagnostic, _ := sanitizeErrorBodyForStorage(safe, 2048)
	logger.FromContext(ctx).Info("bps_error_presentation", zap.Int("status", status),
		zap.String("public_code", public.Code), zap.String("internal_error", diagnostic))
	// Allowlist only these fields. In particular param/failure_stage/repair
	// metadata must not escape through a newly added upstream error field.
	return gin.H{"type": public.Type, "code": public.Code, "message": public.Message}
}

func excelBPSPublicErrorKind(code, message string, status int) errorpresentation.Kind {
	switch code {
	case "basispoints_rate_limited":
		return errorpresentation.RateLimited
	case "basispoints_image_body_too_large":
		return errorpresentation.RequestTooLarge
	case "basispoints_image_limit_reached", "basispoints_image_batch_too_large", "basispoints_image_history_limit", "basispoints_image_compaction_insufficient":
		return errorpresentation.ImageLimit
	case "invalid_encrypted_content", "basispoints_image_history_boundary", "basispoints_image_compact_recommended", "basispoints_image_auto_compact_unsupported", "basispoints_image_checkpoint_limit":
		return errorpresentation.ConversationInvalid
	case "basispoints_model_access_changed", "model_not_allowed", "excel_oauth_route_unavailable":
		return errorpresentation.ModelUnavailable
	case "basispoints_stream_incomplete":
		return errorpresentation.ResponseIncomplete
	case "basispoints_request_invalid":
		if strings.HasPrefix(message, "basispoints tool schema") || strings.HasPrefix(message, "conflicting duplicate Basispoints client tool") {
			return errorpresentation.InvalidToolSchema
		}
		if strings.Contains(message, "exceed") && (strings.Contains(message, "image") || strings.Contains(message, "MiB")) {
			return errorpresentation.ImageLimit
		}
		if strings.HasPrefix(message, "basispoints image budget exceeded:") {
			return errorpresentation.ImageLimit
		}
		if strings.Contains(message, "history") || strings.Contains(message, "checkpoint") {
			return errorpresentation.ConversationInvalid
		}
		return errorpresentation.InvalidRequest
	case "basispoints_transport_error", "basispoints_proxy_unavailable", "basispoints_state_unavailable", "basispoints_attachment_error", "basispoints_account_id_missing",
		"basispoints_image_settings_unavailable", "basispoints_image_relay_unavailable", "basispoints_image_relay_full", "basispoints_image_request_busy", "basispoints_image_policy_state_unavailable", "basispoints_image_checkpoint_unavailable",
		"basispoints_schema_repair_settings_invalid", "basispoints_tool_catalog_settings_invalid", "basispoints_tool_catalog_preparation_failed", "basispoints_usage_invalid":
		return errorpresentation.Unavailable
	}
	if strings.HasPrefix(code, "basispoints_auth_") || strings.HasPrefix(code, "OPENAI_EXCEL_AUTH_") {
		return errorpresentation.Unavailable
	}
	// In-band failures are not mapped here in phase one. Unknown future codes
	// fail closed to the fixed generic message; never echo their text.
	if code == "basispoints_upstream_error" && status == 401 {
		return errorpresentation.Unavailable
	}
	return errorpresentation.RequestFailed
}

func writeExcelBPSImagesError(c *gin.Context, original *OpenAIImagesUpstreamError) bool {
	if original == nil {
		return false
	}
	body := PresentExcelBPSError(c, original.clientStatusCode(), gin.H{
		"type": original.clientErrorType(), "code": original.Code, "message": original.clientMessage(), "param": original.Param,
	})
	copy := *original
	copy.ErrorType, _ = body["type"].(string)
	copy.Code, _ = body["code"].(string)
	copy.Message, _ = body["message"].(string)
	copy.Param, _ = body["param"].(string)
	return writeOpenAIImagesUpstreamErrorResponse(c, &copy)
}
