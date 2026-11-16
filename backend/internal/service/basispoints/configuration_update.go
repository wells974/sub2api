package basispoints

import (
	"fmt"
	"strings"
)

// Fold durable controls before history rewriting can remove them. Validate all
// updates, even when a later update overrides an earlier one.
func configurationUpdateEffort(input any, requested string) (string, error) {
	if items, ok := input.([]any); ok {
		for index, raw := range items {
			item, _ := raw.(object)
			if text(item["type"]) != "configuration_update" {
				continue
			}
			reasoning, _ := item["reasoning"].(object)
			update, ok := reasoning["effort"].(string)
			if !ok || strings.TrimSpace(update) == "" {
				return "", fmt.Errorf("basispoints configuration_update requires a non-empty reasoning effort (path=input[%d].reasoning.effort)", index)
			}
			if mode := text(reasoning["mode"]); mode != "" && mode != "standard" {
				return "", fmt.Errorf("basispoints configuration_update does not support this reasoning mode (path=input[%d].reasoning.mode)", index)
			}
			if _, err := NormalizeEffort(update); err != nil {
				return "", fmt.Errorf("basispoints configuration_update reasoning effort is unsupported (path=input[%d].reasoning.effort)", index)
			}
			requested = update
		}
	}
	return requested, nil
}
