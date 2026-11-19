package basispoints

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Two upstream generations may use different compatible spellings. Every
// reported alias must describe the same combined request when it leaves BPS.
func TestRoundUsageAliasesAcrossSearchRepairAndCompaction(t *testing.T) {
	var first, second object
	require.NoError(t, decode([]byte(`{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cache_write_tokens":20,"cached_tokens":5,"cache_write_tokens_details":{"ttl_30m_tokens":15,"ttl_12h_tokens":5}},"cache_write_tokens":20}`), &first))
	require.NoError(t, decode([]byte(`{"prompt_tokens":60,"completion_tokens":4,"prompt_tokens_details":{"cache_creation_tokens":10,"cached_tokens":3,"cache_write_tokens_details":{"ttl_30m_tokens":8,"ttl_12h_tokens":2}}}`), &second))
	before := quoted(first)
	total := addRepairUsage(addRepairUsage(nil, first), second)
	require.Equal(t, "160", fmt.Sprint(total["input_tokens"]))
	require.Equal(t, "160", fmt.Sprint(total["prompt_tokens"]))
	require.Equal(t, "14", fmt.Sprint(total["output_tokens"]))
	require.Equal(t, "30", fmt.Sprint(total["cache_write_tokens"]))
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		d := total[key].(object)
		require.Equal(t, "8", fmt.Sprint(d["cached_tokens"]))
		ttl := d["cache_write_tokens_details"].(object)
		require.Equal(t, "23", fmt.Sprint(ttl["ttl_30m_tokens"]))
		require.Equal(t, "7", fmt.Sprint(ttl["ttl_12h_tokens"]))
	}
	require.Equal(t, "30", fmt.Sprint(total["input_tokens_details"].(object)["cache_write_tokens"]))
	require.Equal(t, "30", fmt.Sprint(total["prompt_tokens_details"].(object)["cache_creation_tokens"]))
	require.Equal(t, before, quoted(first))
}

func TestRoundUsageRejectsTTLThatOnlyReconcilesAcrossRounds(t *testing.T) {
	round := func(writes, short, long int) object {
		return object{"input_tokens_details": object{"cache_write_tokens": writes,
			"cache_write_tokens_details": object{"ttl_30m_tokens": short, "ttl_12h_tokens": long}}}
	}
	sum := addRepairUsage(round(10, 5, 3), round(20, 12, 10))
	sum = addRepairUsage(sum, round(12, 4, 8))
	details := sum["input_tokens_details"].(object)
	require.Equal(t, "42", fmt.Sprint(details["cache_write_tokens"]))
	require.NotContains(t, details, "cache_write_tokens_details")
}

func TestRoundUsageNestedZeroPrecedesLegacyWrite(t *testing.T) {
	first := object{"input_tokens_details": object{"cache_write_tokens": nil}, "cache_write_tokens": 100}
	next := object{"cache_creation_input_tokens": 20}
	sum := addRepairUsage(addRepairUsage(nil, first), next)
	require.Equal(t, "20", fmt.Sprint(sum["cache_creation_input_tokens"]))
	require.Equal(t, "20", fmt.Sprint(sum["input_tokens_details"].(object)["cache_write_tokens"]))
}

func TestRoundUsageSnapshotsSwitchDialectWithoutAddingProgress(t *testing.T) {
	first := object{"input_tokens": 40, "input_tokens_details": object{"cached_tokens": 10}}
	next := object{"prompt_tokens": 100, "prompt_tokens_details": object{"cached_tokens": 30}}
	round := maxReportedUsage(first, next)
	sum := addRepairUsage(round, object{"prompt_tokens": 60, "cache_read_input_tokens": 10})
	require.Equal(t, "160", fmt.Sprint(sum["input_tokens"]))
	require.Equal(t, "40", fmt.Sprint(sum["cache_read_input_tokens"]))
	require.Equal(t, 10, first["input_tokens_details"].(object)["cached_tokens"])
}

func TestRoundUsageRequiresReportedTTLPair(t *testing.T) {
	partial := func(ttl object) object {
		return object{"input_tokens_details": object{"cache_write_tokens": 10, "cache_write_tokens_details": ttl}}
	}
	first := partial(object{"ttl_30m_tokens": 6})
	next := partial(object{"ttl_12h_tokens": 4})
	sum := maxReportedUsage(first, next)
	require.NotContains(t, sum["input_tokens_details"], "cache_write_tokens_details")
	invalid := partial(object{"ttl_30m_tokens": "6", "ttl_12h_tokens": 4})
	require.NotContains(t, addRepairUsage(nil, invalid)["input_tokens_details"], "cache_write_tokens_details")
}
