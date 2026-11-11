package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func budgetImage(size int) object {
	return object{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, size))}
}
func budgetHistory(t *testing.T, input []any) *ImageHistory {
	t.Helper()
	raw, _ := json.Marshal(object{"input": input})
	h, err := InspectImageHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestImageBudgetCountsBytesAndOccurrences(t *testing.T) {
	h := budgetHistory(t, []any{object{"content": []any{budgetImage(4), budgetImage(4)}}, object{"type": "function_call_output", "output": []any{budgetImage(2)}}})
	u := MeasureImageInput(h.Input)
	if u.Count != 3 || u.Bytes != 10 || u.MaxBytes != 4 {
		t.Fatal(u)
	}
	budget := ImageBudget{MaxImages: 3, MaxImageBytes: 4, MaxTotalBytes: 10}
	if err := budget.Check(h.Input); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		b    ImageBudget
		kind string
	}{{ImageBudget{2, 4, 10}, "count"}, {ImageBudget{3, 3, 10}, "single_bytes"}, {ImageBudget{3, 4, 9}, "total_bytes"}} {
		var failure *ImageBudgetError
		if !errors.As(tc.b.Check(h.Input), &failure) || failure.Kind != tc.kind {
			t.Fatal(tc)
		}
	}
	h.Input = append(h.Input, object{"content": []any{object{"type": "input_image", "image_url": "data:image/png;base64,broken"}}})
	if budget.Check(h.Input) == nil {
		t.Fatal("unknown payload counted as zero")
	}
}

func TestTrimOldToolImagesProtectsUserAndCurrentTurn(t *testing.T) {
	input := []any{object{"role": "user", "content": []any{budgetImage(3)}}}
	for i := 0; i < 5; i++ {
		id := fmt.Sprint(i)
		input = append(input, object{"type": "function_call", "name": "screenshot", "call_id": id, "arguments": "{}"}, object{"type": "function_call_output", "call_id": id, "output": []any{object{"type": "input_text", "text": "keep tool text"}, budgetImage(4)}})
	}
	split := len(input)
	input = append(input, object{"type": "custom_tool_call", "name": "screenshot", "call_id": "new", "input": "x"}, object{"type": "custom_tool_call_output", "call_id": "new", "output": []any{budgetImage(3), budgetImage(3)}})
	h := budgetHistory(t, input)
	before := quoted(h.Input)
	next, cp, err := h.TrimOldToolImages(split, ImageBudget{20, 20, 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Trim) != 4 || next.Count != 4 {
		t.Fatalf("expected batch of four: %+v count=%d", cp, next.Count)
	}
	if quoted(h.Input) != before || !reflect.DeepEqual(next.Input[0], h.Input[0]) || !reflect.DeepEqual(next.Input[split:], h.Input[split:]) {
		t.Fatal("source, user images or new tail changed")
	}
	if !strings.Contains(quoted(next.Input), "keep tool text") || !strings.Contains(quoted(next.Input), trimmedToolImageText) {
		t.Fatal("tool output structure lost")
	}
	// Echo the original client history plus new input: the digest checkpoint
	// reapplies exactly the same removals, including while the policy is off.
	replay := budgetHistory(t, append(append([]any{}, h.Input...), message("user", "continue")))
	changed, err := replay.Reconcile([]ImageCheckpoint{cp})
	if err != nil || !changed || !reflect.DeepEqual(replay.Input[:len(next.Input)], next.Input) {
		t.Fatalf("replay mismatch: %v", err)
	}
	changed, err = replay.Reconcile([]ImageCheckpoint{cp})
	if err != nil || changed {
		t.Fatal("trim is not idempotent")
	}
	foreign := budgetHistory(t, append([]any{message("user", "other conversation")}, h.Input...))
	if changed, err := foreign.Reconcile([]ImageCheckpoint{cp}); err != nil || changed {
		t.Fatal("checkpoint crossed history")
	}
}

func TestTrimHistoryThenCompactReconcilesInOrder(t *testing.T) {
	h := budgetHistory(t, []any{object{"type": "function_call", "name": "shot", "call_id": "old", "arguments": "{}"}, object{"type": "function_call_output", "call_id": "old", "output": []any{budgetImage(8)}}, message("assistant", "seen"), object{"role": "user", "content": []any{budgetImage(2)}}})
	trimmed, trim, err := h.TrimOldToolImages(3, ImageBudget{20, 20, 3})
	if err != nil {
		t.Fatal(err)
	}
	window := []any{object{"type": "compaction", "encrypted_content": "opaque"}}
	compact, err := trimmed.Checkpoint(3, window)
	if err != nil {
		t.Fatal(err)
	}
	replay := budgetHistory(t, append(append(append([]any{}, h.Input...), window...), message("user", "continue")))
	if changed, err := replay.Reconcile([]ImageCheckpoint{trim, compact}); err != nil || !changed {
		t.Fatal(err)
	}
	if len(replay.Input) != 3 || replay.Count != 1 || quoted(replay.Input[1]) != quoted(h.Input[3]) {
		t.Fatal("ordered transforms lost the new image")
	}
}

func TestTrimRefusesAmbiguousOrMissingToolCalls(t *testing.T) {
	for _, calls := range [][]any{nil, {object{"type": "function_call", "call_id": "x"}, object{"type": "function_call", "call_id": "x"}}} {
		input := append(append([]any{}, calls...), object{"type": "function_call_output", "call_id": "x", "output": []any{budgetImage(10)}}, message("user", "new"))
		h := budgetHistory(t, input)
		if _, _, err := h.TrimOldToolImages(len(input)-1, ImageBudget{20, 20, 1}); err == nil {
			t.Fatal("ambiguous old screenshot removed")
		}
	}
}

func TestNativeImageBudgetIsIndependentAndKeepsMediaValidation(t *testing.T) {
	// Existing PNG fixture plus trailing bytes has a valid header; image size is
	// measured over the complete payload, not inferred from pixel dimensions.
	_, payload := nativeTestURL(t)
	payload = append(payload, make([]byte, (1<<20)-len(payload))...)
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
	raw := nativeTestRequest(t, url, url)
	before := bytes.Clone(raw)
	if _, err := PrepareNativeImagesWithBudget(raw, 20, 1); err == nil {
		t.Fatal("configured budget ignored")
	}
	if _, err := PrepareNativeImagesWithBudget(raw, 20, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNativeImages(raw); err != nil {
		t.Fatal("legacy default changed", err)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("input mutated")
	}
	if _, err := PrepareNativeImagesWithBudget(nativeTestRequest(t, "data:image/png;base64,AQID"), 20, 64); err == nil {
		t.Fatal("media validation bypassed")
	}
}
