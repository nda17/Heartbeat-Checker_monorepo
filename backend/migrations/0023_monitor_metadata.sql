ALTER TABLE monitors.monitors
    ADD COLUMN time_zone text NOT NULL DEFAULT 'Europe/Moscow',
    ADD COLUMN check_config_version bigint;
UPDATE monitors.monitors SET check_config_version=version;
ALTER TABLE monitors.monitors ALTER COLUMN check_config_version SET NOT NULL,
    ALTER COLUMN check_config_version SET DEFAULT 1,
    ADD CONSTRAINT monitors_check_config_version_positive CHECK (check_config_version > 0);

-- A separate generation prevents stale DNS enrichment overwriting an edited target.
CREATE TABLE monitors.provider_lookups (
    monitor_id uuid PRIMARY KEY REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    target_host text NOT NULL,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','unknown')),
    provider_names text[] NOT NULL DEFAULT '{}',
    asns bigint[] NOT NULL DEFAULT '{}',
    checked_at timestamptz,
    dataset_version text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    attempt_token uuid,
    CHECK (cardinality(provider_names) <= 3),
    CHECK (cardinality(asns) <= 4)
);
CREATE INDEX provider_lookups_due ON monitors.provider_lookups(next_attempt_at,monitor_id) WHERE status='pending';

-- HTTP target_key is a normalized host:port; native checks carry normalized host.
INSERT INTO monitors.provider_lookups(monitor_id,target_host)
SELECT id,CASE WHEN check_type='http' THEN
    CASE WHEN left(target_key,1)='[' THEN split_part(substr(target_key,2),']',1)
         ELSE regexp_replace(target_key,':[0-9]+$','') END
    ELSE host END
FROM monitors.monitors;
