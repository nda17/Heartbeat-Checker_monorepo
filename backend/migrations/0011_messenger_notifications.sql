-- Human-readable incident numbers are stable across all channels. Existing
-- incidents receive numbers when this additive migration is applied.
ALTER TABLE incidents.incidents
    ADD COLUMN number bigint GENERATED ALWAYS AS IDENTITY;
ALTER TABLE incidents.incidents
    ADD CONSTRAINT incidents_number_unique UNIQUE (number);

CREATE TABLE notifications.messenger_links (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('telegram', 'max')),
    kind text NOT NULL CHECK (kind IN ('private', 'group', 'topic')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'observed', 'candidate', 'confirmed', 'expired')),
    encrypted_observation bytea,
    encrypted_candidate bytea,
    destination_id uuid,
    expires_at timestamptz NOT NULL,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    lease_until timestamptz,
    attempt_token uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((lease_until IS NULL) = (attempt_token IS NULL)),
    CHECK (kind = 'private' OR provider = 'telegram')
);
CREATE INDEX messenger_links_owner_idx ON notifications.messenger_links(account_id, created_at DESC);
CREATE INDEX messenger_links_verify_idx ON notifications.messenger_links(next_attempt_at, id) WHERE status = 'observed';

CREATE TABLE notifications.messenger_destinations (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('telegram', 'max')),
    kind text NOT NULL CHECK (kind IN ('private', 'group', 'topic')),
    recipient_fingerprint bytea NOT NULL CHECK (octet_length(recipient_fingerprint) = 32),
    user_fingerprint bytea NOT NULL CHECK (octet_length(user_fingerprint) = 32),
    encrypted_target bytea NOT NULL,
    status text NOT NULL DEFAULT 'ready' CHECK (status IN ('ready', 'disconnected')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, provider, recipient_fingerprint),
    CHECK (kind = 'private' OR provider = 'telegram')
);
CREATE INDEX messenger_destinations_owner_idx ON notifications.messenger_destinations(account_id, provider);
ALTER TABLE notifications.messenger_links
    ADD CONSTRAINT messenger_links_destination_fk FOREIGN KEY (destination_id)
    REFERENCES notifications.messenger_destinations(id) ON DELETE SET NULL;

CREATE TABLE notifications.messenger_routes (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('telegram', 'max')),
    destination_id uuid NOT NULL REFERENCES notifications.messenger_destinations(id),
    enabled boolean NOT NULL DEFAULT false,
    settings_version bigint NOT NULL DEFAULT 1 CHECK (settings_version > 0),
    delivery_generation bigint NOT NULL DEFAULT 1 CHECK (delivery_generation > 0),
    reminder_minutes integer NOT NULL DEFAULT 0 CHECK (reminder_minutes IN (0,15,30,60,180,360,1440)),
    reminder_incident_id uuid,
    next_due timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (monitor_id, provider)
);
CREATE INDEX messenger_routes_destination_idx ON notifications.messenger_routes(destination_id);

CREATE TABLE notifications.messenger_deliveries (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    route_id uuid NOT NULL REFERENCES notifications.messenger_routes(id) ON DELETE CASCADE,
    destination_id uuid NOT NULL REFERENCES notifications.messenger_destinations(id),
    incident_id uuid NOT NULL,
    route_generation bigint NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    kind text NOT NULL CHECK (kind IN ('down', 'up', 'reminder')),
    encrypted_snapshot bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'delivered', 'failed', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    attempt_token uuid,
    deadline_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    error_code text NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (event_id, route_id),
    CHECK ((status = 'leased') = (lease_until IS NOT NULL AND attempt_token IS NOT NULL)),
    CHECK ((status IN ('delivered','failed','cancelled')) = (completed_at IS NOT NULL))
);
CREATE INDEX messenger_deliveries_queue_idx ON notifications.messenger_deliveries(next_attempt_at, id)
    WHERE status IN ('pending','leased');
CREATE INDEX messenger_deliveries_order_idx ON notifications.messenger_deliveries(route_id, sequence, id)
    WHERE status IN ('pending','leased');
CREATE INDEX messenger_deliveries_retention_idx ON notifications.messenger_deliveries(completed_at, id)
    WHERE status IN ('delivered','failed','cancelled');

-- A cancelled in-flight provider request still occupies the target until the
-- request returns or its lease expires.
CREATE TABLE notifications.messenger_reservations (
    attempt_token uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    provider text NOT NULL,
    recipient_fingerprint bytea NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX messenger_reservations_expiry_idx ON notifications.messenger_reservations(expires_at);

CREATE TABLE notifications.messenger_target_rates (
    provider text NOT NULL CHECK (provider IN ('telegram', 'max')),
    recipient_fingerprint bytea NOT NULL CHECK (octet_length(recipient_fingerprint) = 32),
    next_send_at timestamptz NOT NULL,
    PRIMARY KEY (provider, recipient_fingerprint)
);
