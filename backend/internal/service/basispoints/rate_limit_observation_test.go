package basispoints

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRateLimitObservationBeforeNormalization(t *testing.T) {
	for _, kind := range []string{"error", "response.failed", "response.cancelled"} {
		for _, mode := range []string{"generation", "tool_repair", "unknown_tool_repair", "search", "image_compaction"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				detail := object{"code": "rate_limit_exceeded", "type": "tokens", "message": "Limit 30000, used 29000, requested 22000; retry in 30s"}
				payload := object{"type": kind, "response": object{"status": "failed", "output": []any{}, "error": detail}}
				if kind == "error" {
					payload = object{"type": kind, "error": detail}
				}
				wire := sse(payload)
				var observations []RateLimitObservation
				ctx := WithRateLimitObserver(context.Background(), func(o RateLimitObservation) { observations = append(observations, o) })
				reader := strings.NewReader(wire)
				switch mode {
				case "generation":
					_, bridge := mustPrepare(t, testSource(), "", nil)
					body := bridge.StreamWithRepairs(ctx, io.NopCloser(reader), nil, nil)
					defer body.Close()
					out, err := io.ReadAll(body)
					require.NoError(t, err)
					require.NotContains(t, string(out), detail["message"])
					_, baseline := mustPrepare(t, testSource(), "", nil)
					original := baseline.Stream(io.NopCloser(strings.NewReader(wire)))
					defer original.Close()
					want, err := io.ReadAll(original)
					require.NoError(t, err)
					require.Equal(t, string(want), string(out), "observation must not alter the client wire")
				case "tool_repair":
					_, err := ReadClientToolRepairResponseWithContext(ctx, reader, nil)
					require.Error(t, err)
				case "unknown_tool_repair":
					_, err := readRepairResponseWithContext(ctx, reader)
					require.Error(t, err)
				case "search":
					_, err := readSearchRound(ctx, reader, func(string, object) error { return nil })
					require.Error(t, err)
				case "image_compaction":
					_, err := ReadImageCompactionWithContext(ctx, reader, nil)
					require.Error(t, err)
				}
				require.Equal(t, []RateLimitObservation{{Source: mode, Event: kind, Code: "rate_limit_exceeded", Type: "tokens", Message: detail["message"].(string)}}, observations)
			})
		}
	}
}

func TestRateLimitObservationBoundaries(t *testing.T) {
	var observations []RateLimitObservation
	ctx := WithRateLimitObserver(context.Background(), func(o RateLimitObservation) { observations = append(observations, o) })
	for _, kind := range []string{"response.completed", "response.output_text.delta"} {
		observeRateLimit(ctx, "generation", kind, object{"error": object{"code": "rate_limit_exceeded"}})
	}
	observeRateLimit(ctx, "generation", "error", object{"code": "permission_denied", "message": "not a rate limit"})
	require.Empty(t, observations)
	observeRateLimit(ctx, "generation", "error", object{"code": "rate_limit_exceeded", "type": "tokens", "message": strings.Repeat("secret", 2000)})
	require.Len(t, observations, 1)
	require.Equal(t, "tokens", observations[0].Type, "flat errors preserve the original type")
	require.Equal(t, "[omitted: oversized upstream field]", observations[0].Message)

	var count atomic.Int32
	ctx = WithRateLimitObserver(context.Background(), func(RateLimitObservation) { count.Add(1) })
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			observeRateLimit(ctx, "generation", "error", object{"code": "rate_limit_exceeded"})
		}()
	}
	wg.Wait()
	require.EqualValues(t, 4, count.Load())
}
