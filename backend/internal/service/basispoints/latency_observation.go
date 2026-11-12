package basispoints

// Request-local, bounded observations. No observer owns a reader/writer, queues
// events, changes cache decisions, or retains user payloads.
import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

type latencyObservationKey struct{}

type OutputLatencyObservation struct {
	OutputType       string `json:"output_type"`
	FirstEventMs     *int64 `json:"first_event_ms,omitempty"`
	FirstTextMs      *int64 `json:"first_text_ms,omitempty"`
	FirstReasoningMs *int64 `json:"first_reasoning_ms,omitempty"`
	FirstToolMs      *int64 `json:"first_tool_ms,omitempty"`
	TerminalMs       *int64 `json:"terminal_ms,omitempty"`
	Terminal         string `json:"terminal,omitempty"`
	Text             bool   `json:"text"`
	Reasoning        bool   `json:"reasoning"`
	Tool             bool   `json:"tool"`
	Refusal          bool   `json:"refusal"`
	Other            bool   `json:"other"`
}

type AttachmentLatencyObservation struct {
	CacheHits      int   `json:"cache_hits"`
	InFlightWaits  int   `json:"inflight_waits"`
	InFlightWaitMs int64 `json:"inflight_wait_ms"`
	UploadAttempts int   `json:"upload_attempts"`
	UploadFailures int   `json:"upload_failures"`
	UploadBytes    int64 `json:"upload_bytes"`
	UploadMs       int64 `json:"upload_ms"`
}

type LatencySnapshot struct {
	ForwardElapsedMs int64                        `json:"forward_elapsed_ms"`
	EntryOffsetMs    int64                        `json:"entry_offset_ms"`
	StagesMs         map[string]int64             `json:"stages_ms"`
	LastStage        string                       `json:"last_stage"`
	Upstream         OutputLatencyObservation     `json:"upstream"`
	ClientWrite      OutputLatencyObservation     `json:"client_write"`
	JSONReturnMs     *int64                       `json:"json_return_ms,omitempty"`
	Attachments      AttachmentLatencyObservation `json:"attachments"`
}

type LatencyObservation struct {
	mu      sync.Mutex
	start   time.Time
	stageAt time.Time
	now     func() time.Time
	value   LatencySnapshot
}

func WithLatencyObservation(ctx context.Context, start time.Time) (context.Context, *LatencyObservation) {
	now := time.Now()
	o := &LatencyObservation{start: start, stageAt: now, now: time.Now}
	o.value.EntryOffsetMs = now.Sub(start).Milliseconds()
	o.value.StagesMs = make(map[string]int64)
	o.value.LastStage = "setup"
	return context.WithValue(ctx, latencyObservationKey{}, o), o
}

func latencyObservation(ctx context.Context) *LatencyObservation {
	o, _ := ctx.Value(latencyObservationKey{}).(*LatencyObservation)
	return o
}

// Advance records sequential wall-clock stages, including network/cache waits.
// Names are a fixed vocabulary so this cannot log caller-provided strings.
func (o *LatencyObservation) Advance(stage string) {
	switch stage {
	case "image_history", "image_prepare", "catalog_prepare", "authorization_proxy", "attachment_upload", "attachment_reprepare", "auto_compact", "catalog_delivery", "generation_headers", "generation_receive", "finish":
	default:
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.value.StagesMs[o.value.LastStage] += now.Sub(o.stageAt).Milliseconds()
	o.value.LastStage, o.stageAt = stage, now
}

func (o *LatencyObservation) Snapshot() LatencySnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	s := o.value
	s.ForwardElapsedMs = now.Sub(o.start).Milliseconds()
	s.StagesMs = make(map[string]int64, len(o.value.StagesMs)+1)
	for k, v := range o.value.StagesMs {
		s.StagesMs[k] = v
	}
	s.StagesMs[s.LastStage] += now.Sub(o.stageAt).Milliseconds()
	s.Upstream = copyOutputLatency(s.Upstream)
	s.ClientWrite = copyOutputLatency(s.ClientWrite)
	s.JSONReturnMs = copyLatencyTime(s.JSONReturnMs)
	return s
}

func copyLatencyTime(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyOutputLatency(s OutputLatencyObservation) OutputLatencyObservation {
	s.FirstEventMs = copyLatencyTime(s.FirstEventMs)
	s.FirstTextMs = copyLatencyTime(s.FirstTextMs)
	s.FirstReasoningMs = copyLatencyTime(s.FirstReasoningMs)
	s.FirstToolMs = copyLatencyTime(s.FirstToolMs)
	s.TerminalMs = copyLatencyTime(s.TerminalMs)
	s.OutputType = "none"
	switch {
	case s.Text && s.Tool:
		s.OutputType = "text_and_tool"
	case s.Tool:
		s.OutputType = "tool_only"
	case s.Text:
		s.OutputType = "text_only"
	case s.Refusal:
		s.OutputType = "refusal_only"
	case s.Other:
		s.OutputType = "other"
	case s.Reasoning:
		s.OutputType = "reasoning_only"
	}
	return s
}

func firstLatency(dst **int64, ms int64) {
	if *dst == nil {
		value := ms
		*dst = &value
	}
}

// One classifier is shared by parsed upstream events and downstream wire events.
// The getter is synchronous; it is never saved in the observer.
func observeLatencyEvent(ctx context.Context, upstream bool, kind string, get func(string) gjson.Result) {
	o := latencyObservation(ctx)
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ms := o.now().Sub(o.start).Milliseconds()
	s := &o.value.ClientWrite
	if upstream {
		s = &o.value.Upstream
	}
	firstLatency(&s.FirstEventMs, ms)
	observePart := func(part gjson.Result) {
		switch part.Get("type").String() {
		case "output_text":
			if part.Get("text").String() != "" {
				s.Text = true
				firstLatency(&s.FirstTextMs, ms)
			}
		case "summary_text":
			if part.Get("text").String() != "" {
				s.Reasoning = true
				firstLatency(&s.FirstReasoningMs, ms)
			}
		case "refusal":
			if part.Get("refusal").String() != "" {
				s.Refusal = true
			}
		case "":
		default:
			s.Other = true
		}
	}
	observeItem := func(item gjson.Result) {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call", "tool_call":
			s.Tool = true
			firstLatency(&s.FirstToolMs, ms)
		case "reasoning":
			s.Reasoning = true
			// Empty reasoning items do not imply visible reasoning text.
			item.Get("summary").ForEach(func(_, part gjson.Result) bool {
				if part.Get("text").String() != "" {
					firstLatency(&s.FirstReasoningMs, ms)
				}
				return true
			})
		case "message":
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				observePart(part)
				return true
			})
		case "":
		default:
			s.Other = true
		}
	}
	switch kind {
	case "response.output_text.delta", "response.output_text.done":
		if get("delta").String() != "" || get("text").String() != "" {
			s.Text = true
			firstLatency(&s.FirstTextMs, ms)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		if get("delta").String() != "" || get("text").String() != "" {
			s.Reasoning = true
			firstLatency(&s.FirstReasoningMs, ms)
		}
	case "response.refusal.delta", "response.refusal.done":
		if get("delta").String() != "" || get("refusal").String() != "" {
			s.Refusal = true
		}
	case "response.output_item.added", "response.output_item.done":
		observeItem(get("item"))
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		observePart(get("part"))
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		s.Tool = true
		firstLatency(&s.FirstToolMs, ms)
	case "response.completed", "response.failed", "response.cancelled", "response.incomplete", "error":
		firstLatency(&s.TerminalMs, ms)
		s.Terminal = kind
		get("response.output").ForEach(func(_, item gjson.Result) bool { observeItem(item); return true })
	}
}

func observeUpstreamLatency(ctx context.Context, kind string, raw []byte) {
	if latencyObservation(ctx) != nil {
		observeLatencyEvent(ctx, true, kind, func(path string) gjson.Result { return gjson.GetBytes(raw, path) })
	}
}

// ObserveClientLatencyLine is called only after a successful stream write. This
// is not socket delivery, client rendering, or a measurement of Flush latency.
func ObserveClientLatencyLine(ctx context.Context, line string) {
	if latencyObservation(ctx) == nil || !strings.HasPrefix(line, "data: ") {
		return
	}
	raw := strings.TrimPrefix(line, "data: ")
	if raw == "[DONE]" || !gjson.Valid(raw) {
		return
	}
	observeLatencyEvent(ctx, false, gjson.Get(raw, "type").String(), func(path string) gjson.Result { return gjson.Get(raw, path) })
}

// Non-stream output is not counted as streaming text/tool delivery.
func ObserveJSONReturn(ctx context.Context) {
	if o := latencyObservation(ctx); o != nil {
		o.mu.Lock()
		defer o.mu.Unlock()
		firstLatency(&o.value.JSONReturnMs, o.now().Sub(o.start).Milliseconds())
	}
}

func observeAttachmentHit(ctx context.Context) {
	if o := latencyObservation(ctx); o != nil {
		o.mu.Lock()
		o.value.Attachments.CacheHits++
		o.mu.Unlock()
	}
}

func observeAttachmentWait(ctx context.Context) func() {
	o := latencyObservation(ctx)
	if o == nil {
		return func() {}
	}
	start := time.Now()
	o.mu.Lock()
	o.value.Attachments.InFlightWaits++
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		o.value.Attachments.InFlightWaitMs += time.Since(start).Milliseconds()
		o.mu.Unlock()
	}
}

func observeAttachmentUpload(ctx context.Context, size int64) func(error) {
	o := latencyObservation(ctx)
	if o == nil {
		return func(error) {}
	}
	start := time.Now()
	o.mu.Lock()
	o.value.Attachments.UploadAttempts++
	o.value.Attachments.UploadBytes += size
	o.mu.Unlock()
	return func(err error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.value.Attachments.UploadMs += time.Since(start).Milliseconds()
		if err != nil {
			o.value.Attachments.UploadFailures++
		}
	}
}
