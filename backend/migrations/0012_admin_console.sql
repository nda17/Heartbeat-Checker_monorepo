-- Administrative grants are explicit production entitlements. Existing local
-- and mock sources keep their original eligibility rules in billing.Lock.
ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_source_check
    CHECK (source IN ('local_test', 'mock_test', 'admin_grant'));

ALTER TABLE accounts.accounts ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0);

ALTER TABLE auth.sessions
    ADD COLUMN mfa_version bigint,
    ADD COLUMN mfa_verified_until timestamptz,
    ADD COLUMN mfa_step_up_until timestamptz;

CREATE TABLE auth.mfa_factors (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    secret_cipher bytea NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    last_used_counter bigint NOT NULL DEFAULT -1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth.mfa_enrollments (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL UNIQUE REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES auth.sessions(id) ON DELETE CASCADE,
    credential_version bigint NOT NULL,
    factor_version bigint NOT NULL,
    secret_cipher bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth.mfa_recovery_codes (
    account_id uuid NOT NULL REFERENCES auth.mfa_factors(account_id) ON DELETE CASCADE,
    factor_version bigint NOT NULL,
    code_hash bytea NOT NULL,
    used_at timestamptz,
    PRIMARY KEY (account_id, factor_version, code_hash)
);

CREATE SCHEMA admin;

-- Append-only business audit. Actor identifiers deliberately have no cascading
-- session FK so revocation and auth retention cannot erase administrative acts.
CREATE TABLE admin.audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_id text UNIQUE,
    actor_account_id uuid,
    actor_session_id uuid,
    target_account_id uuid,
    target_id uuid,
    action text NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    before_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    after_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_created_idx ON admin.audit_events(created_at DESC, id DESC);
CREATE INDEX audit_events_target_idx ON admin.audit_events(target_account_id, created_at DESC, id DESC);
