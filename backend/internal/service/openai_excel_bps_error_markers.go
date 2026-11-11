package service

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Shared by diagnostic projections and header filtering. Never apply to model
// output, prompts, user-selected labels or image content.
var excelBPSMarker = regexp.MustCompile(`(?i)(basispoints|bps\.openai\.com|excel[\s/_-]+(?:bps|oauth)|\bbps\s+image|sub2api_bps_|openai_excel_auth_|/api/bps-images/)`)

func hasExcelBPSMarker(value string) bool { return excelBPSMarker.MatchString(value) }

func hasExcelBPSDiagnosticMarker(body string) bool {
	if hasExcelBPSMarker(body) {
		return true
	}
	containsJSON := func(raw string) bool {
		var value any
		if json.Unmarshal([]byte(raw), &value) != nil {
			return false
		}
		// Iterative traversal also covers escaped marker strings in unknown
		// metadata fields, without depending on a provider's error schema.
		pending := []any{value}
		for len(pending) > 0 {
			value, pending = pending[len(pending)-1], pending[:len(pending)-1]
			switch node := value.(type) {
			case string:
				if hasExcelBPSMarker(node) {
					return true
				}
			case map[string]any:
				for key, child := range node {
					if hasExcelBPSMarker(key) {
						return true
					}
					pending = append(pending, child)
				}
			case []any:
				pending = append(pending, node...)
			}
		}
		return false
	}
	if containsJSON(body) {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data:") && containsJSON(strings.TrimSpace(line[5:])) {
			return true
		}
	}
	return false
}
