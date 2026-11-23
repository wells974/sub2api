package service

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestQualityPatrolQuietBoundariesAndVerdicts(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	c := QualityPatrolConfig{QuietEnabled: true, QuietStart: "23:00", QuietEnd: "07:00"}
	for _, tc := range []struct {
		hour, min int
		quiet     bool
	}{{22, 59, false}, {23, 0, true}, {0, 0, true}, {6, 59, true}, {7, 0, false}} {
		at := time.Date(2026, 11, 23, tc.hour, tc.min, 0, 0, loc).UTC()
		require.Equal(t, tc.quiet, c.quiet(at, loc))
	}
	c.QuietStart, c.QuietEnd = "13:00", "14:00"
	require.True(t, c.quiet(time.Date(2026, 1, 1, 13, 0, 0, 0, loc), loc))
	correct := &ScheduledTestResult{Status: "success", QualityJudgment: &QualityJudgment{Verdict: "correct"}}
	wrong := &ScheduledTestResult{Status: "failed", ErrorMessage: "answer_mismatch", QualityJudgment: &QualityJudgment{Verdict: "incorrect"}}
	skipped := &ScheduledTestResult{Status: "skipped"}
	require.Equal(t, "passed", patrolVerdict([]*ScheduledTestResult{correct, correct}))
	require.Equal(t, "inconclusive", patrolVerdict([]*ScheduledTestResult{correct, skipped}))
	require.Equal(t, "failed", patrolVerdict([]*ScheduledTestResult{correct, skipped, wrong}))
}
