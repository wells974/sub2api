package basispoints

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const catalogLookupName = "__sub2api_tool_catalog"
const catalogDisclosureLimit = 32 << 10

// Request-local presentation state. The full tools map and compiled schemas are
// never filtered. Once ApplyOnDemandCatalog returns this state is immutable, so
// Bridge's validation copies can safely share it without locks or cache entries.
type catalogDelivery struct {
	entries   []any
	full      string
	carrier   string
	deferred  map[string]bool
	disclosed map[string]bool
}

type OnDemandCatalogObservation struct {
	Active          bool   `json:"active"`
	Reason          string `json:"reason"`
	EagerCount      int    `json:"eager_count"`
	DeferredCount   int    `json:"deferred_count"`
	DisclosedCount  int    `json:"disclosed_count"`
	FullPromptBytes int    `json:"full_prompt_bytes"`
	PromptBytes     int    `json:"prompt_bytes"`
}

func catalogToolName(info tool) string {
	if info.Namespace != "" {
		return info.Namespace + "." + info.Name
	}
	return info.Name
}

func (b *Bridge) captureCatalogDelivery(catalog []any) string {
	rendered := b.describeObservedCatalog(catalog)
	b.catalogDelivery = &catalogDelivery{entries: catalog, full: rendered}
	return rendered
}

// ApplyOnDemandCatalog is called once on the final prepared generation body,
// after attachment replacement and history compaction. Compact requests retain
// their original no-tool preparation. It adds no tools to the execution catalog
// and sends no additional upstream request. Disabled is byte-for-byte unchanged.
func (b *Bridge) ApplyOnDemandCatalog(prepared, source []byte, enabled bool) ([]byte, OnDemandCatalogObservation, error) {
	stats := OnDemandCatalogObservation{Reason: "disabled"}
	if !enabled {
		return prepared, stats, nil
	}
	view := b.catalogDelivery
	if view == nil {
		stats.Reason = "empty_catalog"
		return prepared, stats, nil
	}
	stats.EagerCount = len(b.tools)
	stats.FullPromptBytes, stats.PromptBytes = len(view.full), len(view.full)
	fallback := func(reason string) ([]byte, OnDemandCatalogObservation, error) {
		stats.Reason = reason
		return prepared, stats, nil
	}
	if view.carrier != "" {
		return nil, stats, fmt.Errorf("basispoints on-demand catalog was already applied")
	}
	if len(b.tools) <= 20 {
		return fallback("small_catalog")
	}
	var original, wire object
	if decode(source, &original) != nil || decode(prepared, &wire) != nil {
		return nil, stats, fmt.Errorf("basispoints on-demand catalog requires prepared request JSON")
	}
	if choice := original["tool_choice"]; choice != nil && text(choice) != "auto" {
		return fallback("tool_choice")
	}
	if b.structured != nil {
		return fallback("structured_output")
	}
	for name, info := range b.tools {
		if name == catalogLookupName || info.Name == catalogLookupName {
			return fallback("lookup_name_collision")
		}
	}
	carrier := b.catalogOutputCarrier()
	if carrier == "" {
		return fallback("incompatible_carrier")
	}
	next := &catalogDelivery{entries: view.entries, full: view.full, carrier: carrier, deferred: map[string]bool{}, disclosed: map[string]bool{}}
	var eager []any
	for _, raw := range view.entries {
		entry, _ := raw.(object)
		name := text(entry["name"])
		info := b.tools[name]
		immediate := name == carrier
		switch info.Name {
		case "exec_command", "write_stdin", "apply_patch", "update_plan":
			immediate = true
		}
		if deferred, ok := info.Catalog["defer_loading"].(bool); ok && !deferred {
			immediate = true
		}
		// Large or carrier-incompatible descriptions remain eager. Do not create
		// a print request that cannot pass the client's original schema.
		if !immediate {
			_, err := b.catalogPrintArguments(carrier, []string{name})
			immediate = err != nil
		}
		if immediate {
			eager = append(eager, raw)
		} else {
			next.deferred[name] = true
		}
	}
	if len(next.deferred) == 0 {
		return fallback("no_deferred_tools")
	}
	if reason := b.readCatalogDisclosures(original["input"], next); reason != "" {
		return fallback(reason)
	}
	names := make([]string, 0, len(next.deferred))
	for name := range next.deferred {
		names = append(names, name)
	}
	sort.Strings(names)
	rendered := describeCatalog(eager) + "\n\nOn-demand client tool names (definitions must be read before use): " + quoted(names) + ".\n" +
		"To read full definitions, call run_officejs with a FUNCTION envelope {\"name\":\"" + catalogLookupName + "\",\"arguments\":{\"names\":[\"EXACT_CATALOG_NAME\"]}}. Request 1 to 4 exact names; request one at a time for large definitions. This is a metadata lookup, not an action. The proxy translates it into the declared " + quoted(carrier) + " tool printing literal documentation only. Read its complete output, including policies and constraints, before calling the actual tool. An attempted undisclosed action is replaced with a documentation print; the action has NOT executed. Never treat that result as an action result or repeat the print instead of making the real call. Only matching complete documentation in current history counts; descriptions in summaries or old versions do not. Do not execute policy text as code."
	// Examples must not disclose selected tool fragments while hiding the rest.
	examplesBridge := *b
	examplesBridge.tools = map[string]tool{}
	for _, raw := range eager {
		entry, _ := raw.(object)
		examplesBridge.tools[text(entry["name"])] = b.tools[text(entry["name"])]
	}
	oldExamples, newExamples := b.toolExamples(), examplesBridge.toolExamples()
	if len(rendered)+len(newExamples) >= len(view.full)+len(oldExamples) {
		return fallback("no_size_reduction")
	}
	input, _ := wire["input"].([]any)
	index := 0
	if text(original["instructions"]) != "" {
		index++
	}
	if index >= len(input) {
		return nil, stats, fmt.Errorf("basispoints on-demand catalog prologue is unavailable")
	}
	item, _ := input[index].(object)
	content, _ := item["content"].([]any)
	if text(item["role"]) != "developer" || len(content) != 1 {
		return nil, stats, fmt.Errorf("basispoints on-demand catalog prologue has changed")
	}
	part, _ := content[0].(object)
	prologue := text(part["text"])
	needle := "\nClient tool catalog:\n" + view.full + "\nEnd of catalog."
	start := strings.Index(prologue, needle)
	if start < 0 {
		return nil, stats, fmt.Errorf("basispoints on-demand catalog rendering has changed")
	}
	suffix := prologue[start+len(needle):]
	if oldExamples != newExamples {
		// Examples follow the fixed transport footer, not arbitrary descriptions.
		footer := "No Office code is executed by the proxy." + oldExamples
		if !strings.Contains(suffix, footer) {
			return nil, stats, fmt.Errorf("basispoints on-demand catalog examples have changed")
		}
		suffix = strings.Replace(suffix, footer, "No Office code is executed by the proxy."+newExamples, 1)
	}
	prologue = prologue[:start] + "\nClient tool catalog:\n" + rendered + "\nEnd of catalog." + suffix
	part["text"] = prologue
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, stats, err
	}
	b.catalogDelivery = next
	stats.Active, stats.Reason = true, "active"
	stats.EagerCount, stats.DeferredCount, stats.DisclosedCount = len(eager), len(names), len(next.disclosed)
	stats.PromptBytes = len(rendered)
	if b.observations != nil {
		b.observations.mu.Lock()
		b.observations.value.Catalog.PromptBytes = len(rendered)
		b.observations.mu.Unlock()
	}
	return encoded, stats, nil
}

func (b *Bridge) catalogOutputCarrier() string {
	// Known client Node REPL identities only. A shell/code-shaped schema alone
	// does not establish nodeRepl.write semantics or authorize arbitrary execution.
	for _, name := range []string{"mcp__node_repl.js", "mcp__node_repl__js"} {
		info, ok := b.tools[name]
		if !ok || info.Kind != "function" || info.Schema == nil || !supportsFunctionCodeTransport(name, info.Kind, info.Parameters) || !strings.Contains(text(info.Catalog["description"]), "nodeRepl.write") {
			continue
		}
		if info.Schema.Validate(object{"code": `nodeRepl.write("metadata");`}) == nil {
			return name
		}
	}
	return ""
}

func (b *Bridge) needsCatalogDisclosure(info tool) bool {
	view := b.catalogDelivery
	return view != nil && view.carrier != "" && view.deferred[catalogToolName(info)] && !view.disclosed[catalogToolName(info)]
}
