package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"go.uber.org/zap"
)

var (
	bpsDiagnosticURL        = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	bpsDiagnosticEmail      = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	bpsDiagnosticCredential = regexp.MustCompile(`\b(?:eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|sk-[A-Za-z0-9_-]+|org-[A-Za-z0-9_-]+|proj_[A-Za-z0-9_-]+)\b`)
	// Echoed request content and header values are not useful rate-limit hints.
	// Drop the entire suffix, including multiword values and cookie attributes.
	bpsDiagnosticPrivateSuffix = regexp.MustCompile(`(?is)\b(?:prompt|input|messages|content|cookie|authorization|password|access_token|refresh_token|id_token|api_key|apikey|token|secret|key|ticket|recovery_ticket|session|session_id)["']?\s*[:=].*`)
)

func withExcelBPSRateLimitDiagnostics(ctx context.Context, account *Account, token, model string) context.Context {
	// Snapshot credentials before starting a bridge goroutine. The observer
	// never reads gin.Context, mutable account state, or request/response bodies.
	secrets := append([]string{token}, excelBPSAccountSecrets(account)...)
	for _, key := range []string{"chatgpt_account_id", "email", "password"} {
		secrets = append(secrets, account.GetCredential(key))
	}
	if account.Proxy != nil {
		secrets = append(secrets, account.Proxy.Username)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	log := logger.FromContext(ctx).With(zap.String("component", "service.openai_excel_bps"),
		zap.Int64("account_id", account.ID), zap.String("platform", PlatformOpenAI), zap.String("model", model))
	return basispoints.WithRateLimitObserver(ctx, func(observation basispoints.RateLimitObservation) {
		payload := map[string]string{
			"source": observation.Source, "event": observation.Event,
			"upstream_code":    sanitizeBPSRateLimitDiagnostic(observation.Code, secrets),
			"upstream_type":    sanitizeBPSRateLimitDiagnostic(observation.Type, secrets),
			"upstream_message": sanitizeBPSRateLimitDiagnostic(observation.Message, secrets),
		}
		encoded, _ := json.Marshal(payload)
		// WARN is indexed by OpsSystemLogSink. Its existing `errors` field is
		// displayed by both desktop and mobile system-log views; no raw payload
		// is added to public errors or SSE events.
		log.Warn("bps_stream_rate_limited", zap.String("errors", string(encoded)))
	})
}

func sanitizeBPSRateLimitDiagnostic(value string, secrets []string) string {
	if len(value) > 8192 {
		return "[omitted: oversized upstream field]"
	}
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = bpsDiagnosticPrivateSuffix.ReplaceAllString(value, "[redacted]")
	value = excelBPSBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = bpsDiagnosticURL.ReplaceAllString(value, "[redacted URL]")
	value = bpsDiagnosticEmail.ReplaceAllString(value, "[redacted email]")
	value = bpsDiagnosticCredential.ReplaceAllString(value, "[redacted]")
	value = excelBPSImageCapabilityPattern.ReplaceAllString(value, "/api/bps-images/[redacted]")
	value = excelBPSAttachmentIDPattern.ReplaceAllString(value, "file-[redacted]")
	value = logredact.RedactText(sanitizeUpstreamErrorMessage(value), "api_key", "apikey", "token", "secret", "key", "ticket", "recovery_ticket", "session", "session_id")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	return truncateString(strings.Join(strings.Fields(value), " "), 2048)
}
