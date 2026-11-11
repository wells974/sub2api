package basispoints

import (
	"fmt"
	"strings"
)

const catalogDisclosureStart = "SUB2API_TOOL_CATALOG_V1\n"
const catalogDisclosureEnd = "\nEND_SUB2API_TOOL_CATALOG_V1"

func (b *Bridge) catalogDocumentation(name string) object {
	info := b.tools[name]
	// Unlike toolDefinitionFingerprint, this includes descriptions, discovery
	// preferences, custom grammars and all future declaration fields.
	definition := object{"name": name, "declaration": info.Catalog}
	return object{"name": name, "version": fingerprint(definition), "definition": definition}
}

func (b *Bridge) catalogPrintArguments(carrier string, names []string) (object, error) {
	if len(names) == 0 || len(names) > 4 {
		return nil, fmt.Errorf("basispoints catalog lookup requires 1 to 4 exact tool names")
	}
	seen := map[string]bool{}
	docs := make([]any, 0, len(names))
	for _, name := range names {
		if _, ok := b.tools[name]; !ok || seen[name] {
			return nil, fmt.Errorf("basispoints catalog lookup requires unique declared tool names")
		}
		seen[name] = true
		docs = append(docs, b.catalogDocumentation(name))
	}
	payload := catalogDisclosureStart + quoted(object{
		"notice": "Tool documentation only. No requested action was executed. Read all policies and constraints, then submit the actual tool call separately using its declared transport.",
		"tools":  docs,
	}) + catalogDisclosureEnd
	if len(payload) > catalogDisclosureLimit {
		return nil, fmt.Errorf("basispoints catalog lookup output is too large; request fewer names")
	}
	// A JSON-quoted literal is the ONLY argument to the known Node printer. Never
	// interpolate tool descriptions, names, schemas or attempted action arguments
	// as JavaScript syntax, and never evaluate the code on the gateway.
	args := object{"code": "nodeRepl.write(" + quoted(payload) + ");"}
	info, ok := b.tools[carrier]
	if !ok || info.Schema == nil || info.Schema.Validate(args) != nil {
		return nil, fmt.Errorf("basispoints catalog lookup is incompatible with the declared output tool")
	}
	return args, nil
}

func (b *Bridge) catalogDisclosureCall(native object, names []string) (object, error) {
	view := b.catalogDelivery
	args, err := b.catalogPrintArguments(view.carrier, names)
	if err != nil {
		return nil, err
	}
	// The carrier is always eager. Reuse normal schema validation, identity and
	// namespace serialization. Its caller owns the atomic replay write.
	return b.finishClientToolCall(native, b.tools[view.carrier], object{"arguments": args}, false, true)
}

func (b *Bridge) translateCatalogLookup(native object) (object, bool, error) {
	view := b.catalogDelivery
	if view == nil || view.carrier == "" || text(native["type"]) != "function_call" {
		return nil, false, nil
	}
	name := text(native["name"])
	if name != "run_officejs" && name != "functions.run_officejs" {
		return nil, false, nil
	}
	outer := transportArguments(native)
	for _, prefix := range []string{customTransportPrefix, functionCodeTransportPrefix, functionCmdTransportPrefix} {
		if strings.HasPrefix(text(outer["summary"]), prefix) {
			return nil, false, nil
		}
	}
	envelope, err := decodeTransportEnvelope(outer["code"])
	if err != nil {
		return nil, false, nil
	}
	target, err := envelopeName(envelope)
	if err != nil || target != catalogLookupName {
		return nil, false, nil
	}
	args, err := envelopeArguments(envelope)
	if err == nil {
		if raw, ok := args.(string); ok {
			err = decode([]byte(raw), &args)
		}
	}
	params, ok := args.(object)
	if err != nil || !ok || len(params) != 1 {
		return nil, true, fmt.Errorf("basispoints catalog lookup requires only names")
	}
	items, ok := params["names"].([]any)
	if !ok || len(items) == 0 || len(items) > 4 {
		return nil, true, fmt.Errorf("basispoints catalog lookup requires 1 to 4 exact tool names")
	}
	names := make([]string, len(items))
	for i, raw := range items {
		names[i] = text(raw)
	}
	client, err := b.catalogDisclosureCall(native, names)
	if err == nil {
		err = b.rememberReplay(text(native["call_id"]), native, client)
	}
	return client, true, err
}

// Decode only our literal-print grammar; do not search arbitrary executable code
// for a version marker. This also works without the native replay cache.
func catalogPrintedPayload(item object, carrier string) (string, bool) {
	if text(item["type"]) != "function_call" {
		return "", false
	}
	name := text(item["name"])
	if ns := text(item["namespace"]); ns != "" {
		name = ns + "." + name
	}
	if name != carrier {
		return "", false
	}
	args, _ := item["arguments"].(object)
	if args == nil && decode([]byte(text(item["arguments"])), &args) != nil {
		return "", false
	}
	code := text(args["code"])
	if !strings.HasPrefix(code, "nodeRepl.write(") || !strings.HasSuffix(code, ");") {
		return "", false
	}
	var payload string
	if decode([]byte(strings.TrimSuffix(strings.TrimPrefix(code, "nodeRepl.write("), ");")), &payload) != nil || len(payload) > catalogDisclosureLimit {
		return "", false
	}
	if !strings.HasPrefix(payload, catalogDisclosureStart) || !strings.HasSuffix(payload, catalogDisclosureEnd) {
		return "", false
	}
	return payload, true
}

func (b *Bridge) readCatalogDisclosures(value any, view *catalogDelivery) string {
	items, _ := value.([]any)
	type disclosure struct {
		payload string
		matched bool
	}
	calls := map[string]*disclosure{}
	seen := map[string]bool{}
	results := map[string]bool{}
	for _, raw := range items {
		item, _ := raw.(object)
		id := text(item["call_id"])
		if isTool(item) {
			payload, found := catalogPrintedPayload(item, view.carrier)
			if found {
				if id == "" || seen[id] || results[id] || (text(item["status"]) != "" && text(item["status"]) != "completed") {
					return "disclosure_incomplete"
				}
				calls[id] = &disclosure{payload: payload}
			} else if calls[id] != nil {
				return "disclosure_incomplete"
			}
			seen[id] = true
			continue
		}
		if text(item["type"]) != "function_call_output" {
			continue
		}
		results[id] = true
		texts, healthy := catalogResultTexts(item["output"], 0)
		call := calls[id]
		if call == nil {
			// Compaction or partial history may retain a result without its print
			// call. A marker alone never proves disclosure or authorizes an action.
			for _, s := range texts {
				if strings.Contains(s, catalogDisclosureStart) {
					return "disclosure_incomplete"
				}
			}
			continue
		}
		if call.matched || !healthy || item["is_error"] == true || item["isError"] == true || item["error"] != nil || (text(item["status"]) != "" && text(item["status"]) != "completed") {
			return "disclosure_failed"
		}
		complete := false
		for _, s := range texts {
			complete = complete || strings.Contains(s, call.payload)
		}
		if !complete {
			return "disclosure_incomplete"
		}
		call.matched = true
		var document object
		if decode([]byte(strings.TrimSuffix(strings.TrimPrefix(call.payload, catalogDisclosureStart), catalogDisclosureEnd)), &document) != nil {
			return "disclosure_incomplete"
		}
		docs, _ := document["tools"].([]any)
		if len(docs) == 0 || len(docs) > 4 {
			return "disclosure_incomplete"
		}
		for _, raw := range docs {
			doc, _ := raw.(object)
			name := text(doc["name"])
			if view.deferred[name] && quoted(doc) == quoted(b.catalogDocumentation(name)) {
				view.disclosed[name] = true
			}
		}
	}
	for _, call := range calls {
		if !call.matched {
			return "disclosure_incomplete"
		}
	}
	return ""
}

// Recognize plain Responses text and the common MCP content wrapper. Do not
// accept a digest in arbitrary structured metadata as visible documentation.
func catalogResultTexts(value any, depth int) ([]string, bool) {
	if depth > 8 {
		return nil, false
	}
	switch v := value.(type) {
	case string:
		if len(v) > 2<<20 {
			return nil, false
		}
		var wrapped object
		if decode([]byte(v), &wrapped) == nil && wrapped != nil {
			return catalogResultTexts(wrapped, depth+1)
		}
		return []string{v}, true
	case []any:
		var texts []string
		for _, part := range v {
			next, ok := catalogResultTexts(part, depth+1)
			if !ok {
				return nil, false
			}
			texts = append(texts, next...)
		}
		return texts, true
	case object:
		if v["isError"] == true || v["is_error"] == true || v["error"] != nil || v["truncated"] == true || (text(v["status"]) != "" && text(v["status"]) != "completed") {
			return nil, false
		}
		if content, ok := v["content"]; ok {
			return catalogResultTexts(content, depth+1)
		}
		if kind := text(v["type"]); kind == "text" || kind == "input_text" || kind == "output_text" {
			return catalogResultTexts(v["text"], depth+1)
		}
	}
	return nil, false
}
