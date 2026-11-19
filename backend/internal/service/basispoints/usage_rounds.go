package basispoints

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// Each group represents one measurement, in gateway billing precedence order.
// Keep the wire spellings already present: clients may read either dialect.
var roundCacheWriteAliases = []string{
	"input_tokens_details.cache_creation_input_tokens", "prompt_tokens_details.cache_creation_input_tokens",
	"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens",
	"input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens",
	"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens",
}

var inputAliases = []string{"input_tokens", "prompt_tokens"}
var outputAliases = []string{"output_tokens", "completion_tokens"}

var roundUsageAliases = [][]string{
	inputAliases, outputAliases,
	{"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cache_read_input_tokens", "cache_read_tokens", "cached_tokens", "prompt_cache_hit_tokens"},
	roundCacheWriteAliases,
}

func usageField(root object, path string) (object, string) {
	for {
		key, rest, nested := strings.Cut(path, ".")
		if !nested {
			return root, key
		}
		root, _ = root[key].(object)
		path = rest
	}
}

func usageInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		i := int64(n)
		return i, float64(i) == n
	}
	return 0, false
}

// Response detail fields may explicitly report zero. Legacy aggregate aliases
// are fallbacks and only positive values take precedence among them.
func roundCounter(usage object, paths []string) *big.Int {
	for _, path := range paths {
		parent, key := usageField(usage, path)
		value, exists := parent[key]
		if !exists {
			continue
		}
		if n := tokenCount(value); n != nil && n.Sign() > 0 {
			return n
		}
		if strings.Contains(path, ".") {
			break
		}
	}
	return new(big.Int)
}

func tokenCount(value any) *big.Int {
	if value == nil {
		return nil
	}
	n, ok := new(big.Int).SetString(fmt.Sprint(value), 10)
	if !ok || n.Sign() < 0 {
		return nil
	}
	return n
}

// TTL buckets must reconcile within each generation. An over-report in one
// round cannot compensate for missing evidence in another round.
func roundWriteTTL(usage object) (short, long int64, valid bool) {
	count := roundCounter(usage, roundCacheWriteAliases)
	if !count.IsInt64() {
		return 0, 0, false
	}
	writes := count.Int64()
	for _, path := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details, _ := usage[path].(object)
		raw, exists := details["cache_write_tokens_details"]
		if !exists {
			continue
		}
		ttl, _ := raw.(object)
		a, okA := usageInt(ttl["ttl_30m_tokens"])
		b, okB := usageInt(ttl["ttl_12h_tokens"])
		return a, b, okA && okB && a >= 0 && b >= 0 && a <= writes && b == writes-a
	}
	return 0, 0, writes == 0
}

func addRepairUsage(total object, value any) object {
	usage, _ := value.(object)
	if usage == nil {
		return total
	}
	result := object{}
	mergeUsageFields(result, total, sumTokens)
	mergeUsageFields(result, usage, sumTokens)
	if len(total) > 0 && len(usage) > 0 {
		reconcileUsageAliases(result, total, usage, sumTokens)
		if !reconcileReportedTotal(result, total, usage, sumTokens) {
			delete(result, "total_tokens")
		}
	}
	a, b, firstOK := roundWriteTTL(total)
	c, d, nextOK := roundWriteTTL(usage)
	setUsageTTL(result, a+c, b+d, firstOK && nextOK)
	return result
}

func setUsageTTL(result object, short, long int64, valid bool) {
	for _, path := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details, _ := result[path].(object)
		if _, exists := details["cache_write_tokens_details"]; exists {
			if valid {
				details["cache_write_tokens_details"] = object{"ttl_30m_tokens": short, "ttl_12h_tokens": long}
			} else {
				delete(details, "cache_write_tokens_details")
			}
		}
	}
}

// Responses envelopes may report usage beside the response. Use the same
// precedence as the gateway when both locations are present.
func eventUsage(event, response object) object {
	if usage, ok := event["usage"].(object); ok {
		return usage
	}
	usage, _ := response["usage"].(object)
	return usage
}

func hasBilledUsage(usage object) bool {
	for _, paths := range roundUsageAliases {
		if roundCounter(usage, paths).Sign() > 0 {
			return true
		}
	}
	for _, path := range []string{"input_tokens_details.image_tokens", "prompt_tokens_details.image_tokens", "output_tokens_details.image_tokens", "completion_tokens_details.image_tokens"} {
		parent, key := usageField(usage, path)
		if n := tokenCount(parent[key]); n != nil && n.Sign() > 0 {
			return true
		}
	}
	return false
}

// A nonzero terminal is authoritative. A missing/all-zero terminal must
// not erase observed work before this generation is added to a correction.
func restoreRoundUsage(response, observed object) {
	current, _ := response["usage"].(object)
	if len(observed) > 0 && !hasBilledUsage(current) && hasBilledUsage(observed) {
		response["usage"] = observed
	}
}

// Progressive events in one model generation are cumulative snapshots.
func maxReportedUsage(first, next object) object {
	result := object{}
	mergeUsageFields(result, first, maxTokens)
	mergeUsageFields(result, next, maxTokens)
	if len(first) > 0 && len(next) > 0 {
		reconcileUsageAliases(result, first, next, maxTokens)
		reconcileReportedTotal(result, first, next, maxTokens)
	}
	target := roundCounter(result, roundCacheWriteAliases)
	for _, snapshot := range []object{next, first} {
		short, long, valid := roundWriteTTL(snapshot)
		if valid && target.IsInt64() && short+long == target.Int64() {
			setUsageTTL(result, short, long, true)
			return result
		}
	}
	setUsageTTL(result, 0, 0, false)
	return result
}

type combineTokens func(a, b *big.Int) *big.Int

func sumTokens(a, b *big.Int) *big.Int { return a.Add(a, b) }
func maxTokens(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return b
	}
	return a
}

func reportedRoundTotal(usage object) *big.Int {
	if total := tokenCount(usage["total_tokens"]); total != nil {
		return total
	}
	total := new(big.Int)
	for _, paths := range [][]string{inputAliases, outputAliases} {
		if tokenCount(usage[paths[0]]) == nil && tokenCount(usage[paths[1]]) == nil {
			return nil
		}
		total.Add(total, roundCounter(usage, paths))
	}
	return total
}

// Some providers only report the gross input and output. Never expose the
// total from a single generation as the total for multiple generations.
func reconcileReportedTotal(dst, first, next object, combine combineTokens) bool {
	if _, present := dst["total_tokens"]; !present {
		return false
	}
	a, b := reportedRoundTotal(first), reportedRoundTotal(next)
	if a == nil || b == nil {
		return false
	}
	dst["total_tokens"] = json.Number(combine(a, b).String())
	return true
}

func reconcileUsageAliases(dst, first, next object, combine combineTokens) {
	for _, paths := range roundUsageAliases {
		count := json.Number(combine(roundCounter(first, paths), roundCounter(next, paths)).String())
		for _, path := range paths {
			parent, key := usageField(dst, path)
			if _, exists := parent[key]; exists {
				parent[key] = count
			}
		}
	}
}

func mergeUsageFields(dst, src object, combine combineTokens) {
	for key, value := range src {
		if child, ok := value.(object); ok {
			nested, _ := dst[key].(object)
			if nested == nil {
				nested = object{}
				dst[key] = nested
			}
			mergeUsageFields(nested, child, combine)
		} else if n := tokenCount(value); n != nil {
			if prev := tokenCount(dst[key]); prev != nil {
				n = combine(prev, n)
			}
			dst[key] = json.Number(n.String())
		} else if value == nil {
			if _, exists := dst[key]; !exists {
				dst[key] = nil
			}
		}
	}
}
