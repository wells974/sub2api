package basispoints

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func onDemandSource() object {
	source := testSource()
	source["stream"] = true
	tools := []any{object{"type": "namespace", "name": "mcp__node_repl", "tools": []any{object{
		"type": "function", "name": "js", "description": "Run JavaScript in Node. Use nodeRepl.write(value) to print text.",
		"parameters": object{"type": "object", "properties": object{"code": object{"type": "string"}}, "required": []any{"code"}, "additionalProperties": false},
	}}}}
	for i := 0; i < 24; i++ {
		tools = append(tools, object{"type": "function", "name": fmt.Sprintf("optional_%02d", i), "description": strings.Repeat("Read the full policy before using this capability. ", 10), "parameters": object{"type": "object", "properties": object{"count": object{"type": "integer", "minimum": 1}}, "required": []any{"count"}, "additionalProperties": false}})
	}
	source["tools"] = tools
	return source
}

func prepareOnDemand(t *testing.T, source object, replay *ReplayCache, enabled bool) ([]byte, *Bridge, OnDemandCatalogObservation) {
	t.Helper()
	raw, _ := json.Marshal(source)
	before := string(raw)
	prepared, b, err := Prepare(raw, "lazy", replay)
	if err != nil {
		t.Fatal(err)
	}
	modified, observation, err := b.ApplyOnDemandCatalog(prepared, raw, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != before || (!enabled && !bytes.Equal(prepared, modified)) {
		t.Fatal("modified source or changed disabled behavior")
	}
	return modified, b, observation
}

func disclosureResult(t *testing.T, client object) object {
	t.Helper()
	payload, ok := catalogPrintedPayload(client, "mcp__node_repl.js")
	if !ok {
		t.Fatal("not a literal documentation call", client)
	}
	return object{"type": "function_call_output", "call_id": client["call_id"], "output": []any{object{"type": "input_text", "text": "Script completed\n" + payload}}}
}

func catalogPrologue(t *testing.T, raw []byte) string {
	t.Helper()
	var wire object
	if err := decode(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, item := range wire["input"].([]any) {
		m, _ := item.(object)
		if text(m["role"]) == "developer" {
			for _, raw := range m["content"].([]any) {
				p, _ := raw.(object)
				if strings.Contains(text(p["text"]), "Client tool catalog:") {
					return text(p["text"])
				}
			}
		}
	}
	t.Fatal("missing catalog prologue")
	return ""
}

func TestOnDemandCatalogFirstUseHistoryAndSchema(t *testing.T) {
	source := onDemandSource()
	replay := new(ReplayCache)
	prepared, b, stats := prepareOnDemand(t, source, replay, true)
	if !stats.Active || stats.EagerCount != 1 || stats.DeferredCount != 24 || len(b.tools) != 25 || stats.PromptBytes >= stats.FullPromptBytes || b.Observations().Catalog.PromptBytes != stats.PromptBytes {
		t.Fatal("catalog not reduced while preserving contracts", stats)
	}
	// Even an attempted call with invalid arguments first obtains documentation.
	native := schemaCall("first", "optional_00", object{"count": "invalid"})
	response := schemaResponse(native)
	if err := b.translateResponse(response); err != nil {
		t.Fatal(err)
	}
	client := response["output"].([]any)[0].(object)
	if client["name"] != "js" || client["namespace"] != "mcp__node_repl" || client["call_id"] != native["call_id"] || quoted(replay.get("lazy", text(native["call_id"]))) != quoted(native) {
		t.Fatal("not replaced by documentation with original identity/replay", client)
	}
	result := disclosureResult(t, client)
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("replay=%t", cached), func(t *testing.T) {
			cache := replay
			if !cached {
				cache = new(ReplayCache)
			}
			source["input"] = []any{message("user", "read a tool"), client, result}
			next, second, observation := prepareOnDemand(t, source, cache, true)
			if !observation.Active || observation.DisclosedCount != 1 || catalogPrologue(t, next) != catalogPrologue(t, prepared) {
				t.Fatal("disclosure changed stable prefix or was lost", observation)
			}
			valid, err := second.translateCall(schemaCall("action", "optional_00", object{"count": 2}))
			if err != nil || valid["name"] != "optional_00" {
				t.Fatal("disclosed action not forwarded", valid, err)
			}
			if _, err := second.translateCall(schemaCall("invalid", "optional_00", object{"count": "wrong"})); err == nil {
				t.Fatal("original schema validation was bypassed")
			}
			other, err := second.translateCall(schemaCall("other", "optional_01", object{"count": 2}))
			if err != nil || other["name"] != "js" {
				t.Fatal("unrelated definition wrongly disclosed", err)
			}
		})
	}
}

func TestOnDemandCatalogEligibilityAndFallback(t *testing.T) {
	for _, scenario := range []string{"disabled", "small", "missing", "semantics", "required_metadata", "collision", "all_eager", "oversized", "none", "no_savings"} {
		t.Run(scenario, func(t *testing.T) {
			source := onDemandSource()
			tools := source["tools"].([]any)
			carrier := tools[0].(object)["tools"].([]any)[0].(object)
			switch scenario {
			case "small":
				source["tools"] = tools[:20]
			case "missing":
				source["tools"] = tools[1:]
			case "semantics":
				carrier["description"] = "Execute arbitrary Python code"
			case "required_metadata":
				carrier["parameters"].(object)["required"] = []any{"code", "title"}
				carrier["parameters"].(object)["properties"].(object)["title"] = object{"type": "string"}
			case "collision":
				tools[1].(object)["name"] = catalogLookupName
			case "all_eager":
				for _, tool := range tools[1:] {
					tool.(object)["defer_loading"] = false
				}
			case "oversized":
				carrier["parameters"].(object)["properties"].(object)["code"].(object)["maxLength"] = 40
			case "none":
				source["tool_choice"] = "none"
			case "no_savings":
				for i, tool := range tools[1:] {
					delete(tool.(object), "description")
					tool.(object)["parameters"] = object{"type": "object"}
					if i > 0 {
						tool.(object)["defer_loading"] = false
					}
				}
			}
			raw, _ := json.Marshal(source)
			prepared, b, err := Prepare(raw, "lazy", nil)
			if err != nil {
				t.Fatal(err)
			}
			after, stats, err := b.ApplyOnDemandCatalog(prepared, raw, scenario != "disabled")
			if err != nil || stats.Active || !bytes.Equal(prepared, after) {
				t.Fatal("fallback changed request", stats, err)
			}
		})
	}
}

func TestOnDemandCatalogDisclosureFailureAndVersions(t *testing.T) {
	source := onDemandSource()
	_, b, _ := prepareOnDemand(t, source, nil, true)
	client, err := b.translateCall(schemaCall("first", "optional_00", object{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"description", "schema", "preference", "truncated", "failed", "denied", "missing_result", "missing_call", "out_of_order", "duplicate_result", "compacted", "digest_only", "mcp_output"} {
		t.Run(scenario, func(t *testing.T) {
			source := onDemandSource()
			result := disclosureResult(t, client)
			history := []any{message("user", "continue"), client, result}
			changed := source["tools"].([]any)[1].(object)
			switch scenario {
			case "description":
				changed["description"] = "New policy: obtain approval first"
			case "schema":
				changed["parameters"].(object)["properties"].(object)["count"].(object)["maximum"] = 3
			case "preference":
				changed["defer_loading"] = true
			case "truncated":
				result["output"] = catalogDisclosureStart + "partial..."
			case "failed":
				result["is_error"] = true
			case "denied":
				result["output"] = "User denied this tool call"
			case "missing_result":
				history = history[:2]
			case "missing_call":
				// Prepare can recover this call from the replay cache, but that is
				// not sufficient to assert the documentation is in client history.
				history = []any{message("user", "continue"), result}
			case "out_of_order":
				history = []any{result, client}
			case "duplicate_result":
				history = append(history, result)
			case "compacted":
				history = []any{message("user", "Summary says a tool was loaded")}
			case "digest_only":
				result["output"] = b.catalogDocumentation("optional_00")["version"]
			case "mcp_output":
				payload, _ := catalogPrintedPayload(client, "mcp__node_repl.js")
				result["output"] = quoted(object{"content": []any{object{"type": "text", "text": payload}}, "isError": false})
			}
			source["input"] = history
			cache := new(ReplayCache)
			wrapped, err := b.rebuildNativeHistoryCall(client)
			if err != nil {
				t.Fatal(err)
			}
			cache.put("lazy", text(client["call_id"]), wrapped, client)
			_, next, stats := prepareOnDemand(t, source, cache, true)
			switch scenario {
			case "description", "schema", "preference", "compacted":
				if !stats.Active || stats.DisclosedCount != 0 || !next.needsCatalogDisclosure(next.tools["optional_00"]) {
					t.Fatal("stale disclosure authorized tool", stats)
				}
			case "mcp_output":
				if !stats.Active || stats.DisclosedCount != 1 {
					t.Fatal("complete MCP output not recognized", stats)
				}
			default:
				if stats.Active {
					t.Fatal("failed/incomplete disclosure did not restore full catalog", stats)
				}
			}
		})
	}
}

func TestOnDemandCatalogLookupTransportsAndAtomicity(t *testing.T) {
	source := onDemandSource()
	source["parallel_tool_calls"] = true
	tools := source["tools"].([]any)
	tools[2] = object{"type": "custom", "name": "optional_custom", "description": "Custom policy", "format": object{"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}}
	tools[3].(object)["parameters"] = object{"type": "object", "properties": object{"code": object{"type": "string"}}, "required": []any{"code"}}
	tools[4].(object)["defer_loading"] = false
	replay := new(ReplayCache)
	_, b, stats := prepareOnDemand(t, source, replay, true)
	if stats.EagerCount != 2 {
		t.Fatal("defer_loading=false not honored")
	}
	for _, names := range [][]any{{"optional_00", "optional_custom"}, {"not_declared"}, {"optional_00", "optional_00"}, {}, {"optional_00", "optional_01", "optional_02", "optional_03", "optional_04"}} {
		call := schemaCall("lookup", catalogLookupName, object{"names": names})
		result, err := b.translateCall(call)
		if len(names) == 2 && names[1] == "optional_custom" {
			if err != nil || result["name"] != "js" || !strings.Contains(text(result["arguments"]), "Custom policy") {
				t.Fatal("lookup lost full custom contract", result, err)
			}
		} else if err == nil {
			t.Fatal("invalid metadata lookup accepted", names)
		}
	}
	direct := object{"type": "function_call", "id": "fc_direct", "call_id": "call_direct", "name": "optional_00", "arguments": `{"count":2}`}
	custom := object{"type": "custom_tool_call", "id": "ctc_custom", "call_id": "call_custom", "name": "optional_custom", "input": "never execute this"}
	raw := schemaCall("raw", "optional_02", object{"code": "dangerous()"})
	raw["arguments"] = quoted(object{"summary": functionCodeTransportPrefix + "optional_02", "code": "dangerous()", "extended_summary": "{}"})
	for _, call := range []object{direct, custom, raw} {
		result, err := b.translateCall(call)
		if err != nil || result["name"] != "js" || strings.Contains(text(result["arguments"]), "dangerous()") || strings.Contains(text(result["arguments"]), "never execute this") {
			t.Fatal("undisclosed transport escaped gate", result, err)
		}
	}
	response := schemaResponse(schemaCall("pending", "optional_00", object{}), schemaCall("bad_eager", "optional_03", object{"count": "invalid"}))
	before := quoted(response)
	if err := b.translateResponse(response); err == nil || quoted(response) != before || replay.get("lazy", "call_pending") != nil {
		t.Fatal("invalid mixed batch wrote a partial response or replay")
	}
}

func TestOnDemandCatalogLiteralSafetyAndStream(t *testing.T) {
	source := onDemandSource()
	policy := "\");globalThis.executed = true; // ` ${process.env.SECRET} \n \u2028 </script>"
	source["tools"].([]any)[1].(object)["description"] = policy
	_, b, _ := prepareOnDemand(t, source, new(ReplayCache), true)
	response := schemaResponse(schemaCall("safe", "optional_00", object{"count": 1}))
	repairs := 0
	out, err := readSchemaStream(t, b, WithSchemaRepair(context.Background(), time.Second), "", response, func(context.Context, object, error) (object, error) {
		repairs++
		return nil, fmt.Errorf("must not repair a metadata disclosure")
	})
	if err != nil || repairs != 0 || bytes.Contains(out, []byte("response.failed")) || bytes.Count(out, []byte("event: response.function_call_arguments.done")) != 1 {
		t.Fatal("stream did not deliver one metadata call", string(out), err)
	}
	printed, err := b.translateCall(schemaCall("literal", "optional_00", object{}))
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := catalogPrintedPayload(printed, "mcp__node_repl.js")
	if !ok || !strings.Contains(payload, quoted(policy)) {
		t.Fatal("literal policy not preserved")
	}
	if node, err := exec.LookPath("node"); err == nil {
		var args object
		_ = decode([]byte(text(printed["arguments"])), &args)
		program := "globalThis.executed = false; const nodeRepl = {write: s => process.stdout.write(s)};" + text(args["code"]) + "; if (globalThis.executed) process.exit(9);"
		got, err := exec.Command(node, "-e", program).Output()
		if err != nil || string(got) != payload {
			t.Fatal("metadata escaped JavaScript string literal", err)
		}
	}
	// The same output path still works for ordinary EOF and preserves usage.
	stream := b.Stream(io.NopCloser(strings.NewReader(sse(object{"type": "response.completed", "response": schemaResponse(schemaCall("again", catalogLookupName, object{"names": []any{"optional_00"}}))}))))
	data, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || !bytes.Contains(data, []byte(`"input_tokens":10`)) {
		t.Fatal("usage lost during disclosure", err)
	}
}

func TestOnDemandCatalogInheritanceReprepareAndEviction(t *testing.T) {
	source := onDemandSource()
	raw, _ := json.Marshal(source)
	cache := new(CatalogCache)
	replay := new(ReplayCache)
	wire, first, err := PrepareWithCatalog(raw, "lazy", replay, cache)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err := first.ApplyOnDemandCatalog(wire, raw, true)
	if err != nil || !stats.Active {
		t.Fatal(stats, err)
	}
	client, err := first.translateCall(schemaCall("load", "optional_00", object{}))
	if err != nil {
		t.Fatal(err)
	}
	source["input"] = []any{message("user", "use it"), client, disclosureResult(t, client)}
	delete(source, "tools")
	raw, _ = json.Marshal(source)
	wire, inherited, err := PrepareWithCatalog(raw, "lazy", replay, cache)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err = inherited.ApplyOnDemandCatalog(wire, raw, true)
	if err != nil || !stats.Active || stats.DisclosedCount != 1 {
		t.Fatal("inherited definitions lost disclosure", stats, err)
	}
	// Reprepare is used after upload/compaction. Re-evaluate disclosure from the
	// actual continuation history, rather than retaining a stale active state.
	source["input"] = []any{message("user", "compacted summary without tool outputs")}
	raw, _ = json.Marshal(source)
	wire, compacted, err := inherited.Reprepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err = compacted.ApplyOnDemandCatalog(wire, raw, true)
	if err != nil || !stats.Active || stats.DisclosedCount != 0 {
		t.Fatal("compaction retained stale disclosure", stats, err)
	}
	// Eviction cannot invent tools: callers omitting a now-unavailable catalog
	// retain the original empty-catalog behavior. Explicit declarations recover.
	wire, empty, err := PrepareWithCatalog(raw, "lazy", replay, new(CatalogCache))
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err = empty.ApplyOnDemandCatalog(wire, raw, true)
	if err != nil || stats.Active || len(empty.tools) != 0 {
		t.Fatal("lost catalog was resurrected from disclosure", stats, err)
	}
	source["tools"] = onDemandSource()["tools"]
	_, _, stats = prepareOnDemand(t, source, new(ReplayCache), true)
	if !stats.Active || stats.DisclosedCount != 0 {
		t.Fatal("explicit catalog did not recover", stats)
	}
}
