//go:build bps_request_integration

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the upstream request lease with our Schema repair callback, which
// consumes and validates the repair response itself rather than replaying it.
func TestExcelBPSRequestProxySchemaRepairKeepsExit(t *testing.T) {
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			observed := make(chan requestProxyObservation, 4)
			var calls atomic.Int32
			invalid := excelBPSRepairWire(t, "initial", "Run", `{"name":"lookup","arguments":{"count":"invalid"}}`)
			valid := excelBPSRepairWire(t, "repaired", "Run", `{"name":"lookup","arguments":{"count":1}}`)
			svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				observed <- requestProxyObservation{id: id, path: r.URL.Path, body: raw}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					_, _ = io.WriteString(w, invalid)
				} else {
					_, _ = io.WriteString(w, valid)
				}
			})
			code, response, err := forwardLocalBPSRequest(t.Context(), svc, account, schemaGatewayRequest(stream))
			require.NoError(t, err, response)
			require.Equal(t, 200, code)
			terminal := catalogGatewayTerminal(t, response, stream)
			require.Equal(t, "lookup", terminal.Get("output.0.name").String())
			require.JSONEq(t, `{"count":1}`, terminal.Get("output.0.arguments").String())
			first, repair := <-observed, <-observed
			require.Equal(t, first.id, repair.id)
			require.Contains(t, string(repair.body), "invalid_client_tool_arguments")
			require.False(t, gjson.GetBytes(repair.body, "service_tier").Exists())
			require.EqualValues(t, 2, calls.Load())
			require.Empty(t, observed)
		})
	}
}

// Direct search must become native continuation history while the model,
// search HTTP request and deferred tool disclosure all retain one proxy lease.
func TestExcelBPSRequestProxyDirectSearchCatalogKeepsExit(t *testing.T) {
	t.Setenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED", "true")
	t.Setenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED", "true")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			observed := make(chan requestProxyObservation, 5)
			var calls atomic.Int32
			search := excelSearchResponse(map[string]any{
				"id": "resp_search", "status": "completed", "output": []any{map[string]any{
					"type": "function_call", "id": "fc_search", "call_id": "call_search",
					"name": "__sub2api_web_search", "arguments": `{"query":"synthetic query"}`,
				}},
			})
			searchWire, err := io.ReadAll(search.Body)
			require.NoError(t, err)
			_ = search.Body.Close()
			disclosure := excelBPSRepairWire(t, "disclosure", "Run", `{"name":"optional_00","arguments":{"count":1}}`)
			invalid := excelBPSRepairWire(t, "invalid", "Run", `{"name":"optional_00","arguments":{"count":"PRIVATE_INVALID"}}`)
			repaired := excelBPSRepairWire(t, "repaired", "Run", `{"name":"optional_00","arguments":{"count":2}}`)
			svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				observed <- requestProxyObservation{id: id, path: r.URL.Path, body: raw}
				switch calls.Add(1) {
				case 1:
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(searchWire)
				case 2:
					_, _ = io.WriteString(w, `{"output":"found","results":[{"url":"https://example.test","title":"Fixture"}]}`)
				case 3:
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, disclosure)
				case 4:
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, invalid)
				case 5:
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, repaired)
				default:
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			account.Extra[ExcelBPSWebSearchKey] = true
			account.Extra[ExcelBPSWebSearchAutoKey] = true
			source := onDemandGatewaySource(stream)
			body, err := json.Marshal(source)
			require.NoError(t, err)
			code, response, err := forwardLocalBPSRequest(t.Context(), svc, account, body)
			require.NoError(t, err, response)
			require.Equal(t, 200, code)
			terminal := catalogGatewayTerminal(t, response, stream)
			require.Equal(t, "web_search_call", terminal.Get("output.0.type").String())
			require.Equal(t, "js", terminal.Get("output.1.name").String())
			first, query, next := <-observed, <-observed, <-observed
			require.Equal(t, first.id, query.id)
			require.Equal(t, first.id, next.id)
			require.Equal(t, "/backend-api/codex/alpha/search", query.path)
			nativeSearchFound := false
			for _, item := range gjson.GetBytes(next.body, "input").Array() {
				if item.Get("call_id").String() == "call_search" && item.Get("type").String() == "function_call" {
					require.Equal(t, "run_officejs", item.Get("name").String())
					nativeSearchFound = true
				}
			}
			require.True(t, nativeSearchFound)
			require.EqualValues(t, 3, calls.Load(), "search must not enter Schema repair")
			require.Empty(t, observed)

			// Replay the completed search and literal documentation through the
			// client boundary. Once search is disabled, the disclosed tool must
			// regain ordinary Schema correction without leaking the private tool.
			docsCall := terminal.Get("output.1")
			printCode := gjson.Get(docsCall.Get("arguments").String(), "code").String()
			require.True(t, strings.HasPrefix(printCode, "nodeRepl.write("))
			var docs string
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(printCode, "nodeRepl.write("), ");")), &docs))
			history := []any{map[string]any{"role": "user", "content": "search then use the optional tool"}}
			for _, item := range terminal.Get("output").Array() {
				var value map[string]any
				require.NoError(t, json.Unmarshal([]byte(item.Raw), &value))
				history = append(history, value)
			}
			source["input"] = append(history, map[string]any{"type": "function_call_output", "call_id": docsCall.Get("call_id").String(), "output": docs})
			account.Extra[ExcelBPSWebSearchKey] = false
			account.Extra[ExcelBPSWebSearchAutoKey] = false
			body, err = json.Marshal(source)
			require.NoError(t, err)
			code, response, err = forwardLocalBPSRequest(t.Context(), svc, account, body)
			require.NoError(t, err, response)
			require.Equal(t, 200, code)
			terminal = catalogGatewayTerminal(t, response, stream)
			require.Len(t, terminal.Get("output").Array(), 1)
			require.Equal(t, "optional_00", terminal.Get("output.0.name").String())
			require.JSONEq(t, `{"count":2}`, terminal.Get("output.0.arguments").String())
			require.NotContains(t, response, "PRIVATE_INVALID")
			require.EqualValues(t, 20, terminal.Get("usage.input_tokens").Int())
			require.EqualValues(t, 4, terminal.Get("usage.output_tokens").Int())
			require.EqualValues(t, 5, calls.Load(), "one correction after disclosure, no search replay")
			generation, correction := <-observed, <-observed
			require.Equal(t, generation.id, correction.id)
			require.Contains(t, string(correction.body), "invalid_client_tool_arguments")
			require.NotContains(t, string(generation.body), "__sub2api_web_search")
			require.NotContains(t, string(correction.body), "__sub2api_web_search")
			require.Empty(t, observed)
		})
	}
}
