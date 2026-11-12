package basispoints

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLatencyObservationStreamingAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, o := WithLatencyObservation(ctx, time.Now())
	source := testSource()
	source["stream"] = true
	_, bridge := mustPrepare(t, source, "latency-cancel", nil)
	upstream, producer := io.Pipe()
	defer producer.Close()
	body := bridge.StreamWithRepairs(ctx, upstream, nil, nil)
	defer body.Close()
	textWritten := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(body)
		seen := false
		for scanner.Scan() {
			line := scanner.Text()
			ObserveClientLatencyLine(ctx, line)
			if !seen && o.Snapshot().ClientWrite.Text {
				seen = true
				close(textWritten)
			}
		}
		finished <- scanner.Err()
	}()
	_, err := io.WriteString(producer, sse(object{"type": "response.output_text.delta", "delta": "PRIVATE_STREAM_TEXT"}))
	require.NoError(t, err)
	select {
	case <-textWritten:
	case <-time.After(3 * time.Second):
		t.Fatal("text was buffered awaiting a terminal response")
	}
	require.Nil(t, o.Snapshot().Upstream.TerminalMs)
	cancel()
	select {
	case err := <-finished:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("observer blocked stream cancellation")
	}
	require.Equal(t, "text_only", o.Snapshot().ClientWrite.OutputType)
}

func TestLatencyObservationAttachmentWaitCancellation(t *testing.T) {
	url, _ := nativeTestURL(t)
	plan, err := PrepareNativeImages(nativeTestRequest(t, url))
	require.NoError(t, err)
	cache := new(AttachmentCache)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := plan.Upload(context.Background(), cache, "same-scope", func(context.Context, InlineAttachment) (string, error) {
			close(started)
			<-release
			return "file-shared", nil
		})
		done <- e
	}()
	defer func() { close(release); require.NoError(t, <-done) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, o := WithLatencyObservation(ctx, time.Now())
	second, err := PrepareNativeImages(nativeTestRequest(t, url))
	require.NoError(t, err)
	waitDone := make(chan error, 1)
	go func() {
		_, e := second.Upload(ctx, cache, "same-scope", func(context.Context, InlineAttachment) (string, error) {
			return "", errors.New("waiter must not upload")
		})
		waitDone <- e
	}()
	require.Eventually(t, func() bool { return o.Snapshot().Attachments.InFlightWaits == 1 }, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-waitDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("attachment observer blocked waiter cancellation")
	}
	require.Zero(t, o.Snapshot().Attachments.UploadAttempts)
	require.Zero(t, o.Snapshot().Attachments.CacheHits)
}

func TestLatencyObservationOutputTypes(t *testing.T) {
	for _, tc := range []struct {
		name, raw, output string
		reasoningText     bool
	}{
		{"metadata", `{"type":"response.created"}`, "none", false},
		{"empty_reasoning", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, "reasoning_only", false},
		{"reasoning", `{"type":"response.reasoning_summary_text.delta","delta":"PRIVATE_REASONING"}`, "reasoning_only", true},
		{"refusal", `{"type":"response.refusal.done","refusal":"PRIVATE_REFUSAL"}`, "refusal_only", false},
		{"custom_tool", `{"type":"response.custom_tool_call_input.delta","delta":"PRIVATE_INPUT"}`, "tool_only", false},
		{"other", `{"type":"response.output_item.done","item":{"type":"PRIVATE_UNKNOWN_TYPE"}}`, "other", false},
		{"terminal_text", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"PRIVATE_TEXT"}]}]}}`, "text_only", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, o := WithLatencyObservation(context.Background(), time.Now())
			ObserveClientLatencyLine(ctx, "data: "+tc.raw)
			s := o.Snapshot()
			require.Equal(t, tc.output, s.ClientWrite.OutputType)
			require.Equal(t, tc.reasoningText, s.ClientWrite.FirstReasoningMs != nil)
			encoded, err := json.Marshal(s)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "PRIVATE")
		})
	}
}

func TestLatencyObservationClocksAndPrivacy(t *testing.T) {
	start := time.Now()
	ctx, o := WithLatencyObservation(context.Background(), start)
	clock := start
	o.stageAt, o.now = clock, func() time.Time { return clock }
	clock = start.Add(3 * time.Millisecond)
	o.Advance("image_history")
	clock = start.Add(8 * time.Millisecond)
	o.Advance("generation_receive")
	observeUpstreamLatency(ctx, "response.created", []byte(`{"type":"response.created"}`))
	clock = start.Add(10 * time.Millisecond)
	observeUpstreamLatency(ctx, "response.output_item.added", []byte(`{"item":{"type":"function_call","name":"PRIVATE_TOOL","arguments":"PRIVATE_ARGUMENT"}}`))
	clock = start.Add(20 * time.Millisecond)
	ObserveClientLatencyLine(ctx, `data: {"type":"response.output_item.added","item":{"type":"function_call","name":"PRIVATE_TOOL"}}`)
	ObserveClientLatencyLine(ctx, `data: {"type":"response.output_text.delta","delta":""}`)
	ObserveClientLatencyLine(ctx, `: keepalive`)
	ObserveClientLatencyLine(ctx, `data: [DONE]`)
	ObserveClientLatencyLine(ctx, `data: invalid`)
	snapshot := o.Snapshot()
	require.EqualValues(t, 3, snapshot.StagesMs["setup"])
	require.EqualValues(t, 5, snapshot.StagesMs["image_history"])
	require.EqualValues(t, 12, snapshot.StagesMs["generation_receive"])
	require.EqualValues(t, 8, *snapshot.Upstream.FirstEventMs)
	require.EqualValues(t, 10, *snapshot.Upstream.FirstToolMs)
	require.EqualValues(t, 20, *snapshot.ClientWrite.FirstToolMs)
	require.Equal(t, "tool_only", snapshot.ClientWrite.OutputType)
	require.Nil(t, snapshot.ClientWrite.FirstTextMs)
	require.Nil(t, snapshot.JSONReturnMs)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE")
	*snapshot.Upstream.FirstToolMs = 900
	snapshot.StagesMs["setup"] = 999
	require.EqualValues(t, 10, *o.Snapshot().Upstream.FirstToolMs)
	require.EqualValues(t, 3, o.Snapshot().StagesMs["setup"])
	ObserveClientLatencyLine(ctx, `data: {"type":"response.output_text.delta","delta":"PRIVATE_TEXT"}`)
	ObserveJSONReturn(ctx)
	require.Equal(t, "text_and_tool", o.Snapshot().ClientWrite.OutputType)
	require.EqualValues(t, 20, *o.Snapshot().JSONReturnMs)
}

func TestLatencyObservationWireAndToolBoundary(t *testing.T) {
	for _, mode := range []string{"text", "tool", "mixed", "invalid", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			var first []byte
			for _, enabled := range []bool{false, true} {
				source := testSource()
				source["stream"] = true
				source["tools"] = []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}}
				_, bridge := mustPrepare(t, source, "latency-wire", nil)
				call := nativeCall(object{"name": "shell", "arguments": object{"cmd": "PRIVATE_CMD"}})
				if mode == "invalid" {
					call = nativeCall(object{"name": "PRIVATE_UNKNOWN_TOOL", "arguments": object{}})
				}
				wire := sse(object{"type": "response.created", "response": object{"id": "resp_latency", "output": []any{}}})
				items := []any{}
				if mode == "text" || mode == "mixed" {
					wire += sse(object{"type": "response.output_text.delta", "delta": "PRIVATE_TEXT"})
				}
				if mode != "text" {
					wire += sse(object{"type": "response.output_item.added", "item": call})
					wire += sse(object{"type": "response.output_item.done", "item": call})
					items = append(items, call)
				}
				if mode != "truncated" {
					wire += sse(object{"type": "response.completed", "response": repairResponse("resp_latency", 10, 2, items...)})
				}
				ctx := context.Background()
				var o *LatencyObservation
				if enabled {
					ctx, o = WithLatencyObservation(ctx, time.Now())
				}
				body := bridge.StreamWithRepairs(ctx, io.NopCloser(strings.NewReader(wire)), nil, nil)
				out, err := io.ReadAll(body)
				require.NoError(t, body.Close())
				if mode == "truncated" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				if !enabled {
					first = out
					continue
				}
				require.Equal(t, string(first), string(out), "observer must preserve wire bytes")
				for _, line := range strings.Split(string(out), "\n") {
					ObserveClientLatencyLine(ctx, line)
				}
				s := o.Snapshot()
				require.NotNil(t, s.Upstream.FirstEventMs)
				if mode == "invalid" || mode == "truncated" {
					require.True(t, s.Upstream.Tool)
					require.False(t, s.ClientWrite.Tool, "unvalidated tool must not be released")
				} else if mode == "tool" || mode == "mixed" {
					require.True(t, s.ClientWrite.Tool)
					require.GreaterOrEqual(t, *s.ClientWrite.FirstToolMs, *s.Upstream.TerminalMs)
				}
				if mode == "text" || mode == "mixed" {
					require.True(t, s.Upstream.Text)
					require.True(t, s.ClientWrite.Text)
				}
			}
		})
	}
}

func TestLatencyObservationAttachments(t *testing.T) {
	url, data := nativeTestURL(t)
	cache := new(AttachmentCache)
	for pass := 0; pass < 2; pass++ {
		ctx, o := WithLatencyObservation(context.Background(), time.Now())
		plan, err := PrepareNativeImages(nativeTestRequest(t, url, url))
		require.NoError(t, err)
		_, err = plan.Upload(ctx, cache, "latency-cache", func(context.Context, InlineAttachment) (string, error) { return "file-latency", nil })
		require.NoError(t, err)
		s := o.Snapshot().Attachments
		if pass == 0 {
			require.Equal(t, 1, s.UploadAttempts, "same-request dedup is not a cache hit")
			require.EqualValues(t, len(data), s.UploadBytes)
			require.Zero(t, s.CacheHits)
		} else {
			require.Equal(t, 1, s.CacheHits)
			require.Zero(t, s.UploadAttempts)
		}
	}
	ctx, o := WithLatencyObservation(context.Background(), time.Now())
	plan, err := PrepareNativeImages(nativeTestRequest(t, url))
	require.NoError(t, err)
	want := errors.New("PRIVATE_UPLOAD_ERROR")
	_, err = plan.Upload(ctx, cache, "", func(context.Context, InlineAttachment) (string, error) { return "", want })
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, o.Snapshot().Attachments.UploadFailures)
}

func TestLatencyObservationConcurrentSnapshot(t *testing.T) {
	ctx, o := WithLatencyObservation(context.Background(), time.Now())
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				observeAttachmentHit(ctx)
				ObserveClientLatencyLine(ctx, `data: {"type":"response.output_text.delta","delta":"x"}`)
				o.Advance("finish")
				_ = o.Snapshot()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 800, o.Snapshot().Attachments.CacheHits)
}
