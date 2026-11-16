package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImagePolicyPreservesConfigurationAfterCompaction(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		source := testSource()
		source["input"] = []any{
			message("user", "old history"),
			object{"type": "configuration_update", "reasoning": object{"effort": "high"}},
			message("assistant", "done"),
			message("user", "continue"),
		}
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		history, err := InspectImageHistory(raw)
		require.NoError(t, err)
		_, bridge, err := Prepare(raw, "config-compact", nil)
		require.NoError(t, err)
		window := []any{object{"type": "compaction", "encrypted_content": "opaque"}}
		if reconcile {
			checkpoint, err := history.Checkpoint(3, window)
			require.NoError(t, err)
			echo := append(append([]any{}, source["input"].([]any)...), window...)
			source["input"] = append(echo, message("user", "next turn"))
			raw, err = json.Marshal(source)
			require.NoError(t, err)
			history, err = InspectImageHistory(raw)
			require.NoError(t, err)
			changed, err := history.Reconcile([]ImageCheckpoint{checkpoint})
			require.NoError(t, err)
			require.True(t, changed)
			raw, err = history.Body()
			require.NoError(t, err)
		} else {
			raw, err = history.WithInput(append(window, history.Input[3:]...), false)
			require.NoError(t, err)
		}
		_, prepared, err := bridge.Reprepare(raw)
		require.NoError(t, err)
		require.Equal(t, "high", prepared.Effort, "reconcile=%t", reconcile)
		// Rewriting history must not mutate the saved original request.
		require.Equal(t, "max", history.source["reasoning"].(object)["effort"])
	}
}

func TestImagePolicyDoesNotHideInvalidConfiguration(t *testing.T) {
	history := policyHistory(t, message("user", "old"),
		object{"type": "configuration_update", "reasoning": object{"effort": "PRIVATE_INVALID"}},
		message("user", "new"))
	_, err := history.WithInput(history.Input[2:], false)
	require.ErrorContains(t, err, "input[1].reasoning.effort")
	require.NotContains(t, err.Error(), "PRIVATE_INVALID")
}
