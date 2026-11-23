package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *accountOpsRepository) PatrolConfig(ctx context.Context) (service.QualityPatrolConfig, error) {
	c := service.QualityPatrolConfig{Rules: []string{}, QuietStart: "23:00", QuietEnd: "07:00"}
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT config FROM quality_patrol_settings WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}

func (r *accountOpsRepository) PatrolRules(ctx context.Context) ([]service.QualityPatrolOption, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT 'group:'||id, COALESCE(NULLIF(pelican_config->>'rule_name',''),'分组规则 #'||id)
        FROM quality_rule_templates UNION ALL
        SELECT 'plan:'||p.id, COALESCE(NULLIF(p.pelican_config->>'rule_name',''),'账号规则 #'||p.id)
        FROM scheduled_test_plans p JOIN accounts a ON a.id=p.account_id AND a.deleted_at IS NULL
        WHERE p.pelican_config->'quality' IS NOT NULL AND NOT EXISTS
        (SELECT 1 FROM quality_rule_template_accounts l WHERE l.plan_id=p.id)
        ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.QualityPatrolOption{}
	for rows.Next() {
		var v service.QualityPatrolOption
		if err := rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *accountOpsRepository) SavePatrol(ctx context.Context, c service.QualityPatrolConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO quality_patrol_settings(id,config) VALUES(1,$1::jsonb)
        ON CONFLICT(id) DO UPDATE SET config=excluded.config`, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE quality_patrol_rounds SET done=true WHERE NOT done AND
        (NOT $1 OR NOT(rule_key=ANY($2)))`, c.Enabled, pq.Array(c.Rules)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='suppressed',lease='',lease_until=NULL
      WHERE kind='quality_patrol' AND state IN ('pending','sending','failed') AND (NOT $1 OR NOT(group_id=ANY($2)))`, c.Enabled, pq.Array(c.Rules)); err != nil {
		return err
	}
	return tx.Commit()
}

// Snapshot all due plans before any worker advances next_run_at. Only the first
// scheduler to insert a rule/minute creates its members, including across replicas.
func (r *scheduledTestPlanRepository) PrepareQualityPatrol(ctx context.Context, plans []*service.ScheduledTestPlan) error {
	ids := []int64{}
	due := []time.Time{}
	for _, p := range plans {
		if p.PelicanConfig != nil && p.PelicanConfig.Quality != nil && !p.ManualQualityRun && p.NextRunAt != nil {
			ids = append(ids, p.ID)
			due = append(due, *p.NextRunAt)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx, `WITH cfg AS MATERIALIZED (
       SELECT config FROM quality_patrol_settings WHERE id=1 AND config->>'enabled'='true' FOR SHARE
    ), candidates AS MATERIALIZED (
       SELECT t.id template_id, COALESCE('group:'||t.id,'plan:'||p.id) AS rule_key,
         date_trunc('minute',p.next_run_at) AS scheduled_at,p.id AS plan_id,p.account_id,
         CASE WHEN t.id IS NOT NULL THEN COALESCE(NULLIF(t.pelican_config->>'rule_name',''),'分组规则 #'||t.id)
              ELSE COALESCE(NULLIF(p.pelican_config->>'rule_name',''),'账号规则 #'||p.id) END AS rule_name
       FROM cfg CROSS JOIN unnest($1::bigint[],$2::timestamptz[]) due(id,at)
       JOIN scheduled_test_plans p ON p.id=due.id AND p.enabled AND p.next_run_at=due.at
       JOIN accounts a ON a.id=p.account_id AND a.deleted_at IS NULL
       LEFT JOIN quality_rule_template_accounts l ON l.plan_id=p.id
       LEFT JOIN quality_rule_templates t ON t.id=l.template_id
       WHERE cfg.config->'rules' ? COALESCE('group:'||t.id,'plan:'||p.id)
    ), inserted AS (
       INSERT INTO quality_patrol_rounds(rule_key,scheduled_at,rule_name)
       SELECT DISTINCT rule_key,scheduled_at,rule_name FROM candidates ORDER BY 1,2
       ON CONFLICT DO NOTHING RETURNING rule_key,scheduled_at
    ), members AS (
       SELECT c.rule_key,c.scheduled_at,c.plan_id,c.account_id FROM candidates c JOIN inserted USING(rule_key,scheduled_at)
       UNION
       -- Interval-based plans in one template can fall at different seconds of
       -- the same minute. Include later peers before the first worker finishes.
       SELECT i.rule_key,i.scheduled_at,p.id,p.account_id FROM inserted i
       JOIN (SELECT DISTINCT rule_key,scheduled_at,template_id FROM candidates WHERE template_id IS NOT NULL) g USING(rule_key,scheduled_at)
       JOIN quality_rule_template_accounts l ON l.template_id=g.template_id
       JOIN scheduled_test_plans p ON p.id=l.plan_id AND p.enabled
         AND p.next_run_at>=i.scheduled_at AND p.next_run_at<i.scheduled_at+interval '1 minute'
         AND COALESCE(p.pelican_config->>'_manual_quality_run','false')<>'true'
       JOIN accounts a ON a.id=p.account_id AND a.deleted_at IS NULL
    ), seeded AS (
       INSERT INTO quality_patrol_members(rule_key,scheduled_at,plan_id,account_id) SELECT * FROM members
    ) SELECT DISTINCT plan_id FROM candidates`,
		pq.Array(ids), pq.Array(due))
	if err != nil {
		return err
	}
	defer rows.Close()
	selected := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		selected[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range plans {
		p.PatrolTracked = selected[p.ID]
	}
	return nil
}

func (r *scheduledTestPlanRepository) SetPatrolOutcome(ctx context.Context, planID int64, due, until time.Time, outcome string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE quality_patrol_members m SET outcome=$4,updated_at=now(),deadline=$3
        FROM quality_patrol_rounds r WHERE m.plan_id=$1 AND m.scheduled_at=date_trunc('minute',$2::timestamptz)
        AND r.rule_key=m.rule_key AND r.scheduled_at=m.scheduled_at AND NOT r.done
        AND m.outcome IN ('waiting','running')`, planID, due, until, outcome)
	return err
}

// Mark complete and enqueue immutable counts in one statement. Delivery uses the
// same lease, destination rechecks, receipts and retry budget as pool alerts.
func (r *accountOpsRepository) EnqueuePatrolRounds(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `WITH candidates AS (
      SELECT r.rule_key,r.scheduled_at FROM quality_patrol_rounds r
      WHERE NOT r.done AND NOT EXISTS (
        SELECT 1 FROM quality_patrol_members m WHERE (m.rule_key,m.scheduled_at)=(r.rule_key,r.scheduled_at)
        AND m.outcome IN ('running','waiting') AND m.deadline>now())
      ORDER BY r.created_at LIMIT 100 FOR UPDATE SKIP LOCKED
    ), closed AS (
      UPDATE quality_patrol_rounds r SET done=true FROM candidates c
      WHERE (r.rule_key,r.scheduled_at)=(c.rule_key,c.scheduled_at) RETURNING r.*
    ), accounts AS (
      SELECT c.rule_key,c.rule_name,c.scheduled_at,m.account_id,
        min(CASE m.outcome WHEN 'passed' THEN 2 WHEN 'failed' THEN 0 ELSE 1 END) score,
        max(CASE WHEN m.outcome='running' THEN m.deadline ELSE m.updated_at END) finished
      FROM closed c JOIN quality_patrol_members m USING(rule_key,scheduled_at)
      WHERE m.outcome NOT IN ('skipped','waiting') GROUP BY c.rule_key,c.rule_name,c.scheduled_at,m.account_id
    ), summary AS (
      SELECT rule_key,rule_name,scheduled_at,max(finished) finished,
       'patrol:'||rule_key||':'||extract(epoch FROM scheduled_at)::bigint event_id,
       jsonb_build_object('patrol',jsonb_build_object('passed',count(*) FILTER(WHERE score=2),
         'failed',count(*) FILTER(WHERE score=0),'unknown',count(*) FILTER(WHERE score=1))) details
      FROM accounts GROUP BY rule_key,rule_name,scheduled_at
    ), cleanup AS (
      DELETE FROM quality_patrol_rounds WHERE (rule_key,scheduled_at) IN
      (SELECT rule_key,scheduled_at FROM quality_patrol_rounds WHERE done AND created_at<now()-interval '7 days' LIMIT 100)
    ) INSERT INTO account_ops_threshold_events
      (id,episode_id,phase,notification_enabled,account_id,group_id,group_name,kind,account_name,signal,first_seen,last_seen,state,details,identity,criteria)
      SELECT event_id,event_id,'alert',true,0,rule_key,rule_name,'quality_patrol',rule_name,'quality_patrol',
        scheduled_at,finished,'pending',details,event_id,'patrol:v1' FROM summary ON CONFLICT DO NOTHING`)
	return err
}
