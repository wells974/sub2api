package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Only the image adapter forwards upstream headers through the shared writer.
// Filter its copy, retaining original headers for request IDs, quota and Ops.
// This also applies when an administrator expands the shared header allowlist.
func presentExcelBPSImageResponse(c *gin.Context, original *http.Response) *http.Response {
	if original == nil || !excelBPSNeutralErrors(c) {
		return original
	}
	copy := *original
	copy.Header = original.Header.Clone()
	for key, values := range copy.Header {
		lower := strings.ToLower(key)
		private := hasExcelBPSMarker(key) || strings.HasPrefix(lower, "x-bps-") ||
			lower == "x-codex2api-upstream"
		for _, value := range values {
			private = private || hasExcelBPSMarker(value)
		}
		if private {
			delete(copy.Header, key)
		}
	}
	return &copy
}
