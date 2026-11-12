package basispoints

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Checkpoints contain positions and digests, never image bytes or URLs.
type MessageImagePosition struct {
	Item, Part int
	Variant    MessageImageVariant
	Digest     string
}

type MessageImageObservation struct {
	Reason         string         `json:"reason"`
	BeforeBytes    int64          `json:"before_bytes"`
	AfterBytes     int64          `json:"after_bytes"`
	ProtectedBytes int64          `json:"protected_bytes"`
	Candidates     int            `json:"candidates"`
	Changed        int            `json:"changed"`
	Lossy          int            `json:"lossy"`
	CacheHits      int            `json:"cache_hits"`
	Attempts       int            `json:"attempts"`
	Skipped        map[string]int `json:"skipped,omitempty"`
}

// Keep the entire current user turn, even when a saved progress boundary lies
// later in that turn (e.g. an agent has already made several screenshot calls).
func MessageImageBoundary(h *ImageHistory, previous *ImageProgress) (int, string) {
	split, err := h.Split(previous)
	if err != nil {
		return 0, "boundary_unavailable"
	}
	for i := len(h.Input) - 1; i >= 0; i-- {
		item, _ := h.Input[i].(object)
		kind := text(item["type"])
		if text(item["role"]) == "user" && (kind == "" || kind == "message") {
			for i > 0 {
				prev, _ := h.Input[i-1].(object)
				if text(prev["role"]) != "user" || (text(prev["type"]) != "" && text(prev["type"]) != "message") {
					break
				}
				i--
			}
			return min(split, i), "ready"
		}
	}
	return 0, "boundary_unavailable"
}

func copyMessageHistory(h *ImageHistory) *ImageHistory {
	input := append([]any(nil), h.Input...)
	// Legacy trim replay replaces parts in place. Copy the mutable content
	// containers too, without copying immutable base64 strings or tool JSON.
	for i, raw := range input {
		item, ok := raw.(object)
		if !ok {
			continue
		}
		copyItem := object{}
		for k, v := range item {
			copyItem[k] = v
		}
		for _, field := range []string{"content", "output"} {
			if parts, ok := item[field].([]any); ok {
				copyItem[field] = append([]any(nil), parts...)
			}
		}
		input[i] = copyItem
	}
	return &ImageHistory{source: h.source, Input: input, Count: h.Count}
}

func messageImagePart(h *ImageHistory, itemIndex, partIndex int) (object, bool) {
	if itemIndex < 0 || itemIndex >= len(h.Input) || partIndex < 0 {
		return nil, false
	}
	item, _ := h.Input[itemIndex].(object)
	kind, role := text(item["type"]), text(item["role"])
	if (kind != "" && kind != "message" && kind != "agent_message") || (role != "" && role != "user" && role != "assistant") {
		return nil, false
	}
	parts, _ := item["content"].([]any)
	if partIndex >= len(parts) {
		return nil, false
	}
	part, _ := parts[partIndex].(object)
	url := text(part["image_url"])
	if text(part["type"]) != "input_image" || !strings.HasPrefix(strings.ToLower(url), "data:") {
		return nil, false
	}
	if _, exists := part["file_id"]; exists {
		return nil, false
	}
	return part, true
}

func replaceMessageImage(h *ImageHistory, position MessageImagePosition, url string) error {
	part, ok := messageImagePart(h, position.Item, position.Part)
	if !ok {
		return fmt.Errorf("invalid message image checkpoint position")
	}
	item := h.Input[position.Item].(object)
	nextItem, nextPart := object{}, object{}
	for k, v := range item {
		nextItem[k] = v
	}
	for k, v := range part {
		nextPart[k] = v
	}
	parts := append([]any(nil), item["content"].([]any)...)
	nextPart["image_url"] = url
	parts[position.Part] = nextPart
	nextItem["content"] = parts
	h.Input[position.Item] = nextItem
	return nil
}

func (c *MessageImageCache) CompressHistory(ctx context.Context, scope string, h *ImageHistory, split int, budget ImageBudget, checkpoints []ImageCheckpoint) (*ImageHistory, ImageCheckpoint, MessageImageObservation, error) {
	usage := MeasureImageInput(h.Input)
	obs := MessageImageObservation{BeforeBytes: usage.Bytes, AfterBytes: usage.Bytes, Skipped: map[string]int{}}
	cp := ImageCheckpoint{}
	if split <= 0 || split >= len(h.Input) {
		obs.Reason = "no_confirmed_history"
		return h, cp, obs, nil
	}
	obs.ProtectedBytes = MeasureImageInput(h.Input[split:]).Bytes
	if usage.Unknown > 0 {
		obs.Reason = "invalid_image"
		return h, cp, obs, nil
	}
	if usage.Bytes <= budget.MaxTotalBytes && usage.MaxBytes <= budget.MaxImageBytes {
		obs.Reason = "within_budget"
		return h, cp, obs, nil
	}
	if budget.CheckKnown(MeasureImageInput(h.Input[split:])) != nil {
		obs.Reason = "protected_turn_over_budget"
		return h, cp, obs, nil
	}
	derived := map[string]bool{}
	for _, old := range checkpoints {
		for _, p := range old.MessageImages {
			derived[p.Digest] = true
		}
	}
	// Measure each encoded image once; do not rescan tens of MiB of base64 on
	// every candidate/phase. Retain occurrence sizes, including protected tools.
	sizes := map[[2]int]int64{}
	for i, raw := range h.Input {
		item, _ := raw.(object)
		field := "content"
		switch text(item["type"]) {
		case "", "message", "agent_message":
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		parts, _ := item[field].([]any)
		for j, rawPart := range parts {
			part, _ := rawPart.(object)
			url := text(part["image_url"])
			if text(part["type"]) != "input_image" || !strings.HasPrefix(strings.ToLower(url), "data:") {
				continue
			}
			if size, ok := inlineDecodedLength(url); ok {
				sizes[[2]int{i, j}] = size
			}
		}
	}
	type candidate struct {
		position MessageImagePosition
		url      string
		size     int64
	}
	var candidates []candidate
	for i, raw := range h.Input[:split] {
		item, _ := raw.(object)
		parts, _ := item["content"].([]any)
		for j := range parts {
			part, ok := messageImagePart(h, i, j)
			if !ok {
				continue
			}
			url := text(part["image_url"])
			if derived[messageImageDigest(url)] {
				obs.Skipped["already_compressed"]++
				continue
			}
			size, ok := sizes[[2]int{i, j}]
			if !ok || size < 256<<10 {
				obs.Skipped["small_or_invalid"]++
				continue
			}
			candidates = append(candidates, candidate{MessageImagePosition{Item: i, Part: j}, url, size})
		}
	}
	obs.Candidates = len(candidates)
	if len(candidates) == 0 {
		obs.Reason = "no_candidates"
		return h, cp, obs, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].size > candidates[j].size })
	next := copyMessageHistory(h)
	currentBytes := usage.Bytes
	changed := map[[2]int]MessageImagePosition{}
	// Leave headroom for following screenshots. This is a target, not permission
	// to remove content or exceed the real per-image/count/request budgets.
	target := budget.MaxTotalBytes * 3 / 4
	for mode := 0; mode <= 1; mode++ {
		for _, item := range candidates {
			if err := ctx.Err(); err != nil {
				return h, ImageCheckpoint{}, obs, err
			}
			var maxBytes int64
			for _, size := range sizes {
				maxBytes = max(maxBytes, size)
			}
			if currentBytes <= target && maxBytes <= budget.MaxImageBytes {
				break
			}
			if obs.Attempts >= 64 {
				obs.Skipped["work_limit"]++
				break
			}
			variant := MessageImageVariant{Version: 1, Mode: mode}
			result, hit, err := c.transform(ctx, scope, item.url, variant)
			obs.Attempts++
			if hit {
				obs.CacheHits++
			}
			if err != nil {
				return h, ImageCheckpoint{}, obs, err
			}
			if result.URL == "" {
				obs.Skipped[result.Reason]++
				continue
			}
			key := [2]int{item.position.Item, item.position.Part}
			oldSize := sizes[key]
			if result.Bytes >= oldSize {
				continue
			}
			position := item.position
			position.Variant, position.Digest = variant, messageImageDigest(result.URL)
			if err := replaceMessageImage(next, position, result.URL); err != nil {
				return h, ImageCheckpoint{}, obs, err
			}
			changed[[2]int{position.Item, position.Part}] = position
			sizes[key] = result.Bytes
			currentBytes -= oldSize - result.Bytes
		}
	}
	if len(changed) == 0 {
		obs.Reason = "no_smaller_images"
		return h, cp, obs, nil
	}
	cp.Prefix = h.Progress()
	for _, p := range changed {
		cp.MessageImages = append(cp.MessageImages, p)
		if p.Variant.Mode == 1 {
			obs.Lossy++
		}
	}
	sort.Slice(cp.MessageImages, func(i, j int) bool {
		a, b := cp.MessageImages[i], cp.MessageImages[j]
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		return a.Part < b.Part
	})
	obs.Changed, obs.AfterBytes, obs.Reason = len(changed), currentBytes, "compressed"
	return next, cp, obs, nil
}

// Reconcile handles compression and existing trim/compact checkpoints in order.
// A mismatch skips an unrelated prefix; a matching but unreproducible transform
// fails closed instead of silently changing model context. Mutation is atomic.
func (c *MessageImageCache) Reconcile(ctx context.Context, scope string, h *ImageHistory, checkpoints []ImageCheckpoint) (bool, error) {
	return c.ReconcileObserved(ctx, scope, h, checkpoints, nil)
}

func (c *MessageImageCache) ReconcileObserved(ctx context.Context, scope string, h *ImageHistory, checkpoints []ImageCheckpoint, observation *MessageImageObservation) (bool, error) {
	hasImages := false
	for _, cp := range checkpoints {
		hasImages = hasImages || len(cp.MessageImages) > 0
	}
	if !hasImages {
		return h.Reconcile(checkpoints)
	}
	next := copyMessageHistory(h)
	changed := false
	for _, cp := range checkpoints {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if len(cp.MessageImages) == 0 {
			applied, err := next.Reconcile([]ImageCheckpoint{cp})
			if err != nil {
				return false, err
			}
			changed = changed || applied
			continue
		}
		n := cp.Prefix.Count
		if n <= 0 || len(cp.MessageImages) > 128 || len(cp.Trim) > 0 || cp.Split != 0 {
			return false, fmt.Errorf("invalid message image checkpoint")
		}
		if len(next.Input) < n || imageHistoryDigest(next.Input[:n]) != cp.Prefix.Digest {
			continue
		}
		for _, position := range cp.MessageImages {
			part, ok := messageImagePart(next, position.Item, position.Part)
			if !ok || position.Item >= n {
				return false, fmt.Errorf("invalid message image checkpoint position")
			}
			result, hit, err := c.transform(ctx, scope, text(part["image_url"]), position.Variant)
			if observation != nil {
				observation.Attempts++
				if hit {
					observation.CacheHits++
				}
			}
			if err != nil {
				return false, err
			}
			if result.URL == "" || messageImageDigest(result.URL) != position.Digest {
				return false, fmt.Errorf("message image checkpoint could not be reproduced; compact manually")
			}
			if err := replaceMessageImage(next, position, result.URL); err != nil {
				return false, err
			}
			if observation != nil {
				observation.Changed++
			}
		}
		changed = true
	}
	if changed {
		h.Input, h.Count = next.Input, CountInlineImages(next.Input)
	}
	return changed, nil
}

// Coalesce adjacent compression receipts, avoiding one checkpoint per turn.
// Only replace the old receipt when replay proves the merged result identical.
func (c *MessageImageCache) AppendCheckpoint(ctx context.Context, scope string, original, result *ImageHistory, previous []ImageCheckpoint, added ImageCheckpoint) []ImageCheckpoint {
	appended := append(append([]ImageCheckpoint(nil), previous...), added)
	if len(previous) == 0 || len(previous[len(previous)-1].MessageImages) == 0 {
		return appended
	}
	last := previous[len(previous)-1]
	base := copyMessageHistory(original)
	if _, err := c.Reconcile(ctx, scope, base, previous[:len(previous)-1]); err != nil {
		return appended
	}
	n := last.Prefix.Count
	if n <= 0 || len(base.Input) < n || imageHistoryDigest(base.Input[:n]) != last.Prefix.Digest {
		return appended
	}
	merged := ImageCheckpoint{Prefix: base.Progress(), MessageImages: append(append([]MessageImagePosition(nil), last.MessageImages...), added.MessageImages...)}
	if len(merged.MessageImages) > 128 {
		return appended
	}
	check := copyMessageHistory(base)
	if _, err := c.Reconcile(ctx, scope, check, []ImageCheckpoint{merged}); err != nil || check.Progress() != result.Progress() {
		return appended
	}
	return append(append([]ImageCheckpoint(nil), previous[:len(previous)-1]...), merged)
}
