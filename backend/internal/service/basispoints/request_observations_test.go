package basispoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestImageObservationsCountOccurrencesWithoutMutation(t *testing.T) {
	image := func(url string) object { return object{"type": "input_image", "image_url": url} }
	source := object{"input": []any{
		object{"role": "user", "content": []any{image("data:image/png;base64,AQID"), image("data:image/png;base64,AQID"), image("https://private.invalid/secret"), object{"type": "input_image", "file_id": "secret-id"}}},
		object{"type": "agent_message", "content": []any{image("DATA:image/png;BASE64,YQ==\r\n")}},
		object{"type": "function_call_output", "output": []any{image("data:image/png;base64,YWI=")}},
		object{"type": "custom_tool_call_output", "output": []any{image("data:image/png;base64,??"), image("data:image/png;base64,YQ=="), image("data:image/png,unknown")}},
		object{"type": "function_call", "arguments": object{"content": []any{image("data:image/png;base64,AQID")}}},
		object{"type": "message", "content": "data:image/png;base64,AQID"},
	}}
	raw, _ := json.Marshal(source)
	before := bytes.Clone(raw)
	got := ObserveRequestImages(raw)
	want := ImageObservation{ValidJSON: true, InlineCount: 7, InlineBytes: 10, MaxInlineBytes: 3, MessageBytes: 7, ToolOutputBytes: 3, UnknownBytes: 2, URLCount: 1, FileCount: 1}
	if got != want || !bytes.Equal(raw, before) {
		t.Fatalf("unexpected statistics or mutation: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "AQID") {
		t.Fatal("image data leaked")
	}
	if got := ObserveRequestImages([]byte(`{"input":[`)); got != (ImageObservation{}) {
		t.Fatalf("invalid JSON reported as measured: %+v", got)
	}
}

func TestInlineDecodedLengthMatchesDecoder(t *testing.T) {
	for size := 1; size <= 100; size++ {
		payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, size))
		payload = payload[:2] + "\r\n" + payload[2:] + "\n"
		got, ok := inlineDecodedLength("data:image/png;base64," + payload)
		if !ok || got != int64(size) {
			t.Fatalf("size %d: %d %t", size, got, ok)
		}
	}
	for _, payload := range []string{"", "YQ", "YQ=", "YQ===", "Y Q==", "YQ==AA==", "====", "_Q==", "💡"} {
		if _, ok := inlineDecodedLength("data:image/png;base64," + payload); ok {
			t.Fatalf("invalid payload accepted: %q", payload)
		}
	}
}

func TestImageObservationsContinuePastExistingLimits(t *testing.T) {
	// A syntactically valid payload need not be a valid image to measure bytes.
	// Repeated references must count even when upload deduplication is possible.
	payload := base64.StdEncoding.EncodeToString(make([]byte, 17<<20))
	raw := []byte(`{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,` + payload + `"},{"type":"input_image","image_url":"data:image/png;base64,` + payload + `"}]}]}`)
	got := ObserveRequestImages(raw)
	if got.InlineCount != 2 || got.InlineBytes != 34<<20 || got.MaxInlineBytes != 17<<20 || got.UnknownBytes != 0 {
		t.Fatalf("statistics stopped at the existing 32 MiB limit: %+v", got)
	}
}

func TestArgumentObservationsPreserveErrorsAndRedactValues(t *testing.T) {
	for _, tc := range []struct {
		name, schema, args, path, keyword, reason string
	}{
		{"required", `{"type":"object","required":["count"]}`, `{}`, "", "required", "schema_validation"},
		{"type", `{"properties":{"count":{"type":"integer"}}}`, `{"count":"PRIVATE_VALUE"}`, "/count", "type", "schema_validation"},
		{"enum", `{"properties":{"mode":{"enum":["PRIVATE_ENUM"]}}}`, `{"mode":"PRIVATE_VALUE"}`, "/mode", "enum", "schema_validation"},
		{"additional", `{"additionalProperties":false}`, `{"PRIVATE_KEY":"PRIVATE_VALUE"}`, "", "additionalProperties", "schema_validation"},
		{"dynamic", `{"additionalProperties":{"type":"integer"}}`, `{"PRIVATE_KEY":"PRIVATE_VALUE"}`, "/[redacted]", "type", "schema_validation"},
		{"nested", `{"properties":{"options":{"properties":{"count":{"type":"integer"}}}}}`, `{"options":{"count":"PRIVATE_VALUE"}}`, "/options/count", "type", "schema_validation"},
		{"resource", `{"type":"object"}`, `{"count":1e99999}`, "", "", "resource_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema object
			if err := decode([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			source := testSource()
			source["tools"] = []any{object{"type": "namespace", "name": "functions", "tools": []any{object{"type": "function", "name": "check", "parameters": schema}}}}
			_, bridge := mustPrepare(t, source, "observations", nil)
			_, err := bridge.translateCall(nativeCall(object{"name": "functions.check", "arguments": tc.args}))
			var schemaErr toolArgumentsSchemaError
			if !errors.As(err, &schemaErr) || err.Error() != (toolArgumentsSchemaError{}).Error() || errors.Unwrap(schemaErr) != nil {
				t.Fatalf("error contract changed: %v", err)
			}
			bridge.observeTerminalFailure(fmt.Errorf("wrapped: %w", err))
			got := bridge.Observations().ArgumentFailure
			if got == nil || got.Tool != "functions.check" || got.Path != tc.path || got.Keyword != tc.keyword || got.Reason != tc.reason {
				t.Fatalf("unexpected diagnostic: %+v", got)
			}
			if tc.name == "required" && !reflect.DeepEqual(got.Missing, []string{"count"}) {
				t.Fatal(got)
			}
			if tc.name == "type" && (got.ActualType != "string" || !reflect.DeepEqual(got.ExpectedTypes, []string{"integer"})) {
				t.Fatal(got)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatalf("private data leaked: %s", encoded)
			}
			got.Tool = "mutated"
			if len(got.Missing) > 0 {
				got.Missing[0] = "mutated"
			}
			if strings.Contains(quoted(bridge.Observations()), "mutated") {
				t.Fatal("snapshot aliases observer")
			}
		})
	}
}

func TestArgumentObservationDoesNotChangeStreamOrRetry(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "shell", "parameters": object{"required": []any{"cmd"}}}}
	_, bridge := mustPrepare(t, source, "observations", nil)
	response := object{"id": "resp_test", "status": "completed", "usage": object{"input_tokens": 11, "output_tokens": 3}, "output": []any{nativeCall(object{"name": "shell", "arguments": object{}})}}
	wire := sse(object{"type": "response.completed", "response": response})
	stream := func(b *Bridge) []byte {
		body := b.StreamWithRepair(context.Background(), io.NopCloser(strings.NewReader(wire)), func(context.Context) (io.ReadCloser, error) {
			t.Error("schema failure retried")
			return nil, errors.New("unexpected retry")
		})
		out, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// A nil observer is the pre-observation stream path. It must emit identical
	// bytes, including failure metadata, usage, and native IDs.
	unobserved := *bridge
	unobserved.observations = nil
	want := stream(&unobserved)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = bridge.Observations()
		}
	}()
	got := stream(bridge)
	wg.Wait()
	if !bytes.Equal(got, want) || bridge.Observations().ArgumentFailure == nil {
		t.Fatalf("stream changed or failure missing: %s", got)
	}
	if !bytes.Contains(got, []byte(`"input_tokens":11`)) || !bytes.Contains(got, []byte("response.failed")) || bytes.Contains(got, []byte("schema_failure")) {
		t.Fatalf("wire/usage contract changed: %s", got)
	}
}

func TestCatalogObservationsUseEffectiveRenderedCatalog(t *testing.T) {
	source := testSource()
	tool := catalogReadTool("string")
	tool["description"] = "读取文件：UTF-8 bytes"
	source["tools"] = []any{tool}
	cache := new(CatalogCache)
	first := prepareCatalogTest(t, cache, source, "observations")
	want := first.Observations().Catalog
	if want.ToolCount != 1 || want.SchemaBytes != len(quoted(tool["parameters"])) || want.MaxSchemaBytes != want.SchemaBytes || want.PromptBytes != len(describeCatalog([]any{tool})) {
		t.Fatalf("wrong catalog byte metrics: %+v", want)
	}
	delete(source, "tools")
	bridge := prepareCatalogTest(t, cache, source, "observations")
	if bridge.Observations().Catalog != want {
		t.Fatal("inherited catalog was not measured")
	}
	raw, _ := json.Marshal(source)
	_, prepared, err := bridge.Reprepare(raw)
	if err != nil || prepared.Observations().Catalog != want {
		t.Fatalf("reprepare: %v %+v", err, prepared.Observations())
	}
	source["tool_choice"] = "none"
	if got := prepareCatalogTest(t, cache, source, "observations").Observations().Catalog; got != (CatalogObservation{}) {
		t.Fatalf("inactive tools counted: %+v", got)
	}
}

func BenchmarkObserveRequestImages(b *testing.B) {
	raw := []byte(`{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(make([]byte, 1<<20)) + `"}]}]}`)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ObserveRequestImages(raw)
	}
}
