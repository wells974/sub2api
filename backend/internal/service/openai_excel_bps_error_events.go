package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// These functions run only at BPS output boundaries, after the original
// event has been consumed for usage, cooldowns and Ops. They never feed the
// transformed error back into the bridge or its repair callbacks.
func presentExcelBPSFailureDetails(c *gin.Context, status int, original gin.H) gin.H {
	if !excelBPSNeutralErrors(c) {
		return original
	}
	public := PresentExcelBPSError(c, status, original)
	// Preserve semantic status separately from the already-committed HTTP
	// status. Other internal metadata remains excluded by the allowlist.
	for _, key := range []string{"status", "status_code"} {
		switch value := original[key].(type) {
		case int:
			public[key] = value
		case float64:
			public[key] = value
		case json.Number:
			public[key] = value
		}
	}
	return public
}

func presentExcelBPSErrorLine(c *gin.Context, line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return line
	}
	payload := []byte(strings.TrimPrefix(line, "data: "))
	kind := gjson.GetBytes(payload, "type").String()
	switch kind {
	case "error", "response.failed", "response.cancelled", "response.incomplete":
	default:
		return line
	}
	return "data: " + string(presentExcelBPSErrorEvent(c, payload))
}

func presentExcelBPSErrorEvent(c *gin.Context, payload []byte) []byte {
	if !excelBPSNeutralErrors(c) {
		return payload
	}
	kind := gjson.GetBytes(payload, "type").String()
	switch kind {
	case "error", "response.failed", "response.cancelled", "response.incomplete":
	default:
		return payload
	}
	// Bridge output consists of complete JSON events. Keep all non-error bytes
	// (including large integer usage, IDs, output and sequence numbers) intact.
	result := payload
	for _, path := range []string{"response.error", "error"} {
		detail := gjson.GetBytes(result, path)
		if !detail.Exists() || detail.Type == gjson.Null {
			continue
		}
		original := gin.H{}
		decoder := json.NewDecoder(strings.NewReader(detail.Raw))
		decoder.UseNumber()
		_ = decoder.Decode(&original)
		if original == nil {
			original = gin.H{}
		}
		status := http.StatusBadGateway
		for _, path := range []string{"status", "status_code"} {
			if value := detail.Get(path); value.Type == gjson.Number && value.Int() >= 400 && value.Int() <= 599 {
				status = int(value.Int())
				break
			}
		}
		public := presentExcelBPSFailureDetails(c, status, original)
		encoded, _ := json.Marshal(public)
		if next, err := sjson.SetRawBytes(result, path, encoded); err == nil {
			result = next
		}
	}
	// Incomplete reasons can contain free-form upstream text. Retain standard
	// actionable reasons; discard non-contract metadata in that object.
	if detail := gjson.GetBytes(result, "response.incomplete_details"); detail.Exists() && detail.Type != gjson.Null {
		reason := detail.Get("reason").String()
		if reason != "max_output_tokens" && reason != "content_filter" {
			reason = "response_incomplete"
		}
		encoded, _ := json.Marshal(gin.H{"reason": reason})
		if next, err := sjson.SetRawBytes(result, "response.incomplete_details", encoded); err == nil {
			result = next
		}
	}
	return result
}

func writeExcelBPSCompactFailure(c *gin.Context, status int, code, message string, params ...string) {
	param := ""
	if len(params) > 0 {
		param = params[0]
	}
	writeOpenAICompactSSEFailureMessageParam(c, status, code, message, param, func(original map[string]any) map[string]any {
		return presentExcelBPSFailureDetails(c, status, gin.H(original))
	})
}

func writeExcelBPSIncompleteStream(c *gin.Context) {
	const payload = `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","code":"basispoints_stream_incomplete","message":"Upstream stream ended before completion"}}}`
	_, _ = c.Writer.WriteString("event: response.failed\n" + presentExcelBPSErrorLine(c, "data: "+payload) + "\n\n")
}
