package basispoints

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientToolSearchRoundTrip(t *testing.T) {
	source := testSource()
	search := object{"type": "tool_search", "execution": "client", "parameters": object{"type": "object",
		"properties": object{"query": object{"type": "string"}}, "required": []any{"query"}}}
	source["tools"] = []any{search}
	cache := new(ReplayCache)
	_, bridge := mustPrepare(t, source, "session", cache)
	makeCall := func(query any) object {
		return nativeCall(object{"name": "tool_search", "arguments": object{"query": query}})
	}
	bad := object{"output": []any{makeCall(3)}}
	require.Error(t, bridge.translateResponse(bad))
	native := makeCall("read_file")
	wire := sse(object{"type": "response.completed", "response": object{"id": "resp", "output": []any{native}}})
	body := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
	defer body.Close()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NotContains(t, string(data), "response.function_call_arguments")
	var completed object
	var itemEvents []string
	require.NoError(t, readEvents(bytes.NewReader(data), func(kind string, data []byte) error {
		var event object
		if err := decode(data, &event); err != nil {
			return err
		}
		switch kind {
		case "response.output_item.added", "response.output_item.done":
			itemEvents = append(itemEvents, kind)
			item := mustTestValue[object](t, event["item"])
			require.Equal(t, "tool_search_call", item["type"])
			require.Equal(t, "client", item["execution"])
			require.Equal(t, "tsc_native", item["id"])
			if kind == "response.output_item.done" {
				completed = item
			}
		case "response.completed":
			items := mustTestValue[[]any](t, mustTestValue[object](t, event["response"])["output"])
			require.Equal(t, []any{completed}, items)
		}
		return nil
	}))
	require.Equal(t, []string{"response.output_item.added", "response.output_item.done"}, itemEvents)
	require.Equal(t, object{"query": "read_file"}, completed["arguments"])
	require.Equal(t, native["call_id"], completed["call_id"])

	source["input"] = []any{completed, object{"type": "tool_search_output", "call_id": completed["call_id"], "execution": "client", "tools": []any{catalogReadTool("string")}}, message("user", "continue")}
	prepared, next := mustPrepare(t, source, "session", cache)
	input := mustTestValue[[]any](t, prepared["input"])
	require.Contains(t, input, native)
	_, err = next.translateCall(nativeCall(object{"name": "read_file", "arguments": object{"path": "note"}}))
	require.NoError(t, err)
	_, err = next.translateCall(nativeCall(object{"name": "read_file", "arguments": object{"path": 3}}))
	require.Error(t, err)

	source["tools"] = []any{search, object{"type": "function", "name": "tool_search"}}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	_, _, err = Prepare(raw, "session", nil)
	require.ErrorContains(t, err, "conflicting duplicate")
}

func TestClientToolSearchWithOnDemandCatalog(t *testing.T) {
	source := onDemandSource()
	search := object{"type": "tool_search", "execution": "client"}
	source["tools"] = append(source["tools"].([]any), search)
	source["input"] = []any{
		object{"type": "tool_search_output", "execution": "client", "tools": []any{catalogReadTool("string")}},
		message("user", "continue"),
	}
	_, b, stats := prepareOnDemand(t, source, nil, true)
	require.True(t, stats.Active)
	require.True(t, b.needsCatalogDisclosure(b.tools["optional_03"]))
	require.False(t, b.needsCatalogDisclosure(b.tools["tool_search"]))
	require.False(t, b.needsCatalogDisclosure(b.tools["read_file"]))
	response := schemaResponse(nativeCall(object{"name": "read_file", "arguments": object{"path": "note"}}))
	require.NoError(t, b.translateResponse(response))
	item := response["output"].([]any)[0].(object)
	require.Equal(t, "read_file", item["name"])
}
