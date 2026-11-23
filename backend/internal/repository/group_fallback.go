package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const groupFallbackKey = "group_fallback_v1"

func (r *accountOpsRepository) GroupFallbackState(ctx context.Context) (service.GroupFallbackState, error) {
	state := service.GroupFallbackState{Config: service.GroupFallbackConfig{Rules: []service.GroupFallbackRule{}}, Status: map[int64]service.OpenAIAPIKeyFallbackStatus{}}
	var raw []byte
	err := r.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", groupFallbackKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}

func (r *accountOpsRepository) GroupFallbackOptions(ctx context.Context) ([]service.GroupFallbackOption, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT g.id,g.name,eligible.id,eligible.name,eligible.schedulable
      FROM groups g LEFT JOIN (
        SELECT ag.group_id,a.id,a.name,a.schedulable FROM account_groups ag JOIN accounts a ON a.id=ag.account_id
        WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.type='apikey' AND a.status='active'
        AND a.parent_account_id IS NULL AND (a.expires_at IS NULL OR a.expires_at>now())
      ) eligible ON eligible.group_id=g.id
      WHERE g.platform='openai' AND g.status='active' AND g.deleted_at IS NULL ORDER BY g.name,g.id,eligible.name,eligible.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []service.GroupFallbackOption{}
	for rows.Next() {
		var id int64
		var name string
		var aid sql.NullInt64
		var aname sql.NullString
		var enabled sql.NullBool
		if err := rows.Scan(&id, &name, &aid, &aname, &enabled); err != nil {
			return nil, err
		}
		if len(options) == 0 || options[len(options)-1].ID != id {
			options = append(options, service.GroupFallbackOption{ID: id, Name: name, Accounts: []service.GroupFallbackAccount{}})
		}
		if aid.Valid {
			group := &options[len(options)-1]
			group.Accounts = append(group.Accounts, service.GroupFallbackAccount{ID: aid.Int64, Name: aname.String, Schedulable: enabled.Bool})
		}
	}
	return options, rows.Err()
}
func (r *accountOpsRepository) SaveGroupFallback(ctx context.Context, c service.GroupFallbackConfig, previous string) (bool, error) {
	raw, err := json.Marshal(service.GroupFallbackState{Config: c, Status: map[int64]service.OpenAIAPIKeyFallbackStatus{}})
	if err != nil {
		return false, err
	}
	var result sql.Result
	if previous == "" {
		result, err = r.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO NOTHING`, groupFallbackKey, string(raw))
	} else {
		// Preserve observed history for retained groups, including checks that
		// finished while the administrator was editing the configuration.
		result, err = r.db.ExecContext(ctx, `UPDATE settings s SET value=jsonb_set($2::jsonb,'{status}',
		  (SELECT COALESCE(jsonb_object_agg(entry.key,entry.value),'{}'::jsonb)
		   FROM jsonb_each(s.value::jsonb->'status') entry
		   WHERE entry.key IN (SELECT rule->>'group_id' FROM jsonb_array_elements($2::jsonb->'config'->'rules') rule)))::text,
		  updated_at=now() WHERE key=$1 AND value::jsonb->'config'->>'revision'=$3`, groupFallbackKey, string(raw), previous)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Fence checks against edits and other replicas. Changes, scheduler events,
// and the displayed last activation are committed together.
func (r *accountOpsRepository) ApplyGroupFallbackCheck(ctx context.Context, revision string, gid int64, status service.OpenAIAPIKeyFallbackStatus, reason string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 AND value::jsonb->'config'->>'revision'=$2 FOR UPDATE`, groupFallbackKey, revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var state service.GroupFallbackState
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if !state.Config.Enabled {
		return nil
	}
	previous := state.Status[gid]
	if status.LastCheckedAt == nil || previous.LastCheckedAt != nil && !status.LastCheckedAt.After(*previous.LastCheckedAt) {
		return nil
	}
	status.LastEnabledAt = previous.LastEnabledAt
	status.LastTriggerReason = previous.LastTriggerReason
	for _, rule := range state.Config.Rules {
		if rule.GroupID != gid || !rule.Policy.Enabled {
			continue
		}
		if reason != "" {
			status.LastTriggerReason = reason
			changed, err := enableGroupFallbackAccounts(ctx, tx, gid, rule.Policy.AccountIDs)
			if err != nil {
				return err
			}
			if changed {
				now := time.Now().UTC()
				status.LastEnabledAt = &now
			}
		}
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE settings SET value=jsonb_set(value::jsonb,ARRAY['status',$2]::text[],$3::jsonb)::text WHERE key=$1`, groupFallbackKey, strconv.FormatInt(gid, 10), string(encoded))
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}
func enableGroupFallbackAccounts(ctx context.Context, tx *sql.Tx, gid int64, ids []int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `WITH members AS MATERIALIZED (
        SELECT ag.account_id FROM account_groups ag JOIN groups g ON g.id=ag.group_id
        WHERE ag.group_id=$1 AND ag.account_id=ANY($2::bigint[]) AND g.platform='openai'
          AND g.deleted_at IS NULL AND g.status='active' ORDER BY ag.account_id FOR SHARE OF ag,g
    ) UPDATE accounts SET schedulable=true,updated_at=now() FROM members
      WHERE accounts.id=members.account_id AND accounts.platform='openai' AND accounts.type='apikey'
        AND accounts.deleted_at IS NULL AND accounts.parent_account_id IS NULL AND accounts.status='active'
        AND NOT accounts.schedulable AND (accounts.expires_at IS NULL OR accounts.expires_at>now()) RETURNING accounts.id`, gid, pq.Array(ids))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	changed := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		changed = append(changed, id)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, id := range changed {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return false, err
		}
	}
	return len(changed) > 0, nil
}
