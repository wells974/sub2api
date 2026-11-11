package basispoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func schemaRepairSource() object {
	source := testSource()
	source["stream"] = true
	source["tools"] = []any{object{"type": "function", "name": "lookup", "parameters": object{"type": "object", "properties": object{"count": object{"type": "integer", "minimum": 1}}, "required": []any{"count"}, "additionalProperties": false}}, object{"type": "function", "name": "other", "parameters": object{"type": "object"}}}
	return source
}
func schemaCall(id, target string, args any) object {
	item := nativeCall(object{"name": target, "arguments": args})
	item["id"] = "fc_" + id
	item["call_id"] = "call_" + id
	return item
}
func schemaResponse(items ...object) object {
	output := make([]any, len(items))
	for i, item := range items {
		output[i] = item
	}
	return object{"id": "resp_original", "status": "completed", "output": output, "usage": object{"input_tokens": 10, "output_tokens": 2}}
}
func readSchemaStream(t *testing.T, b *Bridge, ctx context.Context, prefix string, response object, repair ToolRepairFunc) ([]byte, error) {
	t.Helper()
	body := b.StreamWithToolRepair(ctx, io.NopCloser(strings.NewReader(prefix+sse(object{"type": "response.completed", "response": response}))), repair)
	out, err := io.ReadAll(body)
	_ = body.Close()
	return out, err
}

func TestSchemaRepairSuccessAtomicBatchAndFeedback(t *testing.T) {
	source := schemaRepairSource()
	source["parallel_tool_calls"] = true
	raw, _ := json.Marshal(source)
	prepared, b, err := Prepare(raw, "schema", new(ReplayCache))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSchemaRepair(context.Background(), time.Second)
	original := schemaResponse(schemaCall("bad", "lookup", object{"count": "PRIVATE_VALUE"}), schemaCall("valid", "lookup", object{"count": 2}))
	before := quoted(original)
	calls := 0
	out, err := readSchemaStream(t, b, ctx, "", original, func(ctx context.Context, failed object, validation error) (object, error) {
		calls++
		if b.replay.get("schema", "call_bad") != nil || b.replay.get("schema", "call_valid") != nil {
			t.Fatal("batch committed before correction")
		}
		request, err := BuildToolRepairRequest(prepared, failed, validation)
		if err != nil {
			t.Fatal(err)
		}
		var feedback object
		_ = decode(request, &feedback)
		input, _ := feedback["input"].([]any)
		for _, entry := range input {
			item, _ := entry.(object)
			if text(item["type"]) == "function_call_output" {
				result := text(item["output"])
				if !strings.Contains(result, `"executed":false`) || strings.Contains(result, "PRIVATE_VALUE") {
					t.Fatal("unsafe/incomplete feedback", result)
				}
				if text(item["call_id"]) == "call_bad" && !strings.Contains(result, `"keyword":"type"`) {
					t.Fatal("missing invalid-call diagnostic")
				}
				if text(item["call_id"]) == "call_valid" && !strings.Contains(result, `"schema_valid":true`) {
					t.Fatal("valid call incorrectly marked invalid")
				}
			}
		}
		if !strings.Contains(string(request), "Correct its invalid JSON Schema arguments") || strings.Contains(string(request), "Correct only its transport formatting") {
			t.Fatal("wrong correction instructions")
		}
		return schemaResponse(schemaCall("fixed", "lookup", object{"count": 1}), schemaCall("kept", "lookup", object{"count": 2})), nil
	})
	if err != nil || calls != 1 || !bytes.Contains(out, []byte("response.completed")) || bytes.Contains(out, []byte("response.failed")) {
		t.Fatalf("correction failed: %v %s", err, out)
	}
	if strings.Count(string(out), "event: response.function_call_arguments.done") != 2 || !bytes.Contains(out, []byte(`"input_tokens":20`)) {
		t.Fatal("dispatch/usage contract changed", string(out))
	}
	if quoted(original) != before {
		t.Fatal("upstream source mutated")
	}
	if b.replay.get("schema", "call_bad") != nil || b.replay.get("schema", "call_valid") != nil || b.replay.get("schema", "call_fixed") == nil || b.replay.get("schema", "call_kept") == nil {
		t.Fatal("wrong replay identities")
	}
	observation, usage := SchemaRepairSnapshot(ctx)
	if !observation.Attempted || observation.Outcome != "succeeded" || observation.TotalCorrections != 1 || !bytes.Contains(usage, []byte(`"input_tokens":20`)) {
		t.Fatal(observation, string(usage))
	}
}

func TestSchemaRepairDisabledAndUnsafeBoundaries(t *testing.T) {
	for _, scenario := range []string{"disabled", "visible_text", "visible_reasoning", "visible_snapshot", "resource", "mixed_resource", "unknown", "raw_code", "raw_contract", "duplicate", "incomplete", "parallel", "missing_id"} {
		t.Run(scenario, func(t *testing.T) {
			source := schemaRepairSource()
			source["parallel_tool_calls"] = true
			response := schemaResponse(schemaCall("bad", "lookup", object{"count": "bad"}))
			prefix := ""
			switch scenario {
			case "visible_text":
				prefix = sse(object{"type": "response.output_text.delta", "delta": "already delivered"})
			case "visible_reasoning":
				prefix = sse(object{"type": "response.reasoning_summary_text.delta", "delta": "already delivered"})
			case "visible_snapshot":
				prefix = sse(object{"type": "response.in_progress", "response": object{"output": []any{message("assistant", "already delivered")}}})
			case "resource":
				response = schemaResponse(schemaCall("bad", "lookup", object{"count": json.Number("1e99999")}))
			case "mixed_resource":
				response = schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("large", "lookup", object{"count": json.Number("1e99999")}))
			case "unknown":
				response = schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("unknown", "missing", object{}))
			case "raw_code":
				response = schemaResponse(schemaCall("bad", "lookup", object{"count": "bad", "code": "PRIVATE_CODE"}))
			case "raw_contract":
				source["tools"].([]any)[0].(object)["parameters"].(object)["properties"].(object)["cmd"] = object{"type": "string"}
			case "duplicate":
				response = schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("bad", "lookup", object{}))
			case "incomplete":
				response["status"] = "incomplete"
			case "parallel":
				source["parallel_tool_calls"] = false
				response = schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("other", "lookup", object{}))
			case "missing_id":
				delete(response["output"].([]any)[0].(object), "id")
			}
			_, b := mustPrepare(t, source, "schema", nil)
			ctx := context.Background()
			if scenario != "disabled" {
				ctx = WithSchemaRepair(ctx, time.Second)
			}
			out, err := readSchemaStream(t, b, ctx, prefix, response, func(context.Context, object, error) (object, error) {
				t.Error("unsafe repair attempted")
				return nil, errors.New("unexpected")
			})
			if err != nil || !bytes.Contains(out, []byte("response.failed")) || bytes.Contains(out, []byte("event: response.function_call_arguments")) {
				t.Fatalf("unsafe dispatch: %v %s", err, out)
			}
			obs, _ := SchemaRepairSnapshot(ctx)
			if obs.Attempted {
				t.Fatal(obs)
			}
			if strings.HasPrefix(scenario, "visible_") && obs.SkipReason != "visible_content" {
				t.Fatal(obs)
			}
		})
	}
}

func TestSchemaRepairRejectsChangedOrInvalidBatch(t *testing.T) {
	for _, scenario := range []string{"invalid", "target", "extra", "valid_changed", "reused_id", "raw_added", "failed_terminal", "network"} {
		t.Run(scenario, func(t *testing.T) {
			source := schemaRepairSource()
			source["parallel_tool_calls"] = true
			_, b := mustPrepare(t, source, "schema", new(ReplayCache))
			ctx := WithSchemaRepair(context.Background(), time.Second)
			calls := 0
			out, err := readSchemaStream(t, b, ctx, "", schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("valid", "lookup", object{"count": 2})), func(context.Context, object, error) (object, error) {
				calls++
				fixed := schemaCall("fixed", "lookup", object{"count": 1})
				valid := schemaCall("kept", "lookup", object{"count": 2})
				switch scenario {
				case "invalid":
					fixed = schemaCall("fixed", "lookup", object{})
				case "target":
					fixed = schemaCall("fixed", "other", object{"count": 1})
				case "valid_changed":
					valid = schemaCall("kept", "lookup", object{"count": 3})
				case "reused_id":
					fixed = schemaCall("bad", "lookup", object{"count": 1})
				case "raw_added":
					fixed = schemaCall("fixed", "other", object{"code": "secret"})
				}
				response := schemaResponse(fixed, valid)
				if scenario == "extra" {
					response = schemaResponse(fixed, valid, schemaCall("extra", "lookup", object{"count": 1}))
				}
				if scenario == "failed_terminal" {
					response["status"] = "failed"
				}
				if scenario == "network" {
					return response, io.ErrUnexpectedEOF
				}
				return response, nil
			})
			if err != nil || calls != 1 || !bytes.Contains(out, []byte("response.failed")) || bytes.Contains(out, []byte("event: response.function_call_arguments")) || !bytes.Contains(out, []byte(`"input_tokens":20`)) {
				t.Fatalf("unsafe correction: %v %s", err, out)
			}
			if b.replay.get("schema", "call_fixed") != nil || b.replay.get("schema", "call_valid") != nil {
				t.Fatal("failed batch was cached")
			}
		})
	}
}

func TestSchemaRepairDeadlineAndCancellationKeepKnownUsage(t *testing.T) {
	for _, cancelClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "client_cancel"}[cancelClient], func(t *testing.T) {
			_, b := mustPrepare(t, schemaRepairSource(), "schema", nil)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := WithSchemaRepair(parent, 10*time.Millisecond)
			out, err := readSchemaStream(t, b, ctx, "", schemaResponse(schemaCall("bad", "lookup", object{})), func(ctx context.Context, _ object, _ error) (object, error) {
				if cancelClient {
					cancel()
				}
				<-ctx.Done()
				return object{"usage": object{"input_tokens": 4, "output_tokens": 1}}, ctx.Err()
			})
			if cancelClient {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil || !bytes.Contains(out, []byte("schema correction timed out")) || !bytes.Contains(out, []byte(`"input_tokens":14`)) {
				t.Fatal(err, string(out))
			}
			obs, usage := SchemaRepairSnapshot(ctx)
			if !obs.Attempted || !bytes.Contains(usage, []byte(`"input_tokens":14`)) {
				t.Fatal(obs, string(usage))
			}
			if bytes.Contains(out, []byte("event: response.function_call_arguments")) {
				t.Fatal("canceled tool was dispatched")
			}
		})
	}
	_, b := mustPrepare(t, schemaRepairSource(), "schema", nil)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := WithSchemaRepair(parent, time.Second)
	_, _ = readSchemaStream(t, b, ctx, "", schemaResponse(schemaCall("bad", "lookup", object{})), func(context.Context, object, error) (object, error) {
		t.Fatal("canceled request repaired")
		return nil, nil
	})
}

func TestSchemaRepairSharesSubmissionBudget(t *testing.T) {
	ctx := WithSchemaRepair(context.Background(), time.Second)
	calls := 0
	repair, unknown := schemaRepairCallbacks(ctx, func(context.Context, object, error) (object, error) { calls++; return nil, nil }, func(context.Context) (io.ReadCloser, error) { calls++; return io.NopCloser(strings.NewReader("")), nil })
	if _, err := repair(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	body, err := unknown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if _, err := repair(ctx, nil, nil); err == nil || calls != 2 {
		t.Fatal("nested corrections exceeded shared budget")
	}
	finishSchemaRepairStream(ctx)
}
