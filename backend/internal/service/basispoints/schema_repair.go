package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type schemaRepairCall struct {
	native    object
	target    string
	arguments object
	issue     *ArgumentObservation
}
type schemaRepairIssue struct {
	Index      int                  `json:"call_index"`
	Diagnostic *ArgumentObservation `json:"diagnostic"`
}
type schemaRepairFeedback struct{ Issues []schemaRepairIssue }

// Preserve progressive counters for schema correction when a failed terminal
// omits them or reports zeros. Existing transport reader semantics stay intact.
func ReadClientToolRepairResponse(reader io.Reader, validation error) (map[string]any, error) {
	_, schema := validation.(*schemaRepairFeedback)
	return readToolRepairResponse(reader, schema)
}

func (*schemaRepairFeedback) Error() string {
	return "basispoints client tool arguments failed schema validation; no tool in this batch was executed"
}

// Compatible upstreams may include text in a lifecycle response snapshot,
// without emitting a delta. It has still crossed the downstream stream boundary.
func schemaRepairVisibleContent(ctx context.Context, payload object) bool {
	if schemaSession(ctx) == nil {
		return false
	}
	response, _ := payload["response"].(object)
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(object)
		if hasVisibleStreamContent("response.output_item.done", object{"item": item}) {
			return true
		}
	}
	return false
}

// This deliberately supports only explicit ordinary FUNCTION envelopes. Native
// direct calls, raw-code transports, custom tools and inferred targets stay out.
func (b *Bridge) schemaRepairBatch(response object) ([]schemaRepairCall, string) {
	if text(response["status"]) != "completed" {
		return nil, "incomplete_response"
	}
	if b.structured != nil {
		return nil, "structured_output"
	}
	if b.hasToolHistory && len(b.completedToolHistory) == 0 {
		return nil, "incomplete_tool_history"
	}
	items, ok := repairableTools(response)
	if !ok || (b.disallowParallel && len(items) > 1) {
		return nil, "unsafe_tool_batch"
	}
	ids := map[string]bool{}
	result := make([]schemaRepairCall, 0, len(items))
	check := *b
	check.replay = nil
	for _, item := range items {
		id, call := text(item["id"]), text(item["call_id"])
		if id == "" || ids[id] || b.completedToolHistory["item/"+id] || b.completedToolHistory["call/"+call] {
			return nil, "unsafe_tool_identity"
		}
		if status := text(item["status"]); status != "" && status != "completed" {
			return nil, "incomplete_tool_call"
		}
		ids[id] = true
		transport := transportArguments(item)
		summary := text(transport["summary"])
		for _, prefix := range []string{customTransportPrefix, functionCodeTransportPrefix, functionCmdTransportPrefix} {
			if strings.HasPrefix(summary, prefix) {
				return nil, "raw_payload_transport"
			}
		}
		envelope, err := decodeTransportEnvelope(transport["code"])
		if err != nil {
			return nil, "ambiguous_tool_target"
		}
		target, err := envelopeName(envelope)
		if err != nil || target == "" {
			return nil, "ambiguous_tool_target"
		}
		info, exists := b.tools[target]
		if !exists || info.Kind != "function" {
			return nil, "unsupported_tool_target"
		}
		arguments, err := envelopeArguments(envelope)
		if err != nil {
			return nil, "incomplete_arguments"
		}
		if raw, ok := arguments.(string); ok {
			if decode([]byte(raw), &arguments) != nil {
				return nil, "incomplete_arguments"
			}
		}
		args, ok := arguments.(object)
		if !ok || args == nil {
			return nil, "incomplete_arguments"
		}
		if validateToolNumberBudget(args) != nil {
			return nil, "resource_limit"
		}
		if schemaRepairPayloadTool(target) || schemaRepairPayloadKeys(args, 0) || schemaRepairPayloadKeys(info.Parameters, 0) {
			return nil, "raw_payload_contract"
		}
		_, err = check.finishClientToolCall(item, info, envelope, false, true)
		var issue *ArgumentObservation
		if err != nil {
			var schemaErr toolArgumentsSchemaError
			if !errors.As(err, &schemaErr) || schemaErr.diagnostic == nil {
				return nil, "non_schema_failure"
			}
			if schemaErr.diagnostic.Reason != "schema_validation" {
				return nil, "resource_limit"
			}
			issue = schemaErr.diagnostic
		}
		result = append(result, schemaRepairCall{item, target, args, issue})
	}
	return result, ""
}

func schemaRepairPayloadTool(name string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '.' || r == '-' || r == '/' }) {
		switch part {
		case "exec", "execute", "run", "shell", "bash", "terminal", "python", "node", "patch", "sql", "script", "write", "edit":
			return true
		}
	}
	return false
}
func schemaRepairPayloadKeys(value any, depth int) bool {
	if depth > 128 {
		return true
	}
	switch node := value.(type) {
	case object:
		for key, v := range node {
			switch strings.ToLower(key) {
			case "code", "cmd", "command", "commands", "script", "scripts", "patch", "diff", "sql", "query", "source", "source_code", "program", "input", "text", "content", "body":
				return true
			}
			if schemaRepairPayloadKeys(v, depth+1) {
				return true
			}
		}
	case []any:
		for _, v := range node {
			if schemaRepairPayloadKeys(v, depth+1) {
				return true
			}
		}
	}
	return false
}

func (b *Bridge) translateCompletedWithSchema(ctx context.Context, response object, repair ToolRepairFunc, visible bool) error {
	s := schemaSession(ctx)
	if s == nil {
		return b.translateCompleted(ctx, response, repair)
	}
	validation := b.validateToolResponse(response)
	var schemaErr toolArgumentsSchemaError
	if !errors.As(validation, &schemaErr) {
		return b.translateCompleted(ctx, response, repair)
	}
	s.captureUsage(response["usage"])
	skip := func(reason string) error { s.record(false, "skipped", reason); return validation }
	if visible {
		return skip("visible_content")
	}
	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if blocked != "" {
		return skip(blocked)
	}
	if err := ctx.Err(); err != nil {
		s.record(false, "skipped", "canceled")
		return err
	}
	if repair == nil {
		return skip("repair_unavailable")
	}
	original, reason := b.schemaRepairBatch(response)
	if reason != "" {
		return skip(reason)
	}
	feedback := &schemaRepairFeedback{}
	for i, call := range original {
		if call.issue != nil {
			feedback.Issues = append(feedback.Issues, schemaRepairIssue{i, call.issue})
		}
	}
	if len(feedback.Issues) == 0 {
		return skip("non_schema_failure")
	}
	s.mu.Lock()
	exhausted := s.observation.TotalCorrections >= maxToolRepairs || s.observation.Attempted
	s.mu.Unlock()
	if exhausted {
		return skip("repair_budget_exhausted")
	}
	repairCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	s.record(true, "failed", "")
	corrected, err := repair(repairCtx, response, feedback)
	response["usage"] = addRepairUsage(addRepairUsage(nil, response["usage"]), corrected["usage"])
	s.captureUsage(response["usage"])
	if ctx.Err() != nil {
		s.record(true, "canceled", "")
		return ctx.Err()
	}
	if repairCtx.Err() != nil {
		s.record(true, "timed_out", "")
		// A local correction deadline must emit a terminal with accumulated usage;
		// it is not a cancellation of the client's still-live request.
		return fmt.Errorf("basispoints schema correction timed out; no tool was executed")
	}
	if err != nil {
		return fmt.Errorf("basispoints schema correction failed; no tool was executed")
	}
	items, reason := b.schemaRepairBatch(corrected)
	if reason != "" || len(items) != len(original) {
		return fmt.Errorf("basispoints schema correction returned an unsafe tool batch; no tool was executed")
	}
	oldIDs, oldCalls := map[string]bool{}, map[string]bool{}
	for _, before := range original {
		oldIDs[text(before.native["id"])] = true
		oldCalls[text(before.native["call_id"])] = true
	}
	for i, after := range items {
		before := original[i]
		if after.issue != nil {
			return fmt.Errorf("basispoints schema correction remains invalid; no tool was executed: %w", toolArgumentsSchemaError{diagnostic: after.issue})
		}
		if after.target != before.target || oldIDs[text(after.native["id"])] || oldCalls[text(after.native["call_id"])] {
			return fmt.Errorf("basispoints schema correction changed a target or reused an identity; no tool was executed")
		}
		if before.issue == nil && quoted(before.arguments) != quoted(after.arguments) {
			return fmt.Errorf("basispoints schema correction changed an already valid call; no tool was executed")
		}
	}
	// Validate and stage the complete replacement before touching the original
	// response. No corrected tool events are emitted until this succeeds.
	combined := make(object, len(response))
	for key, value := range response {
		combined[key] = value
	}
	output, _ := response["output"].([]any)
	next := append([]any{}, output...)
	index := 0
	for i, raw := range next {
		item, _ := raw.(object)
		if isTool(item) {
			next[i] = items[index].native
			index++
		}
	}
	combined["output"] = next
	if ctx.Err() != nil {
		s.record(true, "canceled", "")
		return ctx.Err()
	}
	if repairCtx.Err() != nil {
		s.record(true, "timed_out", "")
		return fmt.Errorf("basispoints schema correction timed out; no tool was executed")
	}
	if err := b.translateResponse(combined); err != nil {
		return err
	}
	for key, value := range combined {
		response[key] = value
	}
	s.record(true, "succeeded", "")
	return nil
}

// BuildSchemaRepairRequest reuses the existing continuation envelope and only
// replaces its appended transport feedback. Routing metadata, history expansion,
// iteration increments and compaction-trigger placement remain upstream-owned.
func buildSchemaRepairRequest(prepared []byte, failed object, feedback *schemaRepairFeedback) ([]byte, error) {
	raw, err := BuildToolRepairRequest(prepared, failed, errors.New(feedback.Error()))
	if err != nil {
		return nil, err
	}
	var request object
	if decode(raw, &request) != nil {
		return nil, fmt.Errorf("invalid schema correction request")
	}
	input, _ := request["input"].([]any)
	last := len(input) - 1
	if last >= 0 {
		item, _ := input[last].(object)
		if text(item["type"]) == "compaction_trigger" {
			last--
		}
	}
	items, ok := repairableTools(failed)
	if !ok || last < len(items) {
		return nil, fmt.Errorf("invalid schema correction feedback boundary")
	}
	issues := make(map[int]schemaRepairIssue, len(feedback.Issues))
	for _, issue := range feedback.Issues {
		issues[issue.Index] = issue
	}
	for i := last - len(items); i < last; i++ {
		item, _ := input[i].(object)
		// One diagnostic per call keeps feedback linear in batch size. Repeating
		// the full issue list in every result would create quadratic requests.
		result := object{"executed": false, "schema_valid": true, "message": "Another call failed Schema validation; preserve these arguments unchanged."}
		if issue, invalid := issues[i-(last-len(items))]; invalid {
			result = object{"executed": false, "error": object{"code": "invalid_client_tool_arguments", "issues": []schemaRepairIssue{issue}}}
		}
		encoded, _ := json.Marshal(result)
		item["output"] = string(encoded)
	}
	input[last] = message("developer", fmt.Sprintf("The preceding batch was NOT executed. Correct its invalid JSON Schema arguments using the structured validation feedback and current client catalog. Return exactly %d ordinary FUNCTION run_officejs calls in the same order and for the same targets, using new call identities. Keep every already valid call's arguments unchanged. Do not add operations, rewrite code/commands/patches, execute Office code, or repeat commentary. The entire batch will be validated again before dispatch.", len(items)))
	return json.Marshal(request)
}
