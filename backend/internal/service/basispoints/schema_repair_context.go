package basispoints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type schemaRepairContextKey struct{}

type SchemaRepairObservation struct {
	Considered       bool   `json:"considered"`
	Attempted        bool   `json:"attempted"`
	Outcome          string `json:"outcome,omitempty"`
	SkipReason       string `json:"skip_reason,omitempty"`
	TotalCorrections int    `json:"total_corrections"`
}

type schemaRepairSession struct {
	mu          sync.Mutex
	timeout     time.Duration
	started     bool
	done        chan struct{}
	doneOnce    sync.Once
	observation SchemaRepairObservation
	usage       object
	blocked     string
}

// WithSchemaRepair enables one schema correction for one forwarding attempt.
// Normal callers keep the existing behavior by not adding this context option.
func WithSchemaRepair(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithValue(ctx, schemaRepairContextKey{}, &schemaRepairSession{timeout: timeout, done: make(chan struct{})})
}

func schemaSession(ctx context.Context) *schemaRepairSession {
	s, _ := ctx.Value(schemaRepairContextKey{}).(*schemaRepairSession)
	return s
}

// A compaction wrapper can emit retained messages outside the bridge's own
// emitter. Conservatively disable schema correction for such streaming windows.
func GuardSchemaRepairCompactedWindow(ctx context.Context, window []any) {
	s := schemaSession(ctx)
	if s == nil {
		return
	}
	for _, raw := range window {
		item, _ := raw.(object)
		if hasVisibleStreamContent("response.output_item.done", object{"item": item}) {
			s.mu.Lock()
			s.blocked = "compaction_visible_window"
			s.mu.Unlock()
			return
		}
	}
}

// All correction callbacks share the pre-existing maximum of two submissions.
// Schema correction is a separate, single-shot branch, not an inner retry loop.
func schemaRepairCallbacks(ctx context.Context, repair ToolRepairFunc, unknown RepairToolCall) (ToolRepairFunc, RepairToolCall) {
	s := schemaSession(ctx)
	if s == nil {
		return repair, unknown
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	claim := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.observation.TotalCorrections >= maxToolRepairs {
			return fmt.Errorf("basispoints correction budget exhausted")
		}
		s.observation.TotalCorrections++
		return nil
	}
	var wrapped ToolRepairFunc
	if repair != nil {
		wrapped = func(ctx context.Context, failed object, validation error) (object, error) {
			if err := claim(ctx); err != nil {
				return nil, err
			}
			return repair(ctx, failed, validation)
		}
	}
	var regenerated RepairToolCall
	if unknown != nil {
		regenerated = func(ctx context.Context) (io.ReadCloser, error) {
			if err := claim(ctx); err != nil {
				return nil, err
			}
			return unknown(ctx)
		}
	}
	return wrapped, regenerated
}

func finishSchemaRepairStream(ctx context.Context) {
	if s := schemaSession(ctx); s != nil {
		s.doneOnce.Do(func() { close(s.done) })
	}
}

func (s *schemaRepairSession) record(attempted bool, outcome, skip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observation.Considered = true
	s.observation.Attempted = attempted
	s.observation.Outcome = outcome
	s.observation.SkipReason = skip
}
func (s *schemaRepairSession) captureUsage(usage any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = addRepairUsage(nil, usage)
}

// SchemaRepairSnapshot waits only for cancellation cleanup, never generation.
// The bounded wait lets the gateway preserve usage already reported by a repair
// even if the downstream pipe was closed before its terminal event was emitted.
func SchemaRepairSnapshot(ctx context.Context) (SchemaRepairObservation, []byte) {
	s := schemaSession(ctx)
	if s == nil {
		return SchemaRepairObservation{}, nil
	}
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var usage []byte
	if s.usage != nil {
		usage, _ = json.Marshal(object{"type": "response.completed", "response": object{"usage": s.usage}})
	}
	return s.observation, usage
}
