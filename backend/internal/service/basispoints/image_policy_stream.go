package basispoints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// WithCompactedWindow emits the real compacted window before the continuation
// in one Responses lifecycle. Retained items must not be pruned from the window.
func WithCompactedWindow(ctx context.Context, upstream io.ReadCloser, window []any, usage map[string]any) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = upstream.Close() }()
		// Publish cancellation before Close unblocks the upstream reader with a
		// competing closed-pipe error (or waits for slow transport cleanup).
		stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()); _ = upstream.Close() })
		defer stop()
		sequence := 0
		inserted := false
		progressiveUsage := object{}
		emit := func(kind string, p object) error {
			p["type"] = kind
			p["sequence_number"] = sequence
			sequence++
			raw, e := json.Marshal(p)
			if e != nil {
				return e
			}
			_, e = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", kind, raw)
			return e
		}
		insert := func() error {
			if inserted {
				return nil
			}
			inserted = true
			for i, v := range window {
				if e := emit("response.output_item.added", object{"output_index": i, "item": v}); e != nil {
					return e
				}
				if e := emit("response.output_item.done", object{"output_index": i, "item": v}); e != nil {
					return e
				}
			}
			return nil
		}
		err := readEvents(upstream, func(event string, data []byte) error {
			if string(data) == "[DONE]" {
				return nil
			}
			var p object
			if e := decode(data, &p); e != nil {
				return e
			}
			kind := text(p["type"])
			if kind == "" {
				kind = event
			}
			if kind != "response.created" && kind != "response.in_progress" {
				if e := insert(); e != nil {
					return e
				}
			}
			if index, ok := p["output_index"]; ok {
				switch n := index.(type) {
				case json.Number:
					i, e := n.Int64()
					if e != nil {
						return e
					}
					p["output_index"] = i + int64(len(window))
				case float64:
					p["output_index"] = n + float64(len(window))
				}
			}
			response, _ := p["response"].(object)
			observed := eventUsage(p, response)
			terminal := kind == "response.completed" || kind == "response.failed" || kind == "response.cancelled" || kind == "response.incomplete"
			if !terminal && len(observed) > 0 {
				progressiveUsage = maxReportedUsage(progressiveUsage, observed)
			}
			if terminal && response != nil {
				output, _ := response["output"].([]any)
				response["output"] = append(append([]any{}, window...), output...)
				current := observed
				if !hasBilledUsage(current) && hasBilledUsage(progressiveUsage) {
					current = progressiveUsage
				}
				response["usage"] = addRepairUsage(current, usage)
				if _, ok := p["usage"].(object); ok {
					p["usage"] = response["usage"]
				}
			}
			return emit(kind, p)
		})
		_ = writer.CloseWithError(err)
	}()
	return &imagePolicyStream{PipeReader: reader, upstream: upstream}
}

type imagePolicyStream struct {
	*io.PipeReader
	upstream io.ReadCloser
}

func (s *imagePolicyStream) Close() error { _ = s.upstream.Close(); return s.PipeReader.Close() }

// ReadImageCompaction observes reported usage even when no successful terminal
// arrives. The caller never sees intermediate text or executable tool events.
func ReadImageCompaction(reader io.Reader, observe func([]byte)) (map[string]any, error) {
	return ReadImageCompactionWithContext(context.Background(), reader, observe)
}

// ReadImageCompactionWithContext also observes private upstream failures.
func ReadImageCompactionWithContext(ctx context.Context, reader io.Reader, observe func([]byte)) (map[string]any, error) {
	var response object
	completed := false
	progressiveUsage := object{}
	err := readEvents(io.LimitReader(reader, 32<<20), func(event string, raw []byte) error {
		if string(raw) == "[DONE]" {
			return nil
		}
		var p object
		if e := decode(raw, &p); e != nil {
			return e
		}
		if observe != nil {
			observe(raw)
		}
		r, _ := p["response"].(object)
		u := eventUsage(p, r)
		kind := text(p["type"])
		if kind == "" {
			kind = event
		}
		observeRateLimit(ctx, "image_compaction", kind, p)
		item, _ := p["item"].(object)
		if isTool(item) || isToolEvent(kind) {
			return fmt.Errorf("image compaction returned an unexpected tool event")
		}
		switch kind {
		case "response.completed", "response.failed", "response.cancelled", "response.incomplete", "error":
			response = r
			if response != nil && u != nil {
				response["usage"] = u
			}
			completed = kind == "response.completed"
			return io.EOF
		}
		if len(u) > 0 {
			progressiveUsage = maxReportedUsage(progressiveUsage, u)
		}
		return nil
	})
	if response != nil {
		restoreRoundUsage(response, progressiveUsage)
	}
	if err != nil && err != io.EOF {
		return response, err
	}
	if !completed || response == nil {
		return response, fmt.Errorf("image history compaction did not complete")
	}
	return response, nil
}
