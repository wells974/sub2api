-- Independent pool baseline; reuse the durable AccountOps delivery queue.
CREATE TABLE IF NOT EXISTS account_ops_pool_state (
    id SMALLINT PRIMARY KEY CHECK (id=1),
    snapshot TEXT NOT NULL DEFAULT ''
);

ALTER TABLE account_ops_threshold_events DROP CONSTRAINT IF EXISTS account_ops_threshold_events_kind_check;
ALTER TABLE account_ops_threshold_events ADD CONSTRAINT account_ops_threshold_events_kind_check
    CHECK (kind IN ('balance_threshold','quota_threshold','account_pool'));

-- Bound each account's task lookup to its newest completed re-login.
CREATE INDEX IF NOT EXISTS account_ops_pool_reauth_latest
    ON openai_oauth_reauth_tasks(account_id,finished_at DESC,id DESC)
    WHERE oauth_profile='codex' AND status IN ('failed','succeeded') AND finished_at IS NOT NULL;
