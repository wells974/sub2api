-- Snapshot the old Jev connection and effective switch once. Later audit
-- changes must not affect grading, including credential rotation.
DO $$
DECLARE
    audit jsonb;
    profile jsonb;
    active boolean;
BEGIN
    BEGIN
        SELECT value::jsonb INTO audit FROM settings WHERE key = 'content_moderation_config';
    EXCEPTION WHEN invalid_text_representation THEN
        audit := NULL;
    END;
    profile := audit->'typesafe';
    IF jsonb_typeof(profile) IS DISTINCT FROM 'object' THEN
        profile := '{}'::jsonb;
    END IF;
    active := EXISTS (SELECT 1 FROM settings WHERE key = 'risk_control_enabled' AND value = 'true')
        AND audit->'enabled' = 'true'::jsonb
        AND COALESCE(audit->>'mode', '') <> 'off'
        AND jsonb_typeof(profile->'api_keys') = 'array'
        AND profile->'api_keys' <> '[]'::jsonb;
    INSERT INTO settings (key, value)
    VALUES ('quality_jev_config', jsonb_build_object(
        'enabled', COALESCE(active, false),
        'typesafe', profile - 'thresholds' - 'category_settings'
    )::text)
    ON CONFLICT (key) DO NOTHING;
END $$;
