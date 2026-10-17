CREATE SCHEMA IF NOT EXISTS accounts;
CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE accounts.accounts (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 2 AND 100),
    role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'blocked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth.credentials (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    email text NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    password_changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth.registration_challenges (
    id uuid PRIMARY KEY,
    email text NOT NULL,
    name text NOT NULL,
    password_hash text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    failed_attempts integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX registration_challenges_email_idx ON auth.registration_challenges(email, created_at DESC);

CREATE TABLE auth.sessions (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    credential_version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    ip inet,
    user_agent text NOT NULL DEFAULT '' CHECK (length(user_agent) <= 500)
);
CREATE INDEX sessions_account_idx ON auth.sessions(account_id, created_at DESC);

CREATE TABLE auth.password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    credential_version bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_account_idx ON auth.password_reset_tokens(account_id);

CREATE TABLE auth.rate_limits (
    scope text NOT NULL,
    key_hash bytea NOT NULL,
    count integer NOT NULL,
    window_end timestamptz NOT NULL,
    PRIMARY KEY (scope, key_hash)
);
