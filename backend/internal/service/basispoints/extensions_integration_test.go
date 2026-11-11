package basispoints

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type cancellationOrderBody struct {
	reading, closing, release chan struct{}
	readOnce, closeOnce       sync.Once
}

func (b *cancellationOrderBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.reading) })
	<-b.closing
	return 0, io.ErrClosedPipe
}

func (b *cancellationOrderBody) Close() error {
	b.closeOnce.Do(func() { close(b.closing) })
	<-b.release
	return nil
}

func TestCompactedWindowCancellationPrecedesUpstreamClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &cancellationOrderBody{reading: make(chan struct{}), closing: make(chan struct{}), release: make(chan struct{})}
	stream := WithCompactedWindow(ctx, upstream, nil, nil)
	t.Cleanup(func() { close(upstream.release); _ = stream.Close() })
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(stream); result <- err }()
	select {
	case <-upstream.reading:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("upstream closure replaced client cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited on slow upstream close")
	}
}

func TestSchemaRepairDoesNotCertifyUndisclosedArguments(t *testing.T) {
	for _, count := range []any{2, "invalid"} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			source := onDemandSource()
			source["parallel_tool_calls"] = true
			source["tools"].([]any)[1].(object)["defer_loading"] = false
			replay := new(ReplayCache)
			_, b, stats := prepareOnDemand(t, source, replay, true)
			if !stats.Active {
				t.Fatal(stats)
			}
			ctx := WithSchemaRepair(context.Background(), time.Second)
			response := schemaResponse(schemaCall("bad", "optional_00", object{"count": "invalid"}), schemaCall("hidden", "optional_01", object{"count": count}))
			attempts := 0
			out, _ := readSchemaStream(t, b, ctx, "", response, func(context.Context, object, error) (object, error) {
				attempts++
				return nil, fmt.Errorf("must not certify or repair an undisclosed action")
			})
			observation, _ := SchemaRepairSnapshot(ctx)
			if attempts != 0 || observation.SkipReason != "undisclosed_tool" || bytes.Contains(out, []byte("event: response.function_call_arguments")) || replay.get("lazy", "call_hidden") != nil {
				t.Fatalf("mixed batch repaired/dispatched before disclosure: attempts=%d observation=%+v", attempts, observation)
			}
		})
	}
}

func TestSchemaRepairWithDisclosedCatalogRetainsFullValidation(t *testing.T) {
	source := onDemandSource()
	_, first, _ := prepareOnDemand(t, source, nil, true)
	print, err := first.translateCall(schemaCall("docs", "optional_00", object{}))
	if err != nil {
		t.Fatal(err)
	}
	source["input"] = []any{message("user", "read documentation"), print, disclosureResult(t, print)}
	_, b, stats := prepareOnDemand(t, source, new(ReplayCache), true)
	if stats.DisclosedCount != 1 {
		t.Fatal(stats)
	}
	ctx := WithSchemaRepair(context.Background(), time.Second)
	attempts := 0
	out, err := readSchemaStream(t, b, ctx, "", schemaResponse(schemaCall("invalid", "optional_00", object{"count": "wrong"})), func(context.Context, object, error) (object, error) {
		attempts++
		return schemaResponse(schemaCall("fixed", "optional_00", object{"count": 3})), nil
	})
	observation, _ := SchemaRepairSnapshot(ctx)
	if err != nil || attempts != 1 || observation.Outcome != "succeeded" || !bytes.Contains(out, []byte(`"name":"optional_00"`)) || !bytes.Contains(out, []byte(`"input_tokens":20`)) {
		t.Fatalf("disclosed tool/schema interaction failed: %v %+v %s", err, observation, out)
	}
}
