CREATE TABLE quality_patrol_settings (
    id integer PRIMARY KEY CHECK (id = 1),
    config jsonb NOT NULL
);

CREATE TABLE quality_patrol_rounds (
    rule_key text NOT NULL,
    scheduled_at timestamptz NOT NULL,
    rule_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    done boolean NOT NULL DEFAULT false,
    PRIMARY KEY (rule_key, scheduled_at)
);
CREATE INDEX quality_patrol_pending ON quality_patrol_rounds(created_at) WHERE NOT done;

-- Freeze membership before workers start. Each plan contributes its account once,
-- independently of sample retention and of the number of models in the run.
CREATE TABLE quality_patrol_members (
    rule_key text NOT NULL,
    scheduled_at timestamptz NOT NULL,
    plan_id bigint NOT NULL,
    account_id bigint NOT NULL,
    outcome text NOT NULL DEFAULT 'waiting'
        CHECK (outcome IN ('waiting','running','skipped','passed','failed','inconclusive')),
    deadline timestamptz NOT NULL DEFAULT now() + interval '15 minutes',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_key, scheduled_at, plan_id),
    FOREIGN KEY (rule_key, scheduled_at) REFERENCES quality_patrol_rounds ON DELETE CASCADE
);
CREATE INDEX quality_patrol_plan_run ON quality_patrol_members(plan_id, scheduled_at);

ALTER TABLE account_ops_threshold_events DROP CONSTRAINT account_ops_threshold_events_kind_check;
ALTER TABLE account_ops_threshold_events ADD CONSTRAINT account_ops_threshold_events_kind_check
    CHECK (kind IN ('balance_threshold','quota_threshold','account_pool','quality_patrol'));
