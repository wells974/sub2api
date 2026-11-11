//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func poolRepositoryFixture(t *testing.T) (*accountOpsRepository, service.AccountOpsConfig, int64) {
	t.Helper()
	ctx := context.Background()
	var previous string
	err := integrationDB.QueryRow(`SELECT value FROM settings WHERE key='account_ops_notifications_v1'`).Scan(&previous)
	require.True(t, err == nil || errors.Is(err, sql.ErrNoRows))
	c := service.AccountOpsConfig{Enabled: true, Recipient: "ops@example.test", CooldownMinutes: 60,
		PoolNotifications: &service.AccountOpsPoolConfig{Enabled: true, NotifyStates: true, NotifyCounts: true}}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO settings(key,value) VALUES('account_ops_notifications_v1',$1) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, string(raw))
	require.NoError(t, err)
	var id int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('pool-fixture','openai','oauth','active',true) RETURNING id`).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_ops_threshold_events WHERE kind='account_pool'`)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_ops_pool_state`)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_token_guard_states WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
		if previous == "" {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key='account_ops_notifications_v1'`)
		} else {
			_, _ = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key='account_ops_notifications_v1'`, previous)
		}
	})
	return NewAccountOpsRepository(integrationDB).(*accountOpsRepository), c, id
}

func TestAccountOpsPoolAtomicConcurrentRestartAndRetries(t *testing.T) {
	r, c, id := poolRepositoryFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- NewAccountOpsRepository(integrationDB).(*accountOpsRepository).ObserveAccountOpsPool(ctx, service.ObserveAccountOpsPool)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	count := func(want int) {
		t.Helper()
		var n int
		require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM account_ops_threshold_events WHERE kind='account_pool'`).Scan(&n))
		require.Equal(t, want, n)
	}
	count(1)
	require.NoError(t, r.SuppressDisabled(ctx, c), "threshold suppression must leave pool events intact")
	e, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, e)
	require.Equal(t, service.AccountOpsPoolKind, e.Kind)
	receipt := service.AccountOpsDelivery{Provider: "email", Status: "sent", Attempts: 1}
	ok, err := r.SaveDelivery(ctx, e, "email:fixture", receipt)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, r.Complete(ctx, e, "failed", 0))
	restarted := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	require.NoError(t, restarted.ObserveAccountOpsPool(ctx, service.ObserveAccountOpsPool))
	count(1)
	retry, err := restarted.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.Equal(t, e.ID, retry.ID)
	require.Equal(t, 2, retry.Attempts)
	require.Equal(t, receipt, retry.Deliveries["email:fixture"])
	require.NoError(t, restarted.Complete(ctx, retry, "sent", time.Hour))
	_, err = integrationDB.Exec(`UPDATE accounts SET status='error' WHERE id=$1`, id)
	require.NoError(t, err)
	// If enqueue fails, the new baseline must not be committed either.
	err = r.ObserveAccountOpsPool(ctx, func(old string, a []service.AccountOpsPoolAccount, cfg service.AccountOpsPoolConfig) (string, *service.AccountOpsEvent, error) {
		next, event, err := service.ObserveAccountOpsPool(old, a, cfg)
		if event != nil {
			event.Phase = "invalid-phase"
		}
		return next, event, err
	})
	require.Error(t, err)
	count(1)
	require.NoError(t, r.ObserveAccountOpsPool(ctx, service.ObserveAccountOpsPool))
	count(2)
	_, err = integrationDB.Exec(`UPDATE accounts SET status='active' WHERE id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, r.ObserveAccountOpsPool(ctx, service.ObserveAccountOpsPool))
	count(3)
	history, err := r.List(ctx, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "recovery", history[0].Phase)
	require.NotNil(t, history[0].Details.Pool)
	// Disable via the real scoped configuration path, then try a stale scan.
	svc := service.NewAccountOpsService(nil, r, nil)
	cfg, err := svc.SaveNotificationSettings(ctx, service.AccountOpsNotificationSettings{PoolNotifications: &service.AccountOpsPoolConfig{Enabled: false, NotifyStates: true, NotifyCounts: true}, Enabled: ptrPoolRepository(false)})
	require.NoError(t, err)
	require.False(t, cfg.PoolEnabled())
	require.NoError(t, r.ObserveAccountOpsPool(ctx, service.ObserveAccountOpsPool))
	count(3)
	var baselineCount, pending int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM account_ops_pool_state`).Scan(&baselineCount))
	require.Zero(t, baselineCount)
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM account_ops_threshold_events WHERE kind='account_pool' AND state IN ('pending','sending','failed')`).Scan(&pending))
	require.Zero(t, pending)
}

func ptrPoolRepository[T any](v T) *T { return &v }

func TestAccountOpsPoolReadsCredentialSourcesAndScheduling(t *testing.T) {
	r, _, id := poolRepositoryFixture(t)
	ctx := context.Background()
	read := func() service.AccountOpsPoolAccount {
		t.Helper()
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		accounts, err := readAccountOpsPool(ctx, tx)
		require.NoError(t, err)
		for _, a := range accounts {
			if a.ID == id {
				return a
			}
		}
		t.Fatal("fixture not found")
		return service.AccountOpsPoolAccount{}
	}
	_, err := integrationDB.Exec(`INSERT INTO account_token_guard_states(account_id,probe_state,fail_streak,last_probe_at,last_fix_at,last_fix_result) VALUES($1,'ok',0,NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute','失败: private-error')`, id)
	require.NoError(t, err)
	require.True(t, read().ReloginFailed)
	_, err = integrationDB.Exec(`INSERT INTO account_token_guard_v2_accounts(account_id,enabled,probe_state,fail_streak) VALUES($1,false,'auth',1)`, id)
	require.NoError(t, err)
	a := read()
	require.Equal(t, "auth", a.ProbeState)
	require.Equal(t, 1, a.FailStreak)
	require.False(t, a.ReloginFailed, "V2 owns probe and repair state even when paused")
	_, err = integrationDB.Exec(`UPDATE account_token_guard_v2_accounts SET probe_state='transient' WHERE account_id=$1`, id)
	require.NoError(t, err)
	a = read()
	require.Equal(t, 1, a.FailStreak)
	insertTask := func(profile string) int64 {
		t.Helper()
		var task int64
		require.NoError(t, integrationDB.QueryRow(`INSERT INTO openai_oauth_reauth_tasks(account_id,status,stage,oauth_profile,expected_credentials_hash,finished_at) VALUES($1,'failed','failed',$2,'test',clock_timestamp()) RETURNING id`, id, profile).Scan(&task))
		return task
	}
	insertTask("siwc")
	require.False(t, read().ReloginFailed)
	rotation := insertTask("codex")
	_, err = integrationDB.Exec(`INSERT INTO openai_totp_rotations(task_id,account_id,state,previous_ciphertext,password_ciphertext,config_updated_at) VALUES($1,$2,'failed','test','test',NOW())`, rotation, id)
	require.NoError(t, err)
	require.False(t, read().ReloginFailed, "TOTP rotation is not a re-login")
	insertTask("codex")
	require.True(t, read().ReloginFailed, "a failed re-login with no previous probe must still notify")
	_, err = integrationDB.Exec(`UPDATE account_token_guard_v2_accounts SET probe_state='ok',fail_streak=0,last_probe_at=clock_timestamp()+INTERVAL '1 second' WHERE account_id=$1`, id)
	require.NoError(t, err)
	require.False(t, read().ReloginFailed, "fresh successful probe clears obsolete re-login failure")
	_, err = integrationDB.Exec(`INSERT INTO ai_ops_controls(account_id,ai_paused) VALUES($1,true)`, id)
	require.NoError(t, err)
	require.True(t, read().PerformancePaused)
	_, err = integrationDB.Exec(`UPDATE ai_ops_controls SET ai_paused=false WHERE account_id=$1`, id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO account_performance_monitors(account_id,paused) VALUES($1,true)`, id)
	require.NoError(t, err)
	require.True(t, read().PerformancePaused)
	var shadow, deleted int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,parent_account_id,quota_dimension) VALUES('pool-shadow','openai','oauth','active',true,$1,'spark') RETURNING id`, id).Scan(&shadow))
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,deleted_at) VALUES('pool-deleted','openai','oauth','active',true,NOW()) RETURNING id`).Scan(&deleted))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id IN ($1,$2)`, shadow, deleted) })
	require.NoError(t, r.ObserveAccountOpsPool(ctx, func(old string, accounts []service.AccountOpsPoolAccount, cfg service.AccountOpsPoolConfig) (string, *service.AccountOpsEvent, error) {
		for _, a := range accounts {
			require.NotEqual(t, shadow, a.ID)
			require.NotEqual(t, deleted, a.ID)
			require.Empty(t, a.Credentials)
			require.Empty(t, a.ErrorMessage)
		}
		return service.ObserveAccountOpsPool(old, accounts, cfg)
	}))
}
