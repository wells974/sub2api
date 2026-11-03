package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareConfigurationUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updates []string
		want    string
	}{
		{"single", []string{"high"}, "high"},
		{"last update wins", []string{"high", "low"}, "low"},
		{"capped effort", []string{"ultra"}, "xhigh"},
		{"minimum effort", []string{"minimal"}, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testSource()
			before, after := message("user", "before"), message("user", "continue")
			input := []any{before}
			for _, effort := range tc.updates {
				input = append(input, object{"type": "configuration_update", "reasoning": object{"effort": effort}})
			}
			source["input"] = append(input, after)
			prepared, bridge := mustPrepare(t, source, "configuration", nil)
			require.Equal(t, tc.want, prepared["reasoning_effort"])
			require.Equal(t, tc.want, bridge.Effort)
			require.Equal(t, tc.updates[len(tc.updates)-1], bridge.RequestedEffort)
			history := mustTestValue[[]any](t, prepared["input"])
			require.Len(t, history, 4) // Two developer instructions and both user messages.
			require.Equal(t, []any{before, after}, history[2:])
			again, _ := mustPrepare(t, source, "configuration", nil)
			require.Equal(t, prepared, again)
		})
	}
}

func TestPrepareConfigurationUpdateWithoutTopLevelEffort(t *testing.T) {
	source := testSource()
	delete(source, "reasoning")
	source["input"] = []any{message("user", "hello"), object{"type": "configuration_update", "reasoning": object{"effort": "high"}}}
	prepared, bridge := mustPrepare(t, source, "", nil)
	require.Equal(t, "high", prepared["reasoning_effort"])
	require.Equal(t, "high", bridge.RequestedEffort)
}

func TestPrepareConfigurationUpdatePreservesToolHistory(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}}
	source["input"] = []any{
		message("user", "check"),
		object{"type": "function_call", "call_id": "call_check", "name": "shell", "arguments": `{}`},
		object{"type": "configuration_update", "reasoning": object{"effort": "high"}},
		object{"type": "function_call_output", "call_id": "call_check", "output": "done"},
	}
	prepared, bridge := mustPrepare(t, source, "", new(ReplayCache))
	history := mustTestValue[[]any](t, prepared["input"])
	require.Len(t, history, 5)
	call := mustTestValue[object](t, history[3])
	result := mustTestValue[object](t, history[4])
	require.Equal(t, "run_officejs", call["name"])
	require.Equal(t, "call_check", call["call_id"])
	require.Equal(t, call["call_id"], result["call_id"])
	require.Equal(t, "done", result["output"])
	require.True(t, bridge.hasToolHistory)
	require.Equal(t, "high", prepared["reasoning_effort"])
}

func TestPrepareConfigurationUpdateRejectsInvalidSettings(t *testing.T) {
	for name, reasoning := range map[string]any{
		"missing":              nil,
		"wrong reasoning type": "high",
		"missing effort":       object{},
		"empty effort":         object{"effort": " "},
		"wrong effort type":    object{"effort": 42},
		"unknown effort":       object{"effort": "private-invalid-value"},
		"unsupported mode":     object{"effort": "high", "mode": "pro"},
	} {
		t.Run(name, func(t *testing.T) {
			source := testSource()
			source["input"] = []any{
				message("user", "hello"),
				object{"type": "configuration_update", "reasoning": reasoning},
				object{"type": "configuration_update", "reasoning": object{"effort": "high"}},
			}
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			_, _, err = Prepare(raw, "", nil)
			require.ErrorContains(t, err, "path=input[1].reasoning.")
			require.NotContains(t, err.Error(), "private-invalid-value")
		})
	}
}
