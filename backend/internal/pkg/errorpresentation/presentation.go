// Package errorpresentation contains transport-independent public error text.
// Callers retain the original error for policy, accounting and diagnostics.
package errorpresentation

import "strings"

type Kind string

const (
	RequestFailed       Kind = "request_failed"
	InvalidRequest      Kind = "invalid_request"
	Unavailable         Kind = "service_unavailable"
	RateLimited         Kind = "rate_limit_exceeded"
	RequestTooLarge     Kind = "request_too_large"
	ImageLimit          Kind = "image_limit_exceeded"
	ConversationInvalid Kind = "conversation_context_invalid"
	InvalidToolSchema   Kind = "invalid_tool_schema"
	InvalidToolResponse Kind = "invalid_tool_response"
	ModelUnavailable    Kind = "model_unavailable"
	ResponseIncomplete  Kind = "response_incomplete"
)

type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LegacyMode fails closed to neutral presentation on an invalid setting.
// Configuration belongs to the caller, not this package.
func LegacyMode(raw string) (legacy, valid bool) {
	switch strings.TrimSpace(raw) {
	case "", "neutral":
		return false, true
	case "legacy":
		return true, true
	default:
		return false, false
	}
}

// Public accepts no provider message, path or metadata, so unknown errors
// cannot accidentally be copied into the public response.
func Public(kind Kind, status int, originalType string) Error {
	message := ""
	switch kind {
	case InvalidRequest:
		message = "The request is invalid. Check the request parameters and try again."
	case Unavailable:
		message = "The service is temporarily unavailable. Please try again later."
	case RateLimited:
		message = "Too many requests. Please try again later."
	case RequestTooLarge:
		message = "The request is too large. Reduce its size and try again."
	case ImageLimit:
		message = "The request contains too many images or exceeds the image size limit. Reduce the images and try again."
	case ConversationInvalid:
		message = "The conversation context could not be processed. Reduce the history or start a new conversation."
	case InvalidToolSchema:
		message = "A tool definition is invalid. Check the tool schema and try again."
	case InvalidToolResponse:
		message = "The generated tool call could not be validated. No tool in this batch was executed."
	case ModelUnavailable:
		message = "The requested model is currently unavailable."
	case ResponseIncomplete:
		message = "The response ended before completion."
	default:
		kind = RequestFailed
		message = "The request could not be completed. Please try again later."
	}
	// Preserve existing standard protocol types, without reflecting arbitrary
	// upstream strings. HTTP status is never changed by presentation.
	switch originalType {
	case "invalid_request_error", "server_error", "upstream_error", "api_error", "authentication_error", "permission_error", "rate_limit_error":
	default:
		originalType = "invalid_request_error"
		if status >= 500 {
			originalType = "server_error"
		}
	}
	return Error{Type: originalType, Code: string(kind), Message: message}
}
