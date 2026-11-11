package basispoints

import (
	"fmt"
	"strings"
)

// Only positions and a prefix digest persist. Original content is never cached.
type ImageTrimPosition struct{ Item, Part int }

const trimmedToolImageText = "[Earlier tool screenshot omitted by the configured image history policy. Its visual content is no longer available.]"

// TrimOldToolImages replaces only inline images in complete historical tool
// results, before split. Work on a copy so unsuccessful planning is atomic.
// Groups of four reduce repeated changes to the history prefix.
func (h *ImageHistory) TrimOldToolImages(split int, target ImageBudget) (*ImageHistory, ImageCheckpoint, error) {
	if split <= 0 || split >= len(h.Input) {
		return nil, ImageCheckpoint{}, fmt.Errorf("image history boundary unavailable; no screenshots removed")
	}
	if MeasureImageInput(h.Input).Unknown > 0 {
		return nil, ImageCheckpoint{}, fmt.Errorf("invalid image data; no screenshots removed")
	}
	raw, err := h.Body()
	if err != nil {
		return nil, ImageCheckpoint{}, err
	}
	next, err := InspectImageHistory(raw)
	if err != nil {
		return nil, ImageCheckpoint{}, err
	}
	calls, outputs := map[string]int{}, map[string]int{}
	callIndex, callKind := map[string]int{}, map[string]string{}
	for i, raw := range h.Input[:split] {
		item, _ := raw.(object)
		id, kind := text(item["call_id"]), text(item["type"])
		if id == "" {
			continue
		}
		switch kind {
		case "function_call", "custom_tool_call":
			calls[id]++
			callIndex[id] = i
			callKind[id] = kind
		case "function_call_output", "custom_tool_call_output":
			outputs[id]++
		}
	}
	var candidates []ImageTrimPosition
	for i, raw := range h.Input[:split] {
		item, _ := raw.(object)
		id, kind := text(item["call_id"]), text(item["type"])
		if calls[id] != 1 || outputs[id] != 1 || callIndex[id] >= i || kind != callKind[id]+"_output" {
			continue
		}
		parts, _ := item["output"].([]any)
		for j, rawPart := range parts {
			part, _ := rawPart.(object)
			if text(part["type"]) == "input_image" {
				url := text(part["image_url"])
				if len(url) < 5 || !strings.EqualFold(url[:5], "data:") {
					continue
				}
				if _, ok := inlineDecodedLength(url); ok {
					candidates = append(candidates, ImageTrimPosition{i, j})
					if len(candidates) > MaxRelayImages {
						return nil, ImageCheckpoint{}, fmt.Errorf("too many historical screenshots for one trim checkpoint; compact manually")
					}
				}
			}
		}
	}
	cp := ImageCheckpoint{Prefix: h.Progress()}
	for i, position := range candidates {
		if err := replaceTrimmedImage(next.Input, position); err != nil {
			return nil, ImageCheckpoint{}, err
		}
		cp.Trim = append(cp.Trim, position)
		if (i+1)%4 == 0 || i+1 == len(candidates) {
			if target.CheckKnown(MeasureImageInput(next.Input)) == nil {
				break
			}
		}
	}
	if len(cp.Trim) == 0 {
		return nil, ImageCheckpoint{}, fmt.Errorf("no completed historical tool screenshots can be removed")
	}
	next.Count = CountInlineImages(next.Input)
	return next, cp, nil
}

func replaceTrimmedImage(input []any, pos ImageTrimPosition) error {
	if pos.Item < 0 || pos.Item >= len(input) {
		return fmt.Errorf("invalid image trim checkpoint")
	}
	item, _ := input[pos.Item].(object)
	if text(item["type"]) != "function_call_output" && text(item["type"]) != "custom_tool_call_output" {
		return fmt.Errorf("invalid image trim checkpoint")
	}
	parts, _ := item["output"].([]any)
	if pos.Part < 0 || pos.Part >= len(parts) {
		return fmt.Errorf("invalid image trim checkpoint")
	}
	part, _ := parts[pos.Part].(object)
	if text(part["type"]) != "input_image" {
		return fmt.Errorf("invalid image trim checkpoint")
	}
	parts[pos.Part] = object{"type": "input_text", "text": trimmedToolImageText}
	return nil
}

func (h *ImageHistory) reconcileTrim(cp ImageCheckpoint) (bool, error) {
	if cp.Prefix.Count <= 0 {
		return false, fmt.Errorf("invalid image trim checkpoint")
	}
	if len(h.Input) < cp.Prefix.Count || imageHistoryDigest(h.Input[:cp.Prefix.Count]) != cp.Prefix.Digest {
		return false, nil
	}
	for _, pos := range cp.Trim {
		if pos.Item >= cp.Prefix.Count {
			return false, fmt.Errorf("invalid image trim checkpoint")
		}
		if err := replaceTrimmedImage(h.Input, pos); err != nil {
			return false, err
		}
	}
	return true, nil
}
