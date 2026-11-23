package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fallbackGroupInventory struct {
	AccountRepository
	groups map[int64][]Account
	err    error
}

func (i *fallbackGroupInventory) ListAllWithFilters(_ context.Context, platform, kind, status, search string, groupID int64, privacy string) ([]Account, error) {
	if groupID == 0 || platform != PlatformOpenAI || kind != AccountTypeOAuth {
		return nil, errors.New("incorrect inventory scope")
	}
	return i.groups[groupID], i.err
}

type fallbackGroupMetrics struct {
	apiKeyFallbackMetrics
	observations map[int64]*OpenAIAPIKeyFallbackObservation
	groups       []int64
}

func (m *fallbackGroupMetrics) getScopedFallbackMetric(_ context.Context, group *int64, _ string, _, _ time.Time) (*OpenAIAPIKeyFallbackObservation, error) {
	m.groups = append(m.groups, *group)
	return m.observations[*group], nil
}

type fallbackCheckCapture struct {
	groupFallbackStore
	reason string
	status OpenAIAPIKeyFallbackStatus
}

func (c *fallbackCheckCapture) ApplyGroupFallbackCheck(_ context.Context, _ string, _ int64, status OpenAIAPIKeyFallbackStatus, reason string) error {
	c.reason, c.status = reason, status
	return nil
}
func TestGroupFallbackScopedTriggers(t *testing.T) {
	paused := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	active := paused
	active.Schedulable = true
	inventory := &fallbackGroupInventory{groups: map[int64][]Account{1: {paused}, 2: {active}, 3: {active}}}
	metrics := &fallbackGroupMetrics{observations: map[int64]*OpenAIAPIKeyFallbackObservation{2: {Value: 90, Samples: 1}, 3: {Value: 95, Samples: 3}}}
	s := NewAccountOpsService(nil, nil, nil)
	s.accounts = inventory
	s.fallbackSLAReader.Store(openAIAPIKeyFallbackReader{reader: metrics})
	store := &fallbackCheckCapture{}
	check := func(id int64, previous OpenAIAPIKeyFallbackStatus, want string) {
		t.Helper()
		s.checkGroupFallback(t.Context(), store, "test", GroupFallbackRule{GroupID: id, Policy: DefaultOpenAIAPIKeyFallbackConfig()}, previous)
		require.Equal(t, want, store.reason)
	}
	check(1, OpenAIAPIKeyFallbackStatus{}, "oauth_unavailable")
	require.Empty(t, metrics.groups)
	check(2, OpenAIAPIKeyFallbackStatus{}, "metric_below_threshold")
	check(2, store.status, "metric_below_threshold")
	check(3, OpenAIAPIKeyFallbackStatus{}, "")
	check(4, OpenAIAPIKeyFallbackStatus{}, "")
	require.Equal(t, []int64{2, 3, 4}, metrics.groups)
	// Replacing the members of group 1 changes the next availability reading.
	inventory.groups[1] = []Account{active}
	check(1, OpenAIAPIKeyFallbackStatus{}, "")
	require.Equal(t, 1, store.status.OAuthAvailableCount)
	inventory.err = errors.New("inventory unavailable")
	inventory.groups[1] = nil
	check(1, OpenAIAPIKeyFallbackStatus{}, "")
	require.Equal(t, "accounts_unavailable", store.status.LastErrorCategory)
}
