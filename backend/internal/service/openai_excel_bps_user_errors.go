package service

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/errorpresentation"
	"github.com/tidwall/gjson"
)

var excelBPSStoredErrorCode = regexp.MustCompile(`(?i)\b(?:basispoints_[a-z0-9_]+|openai_excel_auth_[a-z0-9_]+|excel_oauth_route_unavailable)\b`)

// Read-time projection only: historic records benefit without rewriting Ops
// storage. Neither admin nor observer diagnostic views call these adapters.
func toPublicUserErrorRequest(original *OpsErrorLog) *UserErrorRequest {
	out := ToUserErrorRequest(original)
	if out == nil || !excelBPSNeutralErrors(nil) || !isExcelBPSStoredError(original, "") {
		return out
	}
	out.Message = publicExcelBPSStoredError(original, "").Message
	return out
}

func toPublicUserErrorRequestDetail(original *OpsErrorLogDetail) *UserErrorRequestDetail {
	out := ToUserErrorRequestDetail(original)
	if out == nil || !excelBPSNeutralErrors(nil) || !isExcelBPSStoredError(&original.OpsErrorLog, original.ErrorBody) {
		return out
	}
	public := publicExcelBPSStoredError(&original.OpsErrorLog, original.ErrorBody)
	out.Message = public.Message
	// Do not recursively redact selected keys in a provider-controlled body.
	// A new field, HTML response or truncated SSE must not bypass this boundary.
	encoded, _ := json.Marshal(struct {
		Error errorpresentation.Error `json:"error"`
	}{public})
	out.ErrorBody = string(encoded)
	return out
}

func isExcelBPSStoredError(original *OpsErrorLog, body string) bool {
	// Do not inspect model, key/group/account names, user agent or request
	// content. Those are user/operator-controlled labels, not route evidence.
	for _, value := range []string{original.UpstreamEndpoint, original.Type, original.Message, body} {
		if hasExcelBPSMarker(value) {
			return true
		}
	}
	// JSON escaping in old error bodies must not defeat route recognition.
	for _, path := range []string{"error.code", "error.type", "error.message", "response.error.code", "response.error.message"} {
		if hasExcelBPSMarker(gjson.Get(body, path).String()) {
			return true
		}
	}
	return false
}

func hasExcelBPSMarker(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "basispoints") || strings.Contains(lower, "bps.openai.com") ||
		strings.Contains(lower, "excel bps") || strings.Contains(lower, "excel oauth") ||
		strings.Contains(lower, "excel / bps") || strings.Contains(lower, "bps image") ||
		strings.Contains(lower, "excel_bps") || strings.Contains(lower, "sub2api_bps_") ||
		strings.Contains(lower, "excel_oauth_") || strings.Contains(lower, "openai_excel_auth_") ||
		strings.Contains(lower, "/api/bps-images/")
}

func publicExcelBPSStoredError(original *OpsErrorLog, body string) errorpresentation.Error {
	code, message := original.Type, original.Message
	// Prefer the same structured error code used by live responses. Only the
	// classification is consumed; no stored string is copied to public output.
	for _, path := range []string{"error", "response.error"} {
		if detail := gjson.Get(body, path); detail.IsObject() {
			if v := detail.Get("code").String(); v != "" {
				code = v
			}
			if v := detail.Get("message").String(); v != "" {
				message = v
			}
			break
		}
	}
	if v := excelBPSStoredErrorCode.FindString(message); v != "" {
		code = strings.ToLower(v)
	}
	if strings.HasPrefix(message, "basispoints function arguments") {
		code = "basispoints_protocol_error"
	}
	return errorpresentation.Public(excelBPSPublicErrorKind(code, message, original.StatusCode), original.StatusCode, original.Type)
}
