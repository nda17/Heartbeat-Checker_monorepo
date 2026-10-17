CREATE SCHEMA billing;
CREATE SCHEMA monitors;
CREATE SCHEMA incidents;
CREATE SCHEMA outbox;

CREATE TABLE billing.entitlements (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_limit integer NOT NULL CHECK (monitor_limit BETWEEN 1 AND 1000),
    valid_until timestamptz NOT NULL,
    source text NOT NULL CHECK (source IN ('local_test')),
    version bigint NOT NULL DEFAULT 1,
    granted_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500)
);
CREATE TABLE monitors.monitors (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    url text NOT NULL CHECK (length(url) <= 2048),
    target_key text NOT NULL,
    method text NOT NULL CHECK (method IN ('GET', 'HEAD')),
    expected_status integer NOT NULL CHECK (expected_status BETWEEN 100 AND 599),
    body_contains text NOT NULL DEFAULT '' CHECK (length(body_contains) <= 1000),
    enabled boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    outcome text NOT NULL DEFAULT 'UNKNOWN' CHECK (outcome IN ('UP', 'DOWN', 'UNKNOWN')),
    last_checked_at timestamptz,
    last_http_status integer,
    last_latency_ms bigint,
    next_check_at timestamptz NOT NULL DEFAULT now(),
    run_id uuid,
    attempt_token uuid,
    lease_until timestamptz,
    sequence bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (method <> 'HEAD' OR body_contains = ''),
    CHECK ((run_id IS NULL AND attempt_token IS NULL AND lease_until IS NULL) OR
           (run_id IS NOT NULL AND attempt_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX monitors_owner_idx ON monitors.monitors(account_id, created_at, id);
CREATE INDEX monitors_due_idx ON monitors.monitors(next_check_at) WHERE enabled;
CREATE INDEX monitors_lease_idx ON monitors.monitors(lease_until) WHERE lease_until IS NOT NULL;
CREATE TABLE monitors.results (
    id uuid PRIMARY KEY,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    run_id uuid NOT NULL,
    attempt_token uuid NOT NULL UNIQUE,
    config_version bigint NOT NULL,
    sequence bigint NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('UP', 'DOWN', 'UNKNOWN')),
    checked_at timestamptz NOT NULL,
    http_status integer,
    latency_ms bigint NOT NULL CHECK (latency_ms BETWEEN 0 AND 12000),
    error_code text NOT NULL DEFAULT '' CHECK (length(error_code) <= 100),
    accepted_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (monitor_id, sequence)
);
CREATE INDEX results_monitor_idx ON monitors.results(monitor_id, checked_at DESC, id DESC);
CREATE INDEX results_retention_idx ON monitors.results(accepted_at);
CREATE TABLE monitors.daily_statistics (
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    day date NOT NULL,
    up_count bigint NOT NULL DEFAULT 0,
    down_count bigint NOT NULL DEFAULT 0,
    unknown_count bigint NOT NULL DEFAULT 0,
    latency_sum_ms bigint NOT NULL DEFAULT 0,
    latency_count bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (monitor_id, day)
);
CREATE INDEX daily_statistics_retention_idx ON monitors.daily_statistics(day);
CREATE TABLE incidents.monitor_state (
    monitor_id uuid PRIMARY KEY REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    last_sequence bigint NOT NULL DEFAULT 0,
    failure_count integer NOT NULL DEFAULT 0,
    first_failure_at timestamptz
);
CREATE TABLE incidents.incidents (
    id uuid PRIMARY KEY,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    opened_at timestamptz NOT NULL,
    confirmed_at timestamptz NOT NULL,
    closed_at timestamptz
);
CREATE UNIQUE INDEX incidents_one_open_idx ON incidents.incidents(monitor_id) WHERE closed_at IS NULL;
CREATE INDEX incidents_history_idx ON incidents.incidents(monitor_id, opened_at DESC);
CREATE TABLE outbox.events (
    id uuid PRIMARY KEY,
    aggregate_id uuid NOT NULL,
    account_id uuid NOT NULL,
    type text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX outbox_pending_idx ON outbox.events(created_at, id) WHERE processed_at IS NULL;
