package basispoints

import "fmt"

func isCatalogItem(kind string) bool {
	return kind == "additional_tools" || kind == "tool_search_output"
}

type catalogDeclaration struct {
	item           object
	namespace, key string
	path           string
}

// Flatten only declaration identity here. Validate the selected schema later,
// so a superseded historical schema cannot reject a valid current declaration.
func flattenCatalogAt(value any, namespace, location string) ([]catalogDeclaration, error) {
	items, ok := value.([]any)
	if value != nil && !ok {
		return nil, fmt.Errorf("basispoints client tools must be an array")
	}
	var result []catalogDeclaration
	for index, raw := range items {
		itemPath := fmt.Sprintf("%s[%d]", location, index)
		item, ok := raw.(object)
		if !ok {
			return nil, fmt.Errorf("invalid Basispoints client tool")
		}
		kind, name := text(item["type"]), text(item["name"])
		if kind == "namespace" {
			if name == "" {
				return nil, fmt.Errorf("basispoints client namespaces require a name")
			}
			path := name
			if namespace != "" {
				path = namespace + "." + name
			}
			nested, err := flattenCatalogAt(item["tools"], path, itemPath+".tools")
			if err != nil {
				return nil, err
			}
			result = append(result, nested...)
			continue
		}
		key := name
		if namespace != "" {
			key = namespace + "." + name
		}
		if clientToolSearch(item) && namespace == "" {
			key = "tool_search"
		} else if isUnsupportedHostedTool(kind) {
			key = "hosted:" + kind
		}
		result = append(result, catalogDeclaration{item: item, namespace: namespace, key: key, path: itemPath})
	}
	return result, nil
}

// Older clients can omit execution on the output. Infer it only from a
// matching client search in this request or the same scoped replay cache.
func (b *Bridge) pairedClientSearchOutputs(items []any) (map[int]object, error) {
	calls := make(map[string]bool)
	paired := make(map[int]object)
	for i, raw := range items {
		item, _ := raw.(object)
		id := text(item["call_id"])
		if id == "" {
			continue
		}
		switch item["type"] {
		case "function_call", "custom_tool_call", "tool_search_call":
			calls[id] = item["type"] == "tool_search_call" && item["execution"] == "client"
		case "tool_search_output":
			if item["execution"] != nil && item["execution"] != "client" {
				continue
			}
			if client, seen := calls[id]; seen {
				if client {
					paired[i] = nil
				}
				continue
			}
			if native, client := b.replay.getMatching(b.scope, id, ""); client {
				paired[i] = native
			} else if err := b.replay.Err(); err != nil {
				return nil, err
			} else if item["execution"] == "client" {
				return nil, fmt.Errorf("basispoints original client search is unavailable (path=input[%d]); resend the matching complete tool search call with its result", i)
			}
		}
	}
	return paired, nil
}

// Current explicit declarations win; without them, newer replay declarations
// replace inherited entries. Preserve first-seen order while choosing the last
// replayed definition. Cache publication still follows the existing CAS boundary.
func (b *Bridge) collectClientCatalog(source object, inherited bool, pairedSearch map[int]object) ([]any, error) {
	catalog, err := b.collectTools(source["tools"], "")
	if err != nil {
		return nil, err
	}
	var order []string
	replayed := make(map[string]catalogDeclaration)
	items, _ := source["input"].([]any)
	for index, raw := range items {
		item, _ := raw.(object)
		if !isCatalogItem(text(item["type"])) {
			continue
		}
		declarations, err := flattenCatalogAt(item["tools"], "", fmt.Sprintf("input[%d].tools", index))
		if err != nil {
			return nil, err
		}
		// The native client already revealed these definitions. Put the selected
		// schema in the BPS prompt once, without requiring a second lookup.
		_, paired := pairedSearch[index]
		clientResult := item["type"] == "tool_search_output" && (item["execution"] == "client" || paired)
		for _, declaration := range declarations {
			if clientResult {
				if b.clientSearchResults == nil {
					b.clientSearchResults = make(map[string]bool)
				}
				b.clientSearchResults[declaration.key] = true
			}
			if _, exists := b.tools[declaration.key]; exists && !inherited {
				continue
			}
			if _, exists := replayed[declaration.key]; !exists {
				order = append(order, declaration.key)
			}
			replayed[declaration.key] = declaration
		}
	}
	if inherited {
		kept := catalog[:0]
		for _, raw := range catalog {
			entry, _ := raw.(object)
			if _, replaced := replayed[text(entry["name"])]; !replaced {
				kept = append(kept, raw)
			}
		}
		catalog = kept
	}
	for _, key := range order {
		declaration := replayed[key]
		if inherited {
			delete(b.tools, key)
		}
		additional, err := b.collectToolsAt([]any{declaration.item}, declaration.namespace, declaration.path, true)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, additional...)
	}
	return catalog, nil
}
