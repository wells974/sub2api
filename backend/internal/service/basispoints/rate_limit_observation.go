package basispoints

import (
	"context"
	"net/http"
	"sync/atomic"
)

// RateLimitObservation is private upstream metadata, not a client error. The
// observer MUST redact these strings before logging or storing them. No request
// content, response output, or arbitrary error fields are included.
type RateLimitObservation struct {
	Source, Event, Code, Type, Message string
}

type rateLimitObserverKey struct{}
type rateLimitObserver struct {
	observe func(RateLimitObservation)
	count   atomic.Uint32
}

// WithRateLimitObserver installs a synchronous observer shared by the main
// stream and its continuations. At most four errors per request are observed.
// The callback must not access mutable handler state or perform network I/O.
func WithRateLimitObserver(ctx context.Context, observe func(RateLimitObservation)) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, rateLimitObserverKey{}, &rateLimitObserver{observe: observe})
}

func observeRateLimit(ctx context.Context, source, kind string, payload object) {
	observer, _ := ctx.Value(rateLimitObserverKey{}).(*rateLimitObserver)
	if observer == nil {
		return
	}
	failure := classifyUpstreamFailure(kind, payload)
	if failure == nil || failure.Status != http.StatusTooManyRequests {
		return
	}
	if observer.count.Add(1) > 4 {
		return
	}
	response, _ := payload["response"].(object)
	detail, _ := response["error"].(object)
	if detail == nil {
		detail, _ = payload["error"].(object)
	}
	if detail == nil && kind == "error" {
		detail = payload
	}
	// Omit oversized fields in full: truncating a raw secret could prevent the
	// gateway from recognizing and redacting it later.
	field := func(key string) string {
		value := text(detail[key])
		if len(value) > 8192 {
			return "[omitted: oversized upstream field]"
		}
		return value
	}
	observer.observe(RateLimitObservation{Source: source, Event: kind,
		Code: field("code"), Type: field("type"), Message: field("message")})
}
