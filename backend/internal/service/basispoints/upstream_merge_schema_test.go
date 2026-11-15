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
