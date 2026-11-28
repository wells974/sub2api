//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupFallbackFencesAndIsolation(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	t.Cleanup(func() { _, _ = integrationDB.Exec("DELETE FROM settings WHERE key=$1", groupFallbackKey) })
	makeGroup := func(name string) int64 {
		var id int64
		require.NoError(t, integrationDB.QueryRow("INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id", name).Scan(&id))
		t.Cleanup(func() { _, _ = integrationDB.Exec("DELETE FROM groups WHERE id=$1", id) })
		return id
	}
	makeKey := func(gid int64) int64 {
		var id int64
		require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable)
        VALUES('group-fallback-fixture','openai','apikey','active',false) RETURNING id`).Scan(&id))
		_, err := integrationDB.Exec("INSERT INTO account_groups(group_id,account_id) VALUES($1,$2)", gid, id)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
		})
		return id
	}
	a, b := makeGroup("group-fallback-a"), makeGroup("group-fallback-b")
	keyA, keyB, moved := makeKey(a), makeKey(b), makeKey(a)
	rule := func(gid int64, ids ...int64) service.GroupFallbackRule {
		p := service.DefaultOpenAIAPIKeyFallbackConfig()
		p.Enabled = true
		p.AccountIDs = ids
		return service.GroupFallbackRule{GroupID: gid, Policy: p}
	}
	svc := service.NewAccountOpsService(nil, repo, nil)
	_, err := svc.SaveGroupFallback(ctx, service.GroupFallbackConfig{Enabled: true, Rules: []service.GroupFallbackRule{rule(a, keyB)}})
	require.Error(t, err, "only same-group targets may be selected")
	config, err := svc.SaveGroupFallback(ctx, service.GroupFallbackConfig{Enabled: true, Rules: []service.GroupFallbackRule{rule(a, keyA, moved), rule(b, keyB)}})
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE account_groups SET group_id=$1 WHERE group_id=$2 AND account_id=$3", b, a, moved)
	require.NoError(t, err)
	now := time.Now().UTC()
	check := service.OpenAIAPIKeyFallbackStatus{LastCheckedAt: &now}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- repo.ApplyGroupFallbackCheck(ctx, config.Revision, a, check, "oauth_unavailable") }()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
	var opened, events int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM accounts WHERE id IN ($1,$2,$3) AND schedulable", keyA, keyB, moved).Scan(&opened))
	require.Equal(t, 1, opened)
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2", keyA, service.SchedulerOutboxEventAccountChanged).Scan(&events))
	require.Equal(t, 1, events)
	state, err := repo.GroupFallbackState(ctx)
	require.NoError(t, err)
	require.Equal(t, "oauth_unavailable", state.Status[a].LastTriggerReason)
	require.NotNil(t, state.Status[a].LastEnabledAt)
	config.Enabled = false
	_, err = svc.SaveGroupFallback(ctx, config)
	require.NoError(t, err, "stale account assignments must allow disabling")
	state, err = repo.GroupFallbackState(ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Status[a].LastEnabledAt, "editing settings preserves group activation history")
	_, err = svc.SaveGroupFallback(ctx, config)
	require.Error(t, err, "stale editor cannot overwrite newer settings")
	require.NoError(t, repo.ApplyGroupFallbackCheck(ctx, config.Revision, b, check, "metric_below_threshold"))
	var on bool
	require.NoError(t, integrationDB.QueryRow("SELECT schedulable FROM accounts WHERE id=$1", keyB).Scan(&on))
	require.False(t, on)
}

// Historical requests keep their recorded group even when both groups use
// the same provider account. Cover the raw sources for both fallback metrics.
func TestGroupFallbackMetricsRespectRequestGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "group-fallback-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	first := mustCreateGroup(t, client, &service.Group{Name: "fallback-metric-first", Platform: service.PlatformOpenAI})
	second := mustCreateGroup(t, client, &service.Group{Name: "fallback-metric-second", Platform: service.PlatformOpenAI})
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM ops_error_logs WHERE api_key_id=$1", key.ID)
		_, _ = integrationDB.Exec("DELETE FROM usage_logs WHERE user_id=$1", user.ID)
		_, _ = integrationDB.Exec("DELETE FROM api_keys WHERE id=$1", key.ID)
		_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", account.ID)
		_, _ = integrationDB.Exec("DELETE FROM groups WHERE id IN ($1,$2)", first.ID, second.ID)
		_, _ = integrationDB.Exec("DELETE FROM users WHERE id=$1", user.ID)
	})
	at := time.Date(2022, 5, 17, 10, 0, 0, 0, time.UTC)
	_, err := integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,created_at)
       VALUES($1,$2,$3,$4,'group-fallback-success','fixture',$5)`, user.ID, key.ID, account.ID, first.ID, at)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO ops_error_logs(api_key_id,group_id,request_id,account_id,model,error_phase,error_type,error_owner,severity,status_code,platform,created_at)
       VALUES($1,$2,'group-fallback-failure',$3,'fixture','upstream','upstream_error','provider','error',503,'openai',$4)`, key.ID, second.ID, account.ID, at)
	require.NoError(t, err)
	// A billed partial response to an overlong prompt is excluded from
	// this group's SLA even though the usage row still exists.
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,created_at,input_tokens,sla_excluded)
       VALUES($1,$2,$3,$4,'group-fallback-overlong','fixture',$5,17,true)`, user.ID, key.ID, account.ID, second.ID, at)
	require.NoError(t, err)

	ops := NewOpsRepository(integrationDB)
	for _, tc := range []struct {
		group int64
		sla   float64
	}{{first.ID, 1}, {second.ID, 0}} {
		filter := &service.OpsDashboardFilter{StartTime: at.Add(-time.Minute), EndTime: at.Add(time.Minute), Platform: service.PlatformOpenAI, GroupID: &tc.group, OpenAIOAuthOnly: true}
		stats, err := ops.GetWindowStats(ctx, filter)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.RequestCountSLA)
		require.Equal(t, tc.sla, stats.SLA)
		overview, err := ops.GetDashboardOverview(ctx, filter)
		require.NoError(t, err)
		require.EqualValues(t, 1, overview.RequestCountSLA)
		require.Equal(t, tc.sla, overview.SLA)
	}
}
