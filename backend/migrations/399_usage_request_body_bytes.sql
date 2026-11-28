-- Preserve completed inbound sizes before migration 400 retires request diagnostics.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_body_bytes bigint;

DO $$
BEGIN
    IF to_regclass('request_timing_details') IS NOT NULL THEN
        UPDATE usage_logs AS u
        SET request_body_bytes = sizes.bytes
        FROM (
            SELECT usage_log_id,
                   MAX(CASE WHEN detail->'body_complete' = 'true'::jsonb
                             AND jsonb_typeof(detail->'body_bytes') = 'number'
                             AND (detail->>'body_bytes') ~ '^[0-9]{1,18}$'
                            THEN (detail->>'body_bytes')::bigint END) AS bytes
            FROM request_timing_details
            GROUP BY usage_log_id
            HAVING count(*) = 1
        ) sizes
        WHERE u.id = sizes.usage_log_id AND u.request_body_bytes IS NULL AND sizes.bytes IS NOT NULL;
    END IF;
END $$;
