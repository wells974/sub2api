package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const qualityPatrolKind = "quality_patrol"

type QualityPatrolConfig struct {
	Enabled      bool     `json:"enabled"`
	QuietEnabled bool     `json:"quiet_enabled"`
	QuietStart   string   `json:"quiet_start"`
	QuietEnd     string   `json:"quiet_end"`
	Rules        []string `json:"rules"`
}

type QualityPatrolOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type QualityPatrolSettings struct {
	Config             QualityPatrolConfig   `json:"config"`
	Timezone           string                `json:"timezone"`
	Rules              []QualityPatrolOption `json:"rules"`
	ChannelsConfigured bool                  `json:"channels_configured"`
}
type QualityPatrolCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Unknown int `json:"unknown"`
}
type qualityPatrolRepository interface {
	PatrolConfig(context.Context) (QualityPatrolConfig, error)
	PatrolRules(context.Context) ([]QualityPatrolOption, error)
	SavePatrol(context.Context, QualityPatrolConfig) error
	EnqueuePatrolRounds(context.Context) error
}

func (s *AccountOpsService) QualityPatrolSettings(ctx context.Context) (QualityPatrolSettings, error) {
	repo, ok := s.repo.(qualityPatrolRepository)
	v := QualityPatrolSettings{Timezone: s.timezone.String()}
	if !ok {
		return v, errors.New("patrol notifications unavailable")
	}
	var err error
	if v.Config, err = repo.PatrolConfig(ctx); err != nil {
		return v, err
	}
	if v.Rules, err = repo.PatrolRules(ctx); err != nil {
		return v, err
	}
	c, err := s.GetConfig(ctx)
	if err != nil {
		return v, err
	}
	v.ChannelsConfigured = c.hasDestination()
	return v, nil
}

func (s *AccountOpsService) SaveQualityPatrol(ctx context.Context, c QualityPatrolConfig) error {
	v, err := s.QualityPatrolSettings(ctx)
	if err != nil {
		return err
	}
	if c.QuietEnabled {
		for _, hm := range []string{c.QuietStart, c.QuietEnd} {
			t, err := time.Parse("15:04", hm)
			if err != nil || t.Format("15:04") != hm {
				return accountOpsConfigValidation("invalid quiet hours")
			}
		}
		if c.QuietStart == c.QuietEnd {
			return accountOpsConfigValidation("quiet hours must have distinct endpoints")
		}
	}
	if c.Enabled && (len(c.Rules) == 0 || !v.ChannelsConfigured) {
		return accountOpsConfigValidation("configure a notification channel and select at least one rule")
	}
	valid := map[string]bool{}
	for _, o := range v.Rules {
		valid[o.ID] = true
	}
	for _, id := range c.Rules {
		if !valid[id] {
			return accountOpsConfigValidation("invalid or duplicate quality rule")
		}
		delete(valid, id)
	}
	if c.Rules == nil {
		c.Rules = []string{}
	}
	return s.repo.(qualityPatrolRepository).SavePatrol(ctx, c)
}

func (c QualityPatrolConfig) quiet(at time.Time, loc *time.Location) bool {
	if !c.QuietEnabled {
		return false
	}
	hm := at.In(loc).Format("15:04")
	if c.QuietStart > c.QuietEnd {
		return hm >= c.QuietStart || hm < c.QuietEnd
	}
	return hm >= c.QuietStart && hm < c.QuietEnd
}

func (s *AccountOpsService) patrolMessage(e *AccountOpsEvent) accountOpsMessage {
	c := e.Details.Patrol
	name := strings.Join(strings.Fields(e.GroupName), " ")
	title := "巡检 · " + name
	stamp := e.FirstSeen.In(s.timezone).Format("2006-01-02 15:04 MST")
	body := fmt.Sprintf("%s\n本轮检查 %d 个账号\n通过 %d 个 · 未通过 %d 个 · 无法判断 %d 个", stamp, c.Passed+c.Failed+c.Unknown, c.Passed, c.Failed, c.Unknown)
	return accountOpsMessage{title: title, robotTitle: title, plain: body, account: name, observedTime: stamp,
		markdown: "### " + escapeOpsMarkdown(title) + "\n" + body, html: "<p>" + strings.ReplaceAll(html.EscapeString(body), "\n", "<br>") + "</p>"}
}

// All destinations and per-recipient receipts are handled by the existing sender.
// Its three config reads also recheck patrol subscriptions and quiet hours,
// including after a rate reservation and on retries.
func (s *AccountOpsService) deliveryConfig(ctx context.Context, e *AccountOpsEvent) (AccountOpsConfig, error) {
	c, err := s.loadConfig(ctx)
	if err != nil || e.Kind != qualityPatrolKind {
		return c, err
	}
	repo, ok := s.repo.(qualityPatrolRepository)
	if !ok {
		return c, errors.New("patrol notifications unavailable")
	}
	p, err := repo.PatrolConfig(ctx)
	c.Enabled = p.Enabled && e.Details != nil && e.Details.Patrol != nil && slices.Contains(p.Rules, e.GroupID) &&
		!p.quiet(time.Now(), s.timezone) && !p.quiet(e.FirstSeen, s.timezone) && !p.quiet(e.LastSeen, s.timezone)
	return c, err
}

type qualityPatrolRecorder interface {
	PrepareQualityPatrol(context.Context, []*ScheduledTestPlan) error
	SetPatrolOutcome(context.Context, int64, time.Time, time.Time, string) error
}

// Notification bookkeeping is best effort. A blocked notification table must
// leave the scheduler time to run and persist the actual account checks.
const patrolBookkeepingTimeout = 2 * time.Second

func (s *ScheduledTestRunnerService) prepareQualityPatrol(ctx context.Context, plans []*ScheduledTestPlan) {
	repo, ok := s.planRepo.(qualityPatrolRecorder)
	if !ok {
		return
	}
	query, cancel := context.WithTimeout(ctx, patrolBookkeepingTimeout)
	defer cancel()
	if err := repo.PrepareQualityPatrol(query, plans); err != nil {
		logger.LegacyPrintf("service.quality_patrol", "could not prepare quality patrol notifications")
	}
}

func (s *AccountOpsService) enqueueQualityPatrol(ctx context.Context) {
	repo, ok := s.repo.(qualityPatrolRepository)
	if !ok {
		return
	}
	query, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := repo.EnqueuePatrolRounds(query); err != nil {
		s.failures.Add(1)
	}
}

func (s *ScheduledTestRunnerService) patrolOutcome(ctx context.Context, p *ScheduledTestPlan, until time.Time, outcome string) {
	repo, ok := s.planRepo.(qualityPatrolRecorder)
	if !ok || !p.PatrolTracked || p.NextRunAt == nil {
		return
	}
	query, cancel := context.WithTimeout(ctx, patrolBookkeepingTimeout)
	defer cancel()
	if err := repo.SetPatrolOutcome(query, p.ID, *p.NextRunAt, until, outcome); err != nil {
		logger.LegacyPrintf("service.quality_patrol", "could not record quality patrol outcome for plan %d", p.ID)
	}
}

func patrolVerdict(results []*ScheduledTestResult) string {
	verdict := qualityRoundOutcome(results)
	if verdict == "answer_29" {
		return "failed"
	}
	if verdict == "passed" {
		for _, r := range results {
			if r == nil || r.Status == "skipped" {
				return "inconclusive"
			}
		}
	}
	return verdict
}
