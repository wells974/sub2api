package basispoints

// Read-only statistics; acceptance and upload policy belong to attachments.go.

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ImageObservation describes the original incoming request, before any policy
// rewrites or uploads. Bytes count occurrences, including duplicate images.
// Unknown payloads are excluded from bytes, never treated as zero-sized images.
type ImageObservation struct {
	ValidJSON       bool  `json:"valid_json"`
	InlineCount     int   `json:"inline_count"`
	InlineBytes     int64 `json:"inline_bytes"`
	MaxInlineBytes  int64 `json:"max_inline_bytes"`
	MessageBytes    int64 `json:"message_inline_bytes"`
	ToolOutputBytes int64 `json:"tool_output_inline_bytes"`
	UnknownBytes    int   `json:"unknown_inline_byte_count"`
	URLCount        int   `json:"url_count"`
	FileCount       int   `json:"file_count"`
}

// ObserveRequestImages does not decode image buffers, fetch URLs, or inspect
// arbitrary text/tool arguments. Base64 syntax is checked only to measure its
// decoded length; media validity and existing limits remain the validator's job.
func ObserveRequestImages(raw []byte) ImageObservation {
	result := ImageObservation{ValidJSON: gjson.ValidBytes(raw)}
	if !result.ValidJSON {
		return result
	}
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return result
	}
	input.ForEach(func(_, item gjson.Result) bool {
		field := "content"
		toolOutput := false
		switch item.Get("type").String() {
		case "", "message", "agent_message":
		case "function_call_output", "custom_tool_call_output":
			field, toolOutput = "output", true
		default:
			return true
		}
		parts := item.Get(field)
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "input_image" {
				return true
			}
			if part.Get("file_id").Exists() {
				result.FileCount++
			}
			url := part.Get("image_url")
			if url.Type != gjson.String {
				return true
			}
			value := url.String()
			if len(value) < 5 || !strings.EqualFold(value[:5], "data:") {
				if value != "" {
					result.URLCount++
				}
				return true
			}
			result.InlineCount++
			size, known := inlineDecodedLength(value)
			if !known {
				result.UnknownBytes++
				return true
			}
			result.InlineBytes += size
			if size > result.MaxInlineBytes {
				result.MaxInlineBytes = size
			}
			if toolOutput {
				result.ToolOutputBytes += size
			} else {
				result.MessageBytes += size
			}
			return true
		})
		return true
	})
	return result
}

func inlineDecodedLength(value string) (int64, bool) {
	comma := strings.IndexByte(value, ',')
	if comma < 0 || !strings.HasSuffix(strings.ToLower(value[:comma]), ";base64") {
		return 0, false
	}
	var count, padding int64
	for _, char := range value[comma+1:] {
		switch {
		case char == '\r' || char == '\n':
			continue // encoding/base64 ignores CR/LF, but not other whitespace.
		case char == '=':
			padding++
			if padding > 2 {
				return 0, false
			}
		case (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '+' || char == '/':
			if padding != 0 {
				return 0, false
			}
		default:
			return 0, false
		}
		count++
	}
	if count == 0 || count%4 != 0 {
		return 0, false
	}
	return count/4*3 - padding, true
}
