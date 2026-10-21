-- Native credentials share the existing revocation, credential-version and
-- session-retention rules, while browser cookies remain web-only credentials.
ALTER TABLE auth.sessions ADD COLUMN client_kind text NOT NULL DEFAULT 'web'
    CHECK (client_kind IN ('web', 'android'));
ALTER TABLE auth.sessions ADD COLUMN revoke_token_hash bytea
    CHECK ((client_kind = 'android' AND revoke_token_hash IS NOT NULL AND octet_length(revoke_token_hash) = 32)
        OR (client_kind = 'web' AND revoke_token_hash IS NULL));

CREATE TABLE auth.native_oauth_attempts (
    id uuid PRIMARY KEY,
    provider text NOT NULL CHECK (provider IN ('vk', 'yandex')),
    mode text NOT NULL CHECK (mode IN ('login', 'link')),
    state_hash bytea NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    pkce_challenge text NOT NULL CHECK (length(pkce_challenge) = 43),
    account_id uuid REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid REFERENCES auth.sessions(id) ON DELETE CASCADE,
    credential_version bigint,
    session_token_hash bytea,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((mode = 'login' AND account_id IS NULL AND session_id IS NULL AND credential_version IS NULL AND session_token_hash IS NULL)
        OR (mode = 'link' AND account_id IS NOT NULL AND session_id IS NOT NULL AND credential_version IS NOT NULL
            AND session_token_hash IS NOT NULL AND octet_length(session_token_hash) = 32))
);
CREATE INDEX native_oauth_attempts_expiry_idx ON auth.native_oauth_attempts(expires_at);

ALTER TABLE billing.orders ADD COLUMN return_target text NOT NULL DEFAULT 'web'
    CHECK (return_target IN ('web', 'android'));
CREATE FUNCTION billing.keep_order_return_target_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.return_target IS DISTINCT FROM OLD.return_target THEN
        RAISE EXCEPTION 'payment return target is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER order_return_target_immutable BEFORE UPDATE ON billing.orders
    FOR EACH ROW EXECUTE FUNCTION billing.keep_order_return_target_immutable();

ALTER TABLE notifications.push_installations ADD COLUMN native_bind_fingerprint bytea
    CHECK (native_bind_fingerprint IS NULL OR octet_length(native_bind_fingerprint) = 32);
