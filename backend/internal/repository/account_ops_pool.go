package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// All extension persistence lives here. The existing transition queue owns
// claims, leases, per-channel receipts, retry budgets and history listing.
func (r *accountOpsRepository) ObserveAccountOpsPool(ctx context.Context, observe service.AccountOpsPoolObserver) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Use the same lock order as config saves: config -> baseline -> queue.
	// Re-read switches under this lock so a stale replica cannot re-enqueue work
	// after disabling notifications. Account/credential data is read AFTER it.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('account_ops_notifications_v1',0))`); err != nil {
		return err
	}
	var rawConfig string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='account_ops_notifications_v1'`).Scan(&rawConfig); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var config service.AccountOpsConfig
	if err = json.Unmarshal([]byte(rawConfig), &config); err != nil {
		return err
	}
	if !config.Enabled || !config.PoolEnabled() {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_ops_pool_state(id,snapshot) VALUES(1,'') ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT snapshot FROM account_ops_pool_state WHERE id=1 FOR UPDATE`).Scan(&previous); err != nil {
		return err
	}
	accounts, err := readAccountOpsPool(ctx, tx)
	if err != nil {
		return err
	}
	next, event, err := observe(previous, accounts, *config.PoolNotifications)
	if err != nil {
		return err
	}
	if event != nil {
		details, err := json.Marshal(event.Details)
		if err != nil {
			return err
		}
		id := uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO account_ops_threshold_events
			(id,episode_id,phase,notification_enabled,account_id,group_id,group_name,kind,account_name,signal,first_seen,last_seen,state,details,identity,criteria)
			VALUES($1,$1,$2,true,0,'pool:all',$3,'account_pool',$3,'pool_state_change',clock_timestamp(),clock_timestamp(),'pending',$4::jsonb,'pool:all','pool:v1')`, id, event.Phase, event.AccountName, string(details))
		if err != nil {
			return err
		}
	}
	if next != previous {
		if _, err = tx.ExecContext(ctx, `UPDATE account_ops_pool_state SET snapshot=$1 WHERE id=1`, next); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func suppressAccountOpsPool(ctx context.Context, exec opsConfigExecutor, c service.AccountOpsConfig) error {
	if c.Enabled && c.PoolEnabled() {
		return nil
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM account_ops_pool_state WHERE id=1`); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='suppressed',lease='',lease_until=NULL
		WHERE kind='account_pool' AND state IN ('pending','failed','sending')`)
	return err
}

// One statement gives a coherent observation of scheduling, both generations of
// credential probes, repair outcomes and performance/AI pauses. Credentials,
// error bodies, proxy details and encrypted login secrets are never selected.
func readAccountOpsPool(ctx context.Context, tx *sql.Tx) ([]service.AccountOpsPoolAccount, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.platform,a.type,a.status,a.schedulable,
		a.expires_at,a.auto_pause_on_expired,a.rate_limit_reset_at,a.overload_until,a.temp_unschedulable_until,COALESCE(a.extra,'{}'::jsonb),
		EXISTS(SELECT 1 FROM account_performance_monitors p WHERE p.account_id=a.id AND p.paused)
		OR EXISTS(SELECT 1 FROM ai_ops_controls p WHERE p.account_id=a.id AND p.ai_paused),
		COALESCE(v2.probe_state,g.probe_state,''), COALESCE(v2.fail_streak,g.fail_streak,0),
		COALESCE(repair.failed AND NOT COALESCE((COALESCE(v2.probe_state,g.probe_state,'')='ok'
			AND COALESCE(v2.last_probe_at,g.last_probe_at)>repair.finished_at),false),false)
		FROM accounts a
		LEFT JOIN account_token_guard_v2_accounts v2 ON v2.account_id=a.id
		LEFT JOIN account_token_guard_states g ON g.account_id=a.id AND v2.account_id IS NULL
		LEFT JOIN LATERAL (
			SELECT failed,finished_at FROM (
				(SELECT t.status='failed' AS failed,t.finished_at,t.id AS tie
				FROM openai_oauth_reauth_tasks t WHERE t.account_id=a.id AND t.oauth_profile='codex'
				AND t.status IN ('failed','succeeded') AND t.finished_at IS NOT NULL
				AND NOT EXISTS(SELECT 1 FROM openai_totp_rotations rotation WHERE rotation.task_id=t.id)
				ORDER BY t.finished_at DESC,t.id DESC LIMIT 1)
				UNION ALL
				SELECT g.last_fix_result LIKE '失败:%',g.last_fix_at,0::bigint
				WHERE v2.account_id IS NULL AND g.last_fix_at IS NOT NULL
			) outcomes ORDER BY finished_at DESC,tie DESC LIMIT 1
		) repair ON TRUE
		WHERE a.deleted_at IS NULL AND a.parent_account_id IS NULL ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accounts := []service.AccountOpsPoolAccount{}
	for rows.Next() {
		var a service.AccountOpsPoolAccount
		var extra []byte
		if err = rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable,
			&a.ExpiresAt, &a.AutoPauseOnExpired, &a.RateLimitResetAt, &a.OverloadUntil, &a.TempUnschedulableUntil, &extra,
			&a.PerformancePaused, &a.ProbeState, &a.FailStreak, &a.ReloginFailed); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}
