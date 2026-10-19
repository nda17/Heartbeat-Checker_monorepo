ALTER TABLE auth.credentials ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE auth.registration_challenges ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE auth.registration_challenges
    ADD COLUMN oauth_provider text CHECK (oauth_provider IN ('vk','yandex')),
    ADD COLUMN oauth_subject text CHECK (length(oauth_subject) BETWEEN 1 AND 255),
    ADD COLUMN oauth_browser_hash bytea,
    ADD COLUMN oauth_return_to text,
    ADD CONSTRAINT registration_challenge_method CHECK (
        (password_hash IS NOT NULL AND oauth_provider IS NULL AND oauth_subject IS NULL AND oauth_browser_hash IS NULL AND oauth_return_to IS NULL)
        OR (password_hash IS NULL AND oauth_provider IS NOT NULL AND oauth_subject IS NOT NULL AND oauth_browser_hash IS NOT NULL AND octet_length(oauth_browser_hash)=32 AND oauth_return_to IS NOT NULL)
    );
CREATE INDEX registration_challenges_oauth_browser_idx ON auth.registration_challenges(oauth_browser_hash) WHERE oauth_browser_hash IS NOT NULL;
CREATE TABLE auth.oauth_identities (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('vk','yandex')),
    subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(provider,subject),
    UNIQUE(account_id,provider)
);
CREATE TABLE auth.oauth_attempts (
    state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash)=32),
    provider text NOT NULL CHECK (provider IN ('vk','yandex')),
    mode text NOT NULL CHECK (mode IN ('login','link')),
    pkce_challenge bytea NOT NULL CHECK (octet_length(pkce_challenge)=32),
    return_to text NOT NULL,
    account_id uuid REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid REFERENCES auth.sessions(id) ON DELETE CASCADE,
    credential_version bigint,
    session_token_hash bytea,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((mode='login' AND account_id IS NULL AND session_id IS NULL AND credential_version IS NULL AND session_token_hash IS NULL)
        OR (mode='link' AND account_id IS NOT NULL AND session_id IS NOT NULL AND credential_version IS NOT NULL AND session_token_hash IS NOT NULL AND octet_length(session_token_hash)=32))
);
CREATE INDEX oauth_attempts_expires_idx ON auth.oauth_attempts(expires_at);
