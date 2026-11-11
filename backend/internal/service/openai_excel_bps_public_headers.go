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
		private := hasExcelBPSMarker(key) || strings.HasPrefix(lower, "x-bps-") || strings.HasPrefix(lower, "x-excel-") ||
			lower == "x-codex2api-upstream"
		for _, value := range values {
			brand := strings.ToLower(strings.TrimSpace(value))
			private = private || hasExcelBPSMarker(value) || brand == "bps" || brand == "excel"
		}
		if private {
			delete(copy.Header, key)
		}
	}
	return &copy
}
