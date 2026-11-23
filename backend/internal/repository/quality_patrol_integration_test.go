//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type patrolTestEmail struct{ titles, bodies []string }

func (m *patrolTestEmail) SendEmail(_ context.Context, _ string, title, body string) error {
	m.titles = append(m.titles, title)
	m.bodies = append(m.bodies, body)
	return nil
}

func TestQualityPatrolGroupedDelivery(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := poolRepositoryFixture(t)
	mail := &patrolTestEmail{}
	svc := service.NewAccountOpsService(nil, repo, mail)
	// Patrol has its own master switch and reuses the existing recipient.
	off := false
	_, err := svc.SaveNotificationSettings(ctx, service.AccountOpsNotificationSettings{Enabled: &off})
	require.NoError(t, err)
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	var group int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO quality_rule_templates (account_filter,cron_expression,pelican_config)
      VALUES('{}','* * * * *','{"rule_name":"主力号池","quality":{}}') RETURNING id`).Scan(&group))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM quality_rule_templates WHERE id=$1`, group) })
	rule := fmt.Sprintf("group:%d", group)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM account_ops_threshold_events WHERE kind='quality_patrol' AND group_id=$1`, rule)
		_, _ = integrationDB.Exec(`DELETE FROM quality_patrol_rounds WHERE rule_key=$1`, rule)
		_, _ = integrationDB.Exec(`DELETE FROM quality_patrol_settings`)
	})
	plans := NewScheduledTestPlanRepository(integrationDB).(*scheduledTestPlanRepository)
	var members []*service.ScheduledTestPlan
	for i := 0; i < 4; i++ {
		var account int64
		require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable)
        VALUES('patrol-fixture','openai','oauth','active',true) RETURNING id`).Scan(&account))
		t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, account) })
		p, err := plans.Create(ctx, &service.ScheduledTestPlan{AccountID: account, ModelID: "fixture", CronExpression: "* * * * *", MaxResults: 5,
			Enabled: true, NextRunAt: &due, PelicanConfig: &service.PelicanTestConfig{RuleName: "同名规则", Quality: &service.QualityPolicy{Action: "observe_only"}}})
		require.NoError(t, err)
		if i < 3 {
			_, err = integrationDB.Exec(`INSERT INTO quality_rule_template_accounts(template_id,account_id,plan_id) VALUES($1,$2,$3)`, group, account, p.ID)
			require.NoError(t, err)
		}
		members = append(members, p)
	}
	c := service.QualityPatrolConfig{Enabled: true, Rules: []string{rule}}
	require.NoError(t, svc.SaveQualityPatrol(ctx, c))
	// Independent scheduler replicas may select the same due plan concurrently.
	const replicas = 4
	errors := make(chan error, replicas)
	start := make(chan struct{})
	for range replicas {
		snapshot := *members[0]
		go func() {
			<-start
			errors <- plans.PrepareQualityPatrol(ctx, []*service.ScheduledTestPlan{&snapshot})
		}()
	}
	close(start)
	for range replicas {
		require.NoError(t, <-errors)
	}
	require.NoError(t, plans.PrepareQualityPatrol(ctx, members))
	for i, p := range members {
		require.Equal(t, i < 3, p.PatrolTracked)
	}
	until := time.Now().Add(15 * time.Minute)
	for i, outcome := range []string{"passed", "failed", "running", "passed"} {
		require.NoError(t, plans.SetPatrolOutcome(ctx, members[i].ID, due, until, outcome))
	}
	require.NoError(t, repo.EnqueuePatrolRounds(ctx))
	var n int
	count := func(want int) {
		t.Helper()
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM account_ops_threshold_events WHERE kind='quality_patrol' AND group_id=$1`, rule).Scan(&n))
		require.Equal(t, want, n)
	}
	count(0)
	require.NoError(t, plans.SetPatrolOutcome(ctx, members[2].ID, due, until, "inconclusive"))
	for range replicas {
		go func() { errors <- repo.EnqueuePatrolRounds(ctx) }()
	}
	for range replicas {
		require.NoError(t, <-errors)
	}
	count(1)
	reset := func() {
		_, err := integrationDB.Exec(`UPDATE account_ops_threshold_events SET state='pending',next_send_at=now(),attempts=0,deliveries='{}' WHERE group_id=$1 AND kind='quality_patrol'`, rule)
		require.NoError(t, err)
	}
	send := func() {
		e, err := repo.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, e)
		require.Equal(t, rule, e.GroupID)
		svc.DeliverNotificationForTesting(ctx, e)
	}
	send()
	require.Equal(t, []string{"巡检 · 主力号池"}, mail.titles)
	require.Contains(t, mail.bodies[0], "本轮检查 3 个账号")
	require.Contains(t, mail.bodies[0], "通过 1 个 · 未通过 1 个 · 无法判断 1 个")
	reset()
	// A retry scheduled for daytime must still drop a round from quiet hours.
	c.QuietEnabled = true
	c.QuietStart = "23:00"
	c.QuietEnd = "07:00"
	require.NoError(t, svc.SaveQualityPatrol(ctx, c))
	_, err = integrationDB.Exec(`UPDATE account_ops_threshold_events SET first_seen='2026-01-01T16:00:00Z' WHERE group_id=$1`, rule)
	require.NoError(t, err)
	send()
	require.Len(t, mail.titles, 1)
	var state string
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_ops_threshold_events WHERE group_id=$1 AND kind='quality_patrol'`, rule).Scan(&state))
	require.Equal(t, "suppressed", state)

	// Removing the subscription invalidates even an already claimed retry.
	reset()
	claimed, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	c.QuietEnabled = false
	c.Rules = []string{fmt.Sprintf("plan:%d", members[3].ID)}
	require.NoError(t, svc.SaveQualityPatrol(ctx, c))
	svc.DeliverNotificationForTesting(ctx, claimed)
	require.Len(t, mail.titles, 1)
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_ops_threshold_events WHERE group_id=$1 AND kind='quality_patrol'`, rule).Scan(&state))
	require.Equal(t, "suppressed", state)
}
