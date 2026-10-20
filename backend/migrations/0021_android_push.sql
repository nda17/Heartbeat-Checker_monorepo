-- Native push is independent of the existing webhook and messenger routes.
-- A device credential proves installation ownership; it never grants web access.
CREATE TABLE notifications.push_installations (
    id uuid PRIMARY KEY,
    credential_hash bytea NOT NULL UNIQUE CHECK (octet_length(credential_hash)=32),
    account_id uuid REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid REFERENCES auth.sessions(id) ON DELETE SET NULL,
    provider text NOT NULL DEFAULT 'rustore' CHECK (provider='rustore'),
    device_label text NOT NULL CHECK (length(device_label) BETWEEN 1 AND 100),
    app_version text NOT NULL CHECK (length(app_version) BETWEEN 1 AND 50),
    encrypted_token bytea,
    token_fingerprint bytea,
    registration_version bigint NOT NULL DEFAULT 1 CHECK (registration_version>0),
    binding_generation bigint NOT NULL DEFAULT 0 CHECK (binding_generation>=0),
    permission text NOT NULL DEFAULT 'denied' CHECK (permission IN ('granted','denied')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','revoked')),
    token_valid boolean NOT NULL DEFAULT true,
    provider_available boolean NOT NULL DEFAULT true,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    request_count integer NOT NULL DEFAULT 0,
    request_window timestamptz NOT NULL DEFAULT now(),
    last_pairing_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((encrypted_token IS NULL)=(token_fingerprint IS NULL))
);
CREATE INDEX push_installations_account_idx ON notifications.push_installations(account_id,id);
CREATE UNIQUE INDEX push_installations_active_token_idx ON notifications.push_installations(provider,token_fingerprint)
    WHERE status<>'revoked' AND token_fingerprint IS NOT NULL;

CREATE TABLE notifications.push_pairings (
    id uuid PRIMARY KEY,
    installation_id uuid NOT NULL REFERENCES notifications.push_installations(id) ON DELETE CASCADE,
    code_challenge text NOT NULL CHECK (length(code_challenge)=43),
    client_state text NOT NULL CHECK (length(client_state)=43),
    completion_code_hash bytea CHECK (octet_length(completion_code_hash)=32),
    request_fingerprint bytea NOT NULL,
    read_count integer NOT NULL DEFAULT 0,
    exchange_attempts integer NOT NULL DEFAULT 0,
    account_id uuid REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid REFERENCES auth.sessions(id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_pairings_expiry_idx ON notifications.push_pairings(expires_at);
CREATE INDEX push_pairings_installation_idx ON notifications.push_pairings(installation_id,created_at DESC);

-- A missing monitor override inherits the account default. That default becomes
-- true only after the first successful session-bound exchange with permission.
CREATE TABLE notifications.push_account_settings (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    default_enabled boolean NOT NULL DEFAULT false
);

CREATE TABLE notifications.push_monitor_settings (
    monitor_id uuid PRIMARY KEY REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    enabled boolean NOT NULL,
    settings_version bigint NOT NULL DEFAULT 1 CHECK (settings_version>0),
    delivery_generation bigint NOT NULL DEFAULT 1 CHECK (delivery_generation>0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_monitor_settings_account_idx ON notifications.push_monitor_settings(account_id,monitor_id);

CREATE TABLE notifications.push_deliveries (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_id uuid NOT NULL REFERENCES monitors.monitors(id) ON DELETE CASCADE,
    incident_id uuid NOT NULL REFERENCES incidents.incidents(id) ON DELETE CASCADE,
    installation_id uuid NOT NULL REFERENCES notifications.push_installations(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('down','up')),
    sequence bigint NOT NULL,
    policy_generation bigint NOT NULL,
    binding_generation bigint NOT NULL,
    registration_version bigint NOT NULL,
    encrypted_snapshot bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','leased','accepted','failed','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    attempt_token uuid,
    deadline_at timestamptz NOT NULL,
    error_code text NOT NULL DEFAULT '' CHECK (length(error_code)<=64),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE(event_id,installation_id)
);
CREATE INDEX push_deliveries_ready_idx ON notifications.push_deliveries(next_attempt_at,id) WHERE status IN ('pending','leased');
CREATE INDEX push_deliveries_order_idx ON notifications.push_deliveries(monitor_id,installation_id,sequence) WHERE status IN ('pending','leased');
CREATE INDEX push_deliveries_retention_idx ON notifications.push_deliveries(completed_at,id) WHERE completed_at IS NOT NULL;
