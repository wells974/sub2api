package service

import (
	"context"
	"math"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

type GroupFallbackRule struct {
	GroupID int64                      `json:"group_id"`
	Policy  OpenAIAPIKeyFallbackConfig `json:"policy"`
}

type GroupFallbackConfig struct {
	Enabled  bool                `json:"enabled"`
	Revision string              `json:"revision"`
	Rules    []GroupFallbackRule `json:"rules"`
}

type GroupFallbackState struct {
	Config GroupFallbackConfig                  `json:"config"`
	Status map[int64]OpenAIAPIKeyFallbackStatus `json:"status"`
}

type GroupFallbackAccount struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Schedulable bool   `json:"schedulable"`
}
type GroupFallbackOption struct {
	ID       int64                  `json:"id"`
	Name     string                 `json:"name"`
	Accounts []GroupFallbackAccount `json:"accounts"`
}

type groupFallbackStore interface {
	GroupFallbackState(context.Context) (GroupFallbackState, error)
	GroupFallbackOptions(context.Context) ([]GroupFallbackOption, error)
	SaveGroupFallback(context.Context, GroupFallbackConfig, string) (bool, error)
	ApplyGroupFallbackCheck(context.Context, string, int64, OpenAIAPIKeyFallbackStatus, string) error
}
type groupFallbackMetricReader interface {
	getScopedFallbackMetric(context.Context, *int64, string, time.Time, time.Time) (*OpenAIAPIKeyFallbackObservation, error)
}

func (s *AccountOpsService) groupFallbackStore() (groupFallbackStore, error) {
	store, ok := s.repo.(groupFallbackStore)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("GROUP_FALLBACK_UNAVAILABLE", "Group fallback unavailable")
	}
	return store, nil
}
func (s *AccountOpsService) GroupFallbackState(ctx context.Context) (GroupFallbackState, error) {
	store, err := s.groupFallbackStore()
	if err != nil {
		return GroupFallbackState{}, err
	}
	return store.GroupFallbackState(ctx)
}
func (s *AccountOpsService) GroupFallbackOptions(ctx context.Context) ([]GroupFallbackOption, error) {
	store, err := s.groupFallbackStore()
	if err != nil {
		return nil, err
	}
	return store.GroupFallbackOptions(ctx)
}
func (s *AccountOpsService) SaveGroupFallback(ctx context.Context, c GroupFallbackConfig) (GroupFallbackConfig, error) {
	store, err := s.groupFallbackStore()
	if err != nil {
		return c, err
	}
	if c.Rules == nil {
		c.Rules = []GroupFallbackRule{}
	}
	seen := make(map[int64]bool)
	var options []GroupFallbackOption
	for i := range c.Rules {
		rule := &c.Rules[i]
		if rule.Policy.AccountIDs == nil {
			rule.Policy.AccountIDs = []int64{}
		}
		if rule.GroupID <= 0 || seen[rule.GroupID] {
			return c, infraerrors.BadRequest("GROUP_FALLBACK_GROUP_INVALID", "Select a unique group for each rule")
		}
		seen[rule.GroupID] = true
		if err := rule.Policy.Validate(); err != nil {
			return c, err
		}
		if !c.Enabled || !rule.Policy.Enabled {
			continue
		}
		if options == nil {
			options, err = store.GroupFallbackOptions(ctx)
			if err != nil {
				return c, err
			}
		}
		index := slices.IndexFunc(options, func(g GroupFallbackOption) bool { return g.ID == rule.GroupID })
		if index < 0 {
			return c, infraerrors.BadRequest("GROUP_FALLBACK_GROUP_INVALID", "Select an active OpenAI group")
		}
		for _, id := range rule.Policy.AccountIDs {
			if !slices.ContainsFunc(options[index].Accounts, func(a GroupFallbackAccount) bool { return a.ID == id }) {
				return c, infraerrors.BadRequest("GROUP_FALLBACK_ACCOUNT_INVALID", "Select eligible API Key accounts in this group")
			}
		}
	}
	previous := c.Revision
	c.Revision = uuid.NewString()
	saved, err := store.SaveGroupFallback(ctx, c, previous)
	if err == nil && !saved {
		err = infraerrors.Conflict("GROUP_FALLBACK_CONFLICT", "Configuration changed. Reload before saving.")
	}
	return c, err
}

// The full-node worker is independent of the global fallback and notification loop.
func (s *AccountOpsService) runGroupFallback(ctx context.Context, store groupFallbackStore) {
	defer s.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		state, err := store.GroupFallbackState(query)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.LegacyPrintf("group_fallback", "could not load group fallback settings")
		}
		if err == nil && state.Config.Enabled {
			for _, rule := range state.Config.Rules {
				if ctx.Err() != nil {
					return
				}
				if rule.Policy.Enabled {
					s.checkGroupFallback(ctx, store, state.Config.Revision, rule, state.Status[rule.GroupID])
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *AccountOpsService) checkGroupFallback(ctx context.Context, store groupFallbackStore, revision string, rule GroupFallbackRule, previous OpenAIAPIKeyFallbackStatus) {
	now := time.Now().UTC()
	policy := rule.Policy
	state := previous
	if previous.Metric != policy.Metric || previous.WindowMinutes != policy.WindowMinutes {
		state.LastMetricCheckedAt, state.MetricValue, state.Samples = nil, nil, 0
	}
	state.LastCheckedAt = &now
	state.Metric, state.WindowMinutes = policy.Metric, policy.WindowMinutes
	state.OAuthAccountCount, state.OAuthAvailableCount = 0, 0
	state.LastErrorCategory = ""
	dep, _ := s.fallbackSLAReader.Load().(openAIAPIKeyFallbackReader)
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	accounts, err := s.accounts.ListAllWithFilters(read, PlatformOpenAI, AccountTypeOAuth, "", "", rule.GroupID, "")
	for i := range accounts {
		account := &accounts[i]
		if account.IsShadow() || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
			continue
		}
		state.OAuthAccountCount++
		if openAIAPIKeyFallbackOAuthAvailable(read, account, dep.settings, now) {
			state.OAuthAvailableCount++
		}
	}
	if err != nil || read.Err() != nil {
		state.LastErrorCategory = "accounts_unavailable"
	}
	cancel()
	reason := ""
	if state.LastErrorCategory == "" && state.OAuthAccountCount > 0 && state.OAuthAvailableCount == 0 {
		reason = "oauth_unavailable"
	}
	if reason == "" {
		if state.LastMetricCheckedAt == nil || now.Sub(*state.LastMetricCheckedAt) >= time.Minute {
			state.LastMetricCheckedAt, state.MetricValue, state.Samples = &now, nil, 0
			reader, ok := dep.reader.(groupFallbackMetricReader)
			if !ok {
				state.LastErrorCategory = "metric_unavailable"
			} else {
				read, cancel := context.WithTimeout(ctx, 5*time.Second)
				obs, err := reader.getScopedFallbackMetric(read, &rule.GroupID, policy.Metric, now.Add(-time.Duration(policy.WindowMinutes)*time.Minute), now)
				cancel()
				if err != nil {
					state.LastErrorCategory = "metric_unavailable"
				} else if obs != nil && obs.Samples > 0 && !math.IsNaN(obs.Value) && obs.Value >= 0 && obs.Value <= 100 {
					state.MetricValue, state.Samples = &obs.Value, obs.Samples
				}
			}
		}
		if state.Samples > 0 && state.MetricValue != nil && *state.MetricValue < policy.Threshold {
			reason = "metric_below_threshold"
		}
	}
	write, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.ApplyGroupFallbackCheck(write, revision, rule.GroupID, state, reason); err != nil && ctx.Err() == nil {
		logger.LegacyPrintf("group_fallback", "could not apply group fallback check for group %d", rule.GroupID)
	}
}
