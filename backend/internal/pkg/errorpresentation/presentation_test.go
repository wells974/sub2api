package errorpresentation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicErrorModesAndAllowlist(t *testing.T) {
	for raw, want := range map[string][2]bool{"": {false, true}, "neutral": {false, true}, "legacy": {true, true}, " typo ": {false, false}} {
		legacy, valid := LegacyMode(raw)
		if legacy != want[0] || valid != want[1] {
			t.Fatalf("mode %q: %t %t", raw, legacy, valid)
		}
	}
	for _, kind := range []Kind{RequestFailed, InvalidRequest, Unavailable, RateLimited, RequestTooLarge, ImageLimit, ConversationInvalid, InvalidToolSchema, InvalidToolResponse, ModelUnavailable, ResponseIncomplete, "private-provider-error"} {
		for _, status := range []int{400, 401, 403, 413, 429, 500, 502, 503, 524} {
			got := Public(kind, status, "private-provider-type")
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "private") || got.Message == "" {
				t.Fatalf("unsafe public result: %s", encoded)
			}
			if status >= 500 && got.Type != "server_error" {
				t.Fatal(got)
			}
		}
	}
	if got := Public(RateLimited, 429, "rate_limit_error"); got.Type != "rate_limit_error" || got.Code != "rate_limit_exceeded" {
		t.Fatal(got)
	}
}
