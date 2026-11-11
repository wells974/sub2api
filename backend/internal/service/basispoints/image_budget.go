package basispoints

import (
	"fmt"
	"strings"
)

// ImageBudget applies to each upstream window, not to the concatenation of a
// compaction request and its continuation. It never replaces media validation.
type ImageBudget struct {
	MaxImages     int
	MaxImageBytes int64
	MaxTotalBytes int64
}

type ImageUsage struct {
	Count           int
	Bytes, MaxBytes int64
	Unknown         int
}

type ImageBudgetError struct {
	Kind          string
	Actual, Limit int64
}

func (e *ImageBudgetError) Error() string {
	return fmt.Sprintf("basispoints image budget exceeded: %s=%d limit=%d", e.Kind, e.Actual, e.Limit)
}

func MeasureImageInput(input []any) ImageUsage {
	var usage ImageUsage
	for _, raw := range input {
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
		for _, rawPart := range parts {
			part, _ := rawPart.(object)
			url := text(part["image_url"])
			if text(part["type"]) != "input_image" || len(url) < 5 || !strings.EqualFold(url[:5], "data:") {
				continue
			}
			usage.Count++
			size, ok := inlineDecodedLength(url)
			if !ok {
				usage.Unknown++
				continue
			}
			usage.Bytes += size
			if size > usage.MaxBytes {
				usage.MaxBytes = size
			}
		}
	}
	return usage
}

// CheckKnown bounds measurable payloads. Unknown data still passes through the
// original native/relay validator before any upload; it is never made valid here.
func (b ImageBudget) CheckKnown(u ImageUsage) error {
	if u.Count > b.MaxImages {
		return &ImageBudgetError{"count", int64(u.Count), int64(b.MaxImages)}
	}
	if u.MaxBytes > b.MaxImageBytes {
		return &ImageBudgetError{"single_bytes", u.MaxBytes, b.MaxImageBytes}
	}
	if u.Bytes > b.MaxTotalBytes {
		return &ImageBudgetError{"total_bytes", u.Bytes, b.MaxTotalBytes}
	}
	return nil
}

func (b ImageBudget) Check(input []any) error {
	u := MeasureImageInput(input)
	if u.Unknown > 0 {
		return fmt.Errorf("basispoints inline image contains unmeasurable base64 data")
	}
	return b.CheckKnown(u)
}
