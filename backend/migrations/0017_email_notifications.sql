-- No backfill: an existing credential or private provisioning is not proof of
-- mailbox ownership. New proof is recorded only after an actual email challenge.
ALTER TABLE auth.credentials ADD COLUMN email_verified_at timestamptz;
CREATE TABLE auth.email_verification_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    email text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_verification_tokens_owner_idx ON auth.email_verification_tokens(account_id,created_at DESC);
ALTER TABLE auth.mail_jobs DROP CONSTRAINT mail_jobs_purpose_check,
    ADD CONSTRAINT mail_jobs_purpose_check CHECK (purpose IN ('registration','reset','email_verification'));

ALTER TABLE notifications.messenger_destinations
    DROP CONSTRAINT messenger_destinations_provider_check,
    DROP CONSTRAINT messenger_destinations_kind_check,
    DROP CONSTRAINT messenger_destinations_check,
    ADD CONSTRAINT messenger_destinations_provider_check CHECK (provider IN ('telegram', 'max', 'email')),
    ADD CONSTRAINT messenger_destinations_kind_check CHECK (
        (provider = 'email' AND kind = 'email') OR
        (provider = 'max' AND kind = 'private') OR
        (provider = 'telegram' AND kind IN ('private', 'group', 'topic'))
    );
CREATE UNIQUE INDEX messenger_destinations_email_owner_idx
    ON notifications.messenger_destinations(account_id) WHERE provider = 'email';

ALTER TABLE notifications.messenger_routes
    DROP CONSTRAINT messenger_routes_provider_check,
    ADD CONSTRAINT messenger_routes_provider_check CHECK (provider IN ('telegram', 'max', 'email'));
ALTER TABLE notifications.messenger_target_rates
    DROP CONSTRAINT messenger_target_rates_provider_check,
    ADD CONSTRAINT messenger_target_rates_provider_check CHECK (provider IN ('telegram', 'max', 'email'));
