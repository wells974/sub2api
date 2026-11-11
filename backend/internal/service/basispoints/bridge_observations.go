package basispoints

// Read-only observations must not make routing, retry, or catalog decisions.

import (
	"encoding/json"
	"errors"
	"sync"
)

// CatalogObservation measures the effective catalog selected by Prepare,
// including inherited tools. PromptBytes excludes the surrounding protocol.
type CatalogObservation struct {
	ToolCount      int `json:"tool_count"`
	PromptBytes    int `json:"prompt_bytes"`
	SchemaBytes    int `json:"schema_bytes"`
	MaxSchemaBytes int `json:"max_schema_bytes"`
}

type BridgeObservation struct {
	Catalog         CatalogObservation   `json:"catalog"`
	ArgumentFailure *ArgumentObservation `json:"argument_failure,omitempty"`
}

// A pointer is intentional: validation copies Bridge by value. Never embed a
// mutex directly in Bridge, or retain validation errors containing user data.
type bridgeObservations struct {
	mu    sync.Mutex
	value BridgeObservation
}

func (b *Bridge) Observations() BridgeObservation {
	if b == nil || b.observations == nil {
		return BridgeObservation{}
	}
	b.observations.mu.Lock()
	defer b.observations.mu.Unlock()
	result := b.observations.value
	if result.ArgumentFailure != nil {
		copy := *result.ArgumentFailure
		copy.Missing = append([]string(nil), copy.Missing...)
		copy.ExpectedTypes = append([]string(nil), copy.ExpectedTypes...)
		result.ArgumentFailure = &copy
	}
	return result
}

func (b *Bridge) describeObservedCatalog(catalog []any) string {
	rendered := describeCatalog(catalog)
	stats := CatalogObservation{ToolCount: len(catalog), PromptBytes: len(rendered)}
	for _, info := range b.tools {
		if info.Parameters == nil {
			continue
		}
		encoded, err := json.Marshal(info.Parameters)
		if err != nil {
			continue
		}
		stats.SchemaBytes += len(encoded)
		if len(encoded) > stats.MaxSchemaBytes {
			stats.MaxSchemaBytes = len(encoded)
		}
	}
	b.observations.mu.Lock()
	b.observations.value.Catalog = stats
	b.observations.mu.Unlock()
	return rendered
}

func (b *Bridge) observeTerminalFailure(err error) {
	var failure toolArgumentsSchemaError
	if b.observations == nil || !errors.As(err, &failure) || failure.diagnostic == nil {
		return
	}
	b.observations.mu.Lock()
	b.observations.value.ArgumentFailure = failure.diagnostic
	b.observations.mu.Unlock()
}
