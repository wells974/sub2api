package basispoints

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the upstream diagnostic through the fork's actual correction path.
// A union failure must not tell the model to satisfy only the first branch.
func TestSchemaRepairUpstreamUnionDiagnostic(t *testing.T) {
	for _, union := range []string{"oneOf", "anyOf"} {
		for _, corrected := range []bool{false, true} {
			t.Run(union+"/"+quoted(corrected), func(t *testing.T) {
				source := schemaRepairSource()
				source["tools"].([]any)[0].(object)["parameters"] = object{
					"type": "object", union: []any{
						object{"required": []any{"left"}},
						object{"required": []any{"right"}},
					},
				}
				_, b := mustPrepare(t, source, "union-correction", new(ReplayCache))
				ctx := WithSchemaRepair(context.Background(), time.Second)
				attempts := 0
				out, err := readSchemaStream(t, b, ctx, "", schemaResponse(schemaCall("bad", "lookup", object{})),
					func(_ context.Context, _ object, validation error) (object, error) {
						attempts++
						feedback, ok := validation.(*schemaRepairFeedback)
						require.True(t, ok)
						require.Len(t, feedback.Issues, 1)
						diagnostic := feedback.Issues[0].Diagnostic
						require.Equal(t, union, diagnostic.Keyword)
						require.Empty(t, diagnostic.Missing)
						require.Empty(t, diagnostic.ExpectedTypes)
						args := object{}
						if corrected {
							args["right"] = true // The second branch is equally valid.
						}
						return schemaResponse(schemaCall("fixed", "lookup", args)), nil
					})
				require.NoError(t, err)
				require.Equal(t, 1, attempts)
				require.Nil(t, b.replay.get("union-correction", "call_bad"))
				if corrected {
					require.Contains(t, string(out), "event: response.completed")
					require.NotContains(t, string(out), "event: response.failed")
					require.NotNil(t, b.replay.get("union-correction", "call_fixed"))
				} else {
					require.Contains(t, string(out), "event: response.failed")
					require.Contains(t, string(out), `"rule":"`+union+`"`)
					require.NotContains(t, string(out), "response.function_call_arguments")
					require.Nil(t, b.replay.get("union-correction", "call_fixed"))
				}
			})
		}
	}
}

// Named tool enforcement must survive the fork's separate schema correction
// path, even when an earlier schema error masks a later wrong-target call.
func TestSchemaRepairUpstreamToolChoice(t *testing.T) {
	for _, scenario := range []string{"required", "named", "wrong_target", "mixed_target", "changed_target"} {
		t.Run(scenario, func(t *testing.T) {
			source := schemaRepairSource()
			source["parallel_tool_calls"] = true
			source["tools"].([]any)[1].(object)["parameters"] = source["tools"].([]any)[0].(object)["parameters"]
			source["tool_choice"] = object{"type": "function", "name": "lookup"}
			if scenario == "required" {
				source["tool_choice"] = "required"
			}
			response := schemaResponse(schemaCall("bad", "lookup", object{}))
			switch scenario {
			case "wrong_target":
				response = schemaResponse(schemaCall("bad", "other", object{}))
			case "mixed_target":
				response = schemaResponse(schemaCall("bad", "lookup", object{}), schemaCall("other", "other", object{"count": 2}))
			}
			_, b := mustPrepare(t, source, scenario, new(ReplayCache))
			ctx := WithSchemaRepair(context.Background(), time.Second)
			attempts := 0
			out, err := readSchemaStream(t, b, ctx, "", response, func(_ context.Context, _ object, _ error) (object, error) {
				attempts++
				switch scenario {
				case "wrong_target", "changed_target":
					return schemaResponse(schemaCall("fixed", "other", object{"count": 1})), nil
				case "mixed_target":
					return schemaResponse(schemaCall("fixed", "lookup", object{"count": 1}), schemaCall("kept", "other", object{"count": 2})), nil
				default:
					return schemaResponse(schemaCall("fixed", "lookup", object{"count": 1})), nil
				}
			})
			require.NoError(t, err)
			if scenario == "required" || scenario == "named" {
				require.Equal(t, 1, attempts)
				require.Contains(t, string(out), "event: response.completed")
				require.NotContains(t, string(out), "event: response.failed")
				require.NotNil(t, b.replay.get(scenario, "call_fixed"))
			} else {
				if scenario == "changed_target" {
					require.Equal(t, 1, attempts)
				} else {
					require.Zero(t, attempts, "a tool-choice violation must not submit a correction")
				}
				require.Contains(t, string(out), "event: response.failed")
				require.NotContains(t, string(out), "response.function_call_arguments")
				require.Nil(t, b.replay.get(scenario, "call_fixed"))
				require.Nil(t, b.replay.get(scenario, "call_kept"))
			}
		})
	}
}
