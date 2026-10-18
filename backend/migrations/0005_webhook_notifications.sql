CREATE SCHEMA notifications;

CREATE TABLE notifications.webhooks (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_id uuid NOT NULL UNIQUE REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    enabled boolean NOT NULL,
    encrypted_url bytea NOT NULL,
    encrypted_secret bytea NOT NULL,
    normalized_host text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhooks_account_idx ON notifications.webhooks(account_id, monitor_id);

CREATE TABLE notifications.deliveries (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    endpoint_id uuid NOT NULL REFERENCES notifications.webhooks(id) ON DELETE CASCADE,
    endpoint_version bigint NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    body bytea NOT NULL CHECK (octet_length(body) <= 16384),
    status text NOT NULL CHECK (status IN ('pending', 'leased', 'delivered', 'failed', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    attempt_token uuid,
    deadline_at timestamptz NOT NULL,
    last_http_status integer CHECK (last_http_status BETWEEN 100 AND 599),
    last_error_code text NOT NULL DEFAULT '' CHECK (length(last_error_code) <= 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (event_id, endpoint_id),
    CHECK ((status = 'leased') = (lease_until IS NOT NULL AND attempt_token IS NOT NULL)),
    CHECK ((status IN ('delivered', 'failed', 'cancelled')) = (completed_at IS NOT NULL))
);
CREATE INDEX deliveries_queue_idx ON notifications.deliveries(next_attempt_at, id)
    WHERE status IN ('pending', 'leased');
CREATE INDEX deliveries_order_idx ON notifications.deliveries(endpoint_id, sequence, id)
    WHERE status IN ('pending', 'leased');
CREATE INDEX deliveries_history_idx ON notifications.deliveries(account_id, monitor_id, created_at DESC, id DESC);
CREATE INDEX deliveries_cleanup_idx ON notifications.deliveries(completed_at, id)
    WHERE status IN ('delivered', 'failed', 'cancelled');

-- A cancelled in-flight POST can still be running outside the database.
-- Reservations keep account and target-host capacity occupied until that POST
-- returns or its lease expires, even when the endpoint is deleted by cascade.
CREATE TABLE notifications.reservations (
    attempt_token uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    normalized_host text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX reservations_expiry_idx ON notifications.reservations(expires_at);

-- Earlier notification requests predate a durable delivery route. Mark only
-- those historical events processed; replaying them would invent deliveries.
UPDATE outbox.events SET processed_at = now()
WHERE type = 'NotificationRequested' AND processed_at IS NULL;
