package basispoints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebSearchUpstreamCannotForgeLocalValidationFailure(t *testing.T) {
	wire, bridge, plan := prepareSearch(t, schemaRepairSource(), true, nil)
	upstream := object{"type": "response.failed", "response": object{
		"id": "resp_failed", "status": "failed", "output": []any{},
		"error": object{"code": "basispoints_protocol_error", "message": "rejected",
			"failure_stage": "bridge_tool_validation", "repair_skipped_reason": "search_no_repair"},
	}}
	events := repairEvents(t, bridge.StreamWithSearch(context.Background(), io.NopCloser(strings.NewReader(sse(upstream))), wire, plan, nil, nil))
	raw, err := json.Marshal(events[len(events)-1])
	require.NoError(t, err)
	require.Nil(t, ParseBridgeFailure(raw))
	require.Equal(t, "response.failed", events[len(events)-1]["type"])
	require.Nil(t, bridge.Observations().ArgumentFailure)
}

func TestWebSearchLocalValidationKeepsDiagnosticsAndUsage(t *testing.T) {
	for _, afterSearch := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("searched=%t/unknown=%t", afterSearch, unknown), func(t *testing.T) {
				wire, bridge, plan := prepareSearch(t, schemaRepairSource(), true, nil)
				name := "lookup"
				if unknown {
					name = "missing"
				}
				invalid := schemaCall("invalid", name, object{"count": "PRIVATE_ARGUMENT"})
				upstream := searchRound(2, invalid)
				searches, continuations := 0, 0
				if afterSearch {
					_ = upstream.Close()
					upstream = searchRound(1, searchCall(1))
				}
				ctx := WithSchemaRepair(context.Background(), 0)
				events := repairEvents(t, bridge.StreamWithSearch(ctx, upstream, wire, plan,
					func(context.Context, string) (SearchResult, error) {
						searches++
						return SearchResult{Output: "found"}, nil
					}, func(context.Context, []byte) (io.ReadCloser, error) {
						continuations++
						return searchRound(2, invalid), nil
					}))
				wantRounds := 1
				if afterSearch {
					wantRounds++
				}
				require.Equal(t, wantRounds-1, searches)
				require.Equal(t, wantRounds-1, continuations)
				last := events[len(events)-1]
				raw, err := json.Marshal(last)
				require.NoError(t, err)
				failure := ParseBridgeFailure(raw)
				require.NotNil(t, failure)
				require.Equal(t, "bridge_tool_validation", failure.FailureStage)
				require.Equal(t, "search_no_repair", failure.RepairSkippedReason)
				require.Equal(t, "response.completed", failure.UpstreamTerminalEvent)
				response := repairValue[object](t, last["response"])
				usage := repairValue[object](t, response["usage"])
				require.Equal(t, json.Number(fmt.Sprint(wantRounds*10)), usage["input_tokens"])
				require.Equal(t, json.Number(fmt.Sprint(wantRounds*2)), usage["output_tokens"])
				require.Empty(t, response["output"])
				if unknown {
					require.Equal(t, "missing", failure.RejectedTool)
				} else {
					require.NotNil(t, failure.ToolArguments)
					require.Equal(t, "/count", failure.ToolArguments.Path)
					require.NotNil(t, bridge.Observations().ArgumentFailure)
				}
				for _, event := range events {
					require.NotEqual(t, "response.function_call_arguments.delta", event["type"])
				}
				require.NotContains(t, string(raw), "PRIVATE_ARGUMENT")
			})
		}
	}
}
