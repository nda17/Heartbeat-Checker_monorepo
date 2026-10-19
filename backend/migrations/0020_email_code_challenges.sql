-- Code proofs are separate from legacy links and OAuth registration challenges.
CREATE TABLE auth.email_code_challenges (
    id uuid PRIMARY KEY,
    purpose text NOT NULL CHECK (purpose IN ('registration','notification_email')),
    email text NOT NULL,
    account_id uuid REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    name text,
    password_hash text,
    browser_hash bytea,
    destination_id uuid,
    destination_version bigint NOT NULL DEFAULT 0 CHECK (destination_version>=0),
    code_hash bytea NOT NULL CHECK (octet_length(code_hash)=32),
    deliverable boolean NOT NULL DEFAULT true,
    failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts BETWEEN 0 AND 5),
    expires_at timestamptz NOT NULL,
    resend_available_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((purpose='registration' AND account_id IS NULL AND name IS NOT NULL AND password_hash IS NOT NULL AND browser_hash IS NOT NULL AND octet_length(browser_hash)=32 AND destination_id IS NULL AND destination_version=0)
        OR (purpose='notification_email' AND account_id IS NOT NULL AND name IS NULL AND password_hash IS NULL AND browser_hash IS NULL)),
    CHECK ((destination_id IS NULL AND destination_version=0) OR (destination_id IS NOT NULL AND destination_version>0))
);
CREATE INDEX email_code_challenges_owner_idx ON auth.email_code_challenges(account_id,created_at DESC) WHERE purpose='notification_email';
CREATE INDEX email_code_challenges_expiry_idx ON auth.email_code_challenges(expires_at);
ALTER TABLE auth.mail_jobs DROP CONSTRAINT mail_jobs_purpose_check,
    ADD CONSTRAINT mail_jobs_purpose_check CHECK (purpose IN ('registration','reset','email_verification','email_code'));
ALTER TABLE notifications.messenger_destinations ADD COLUMN email_proof_kind text,
    ADD COLUMN email_verified_at timestamptz;
-- A legacy destination still depends on the credential's real verification.
-- Its historical timestamp cannot be reconstructed and must stay NULL.
UPDATE notifications.messenger_destinations SET email_proof_kind='account_email' WHERE provider='email';
ALTER TABLE notifications.messenger_destinations ADD CONSTRAINT messenger_destinations_email_proof_check CHECK (
    (provider='email' AND email_proof_kind IS NOT NULL AND email_proof_kind IN ('account_email','code')
        AND (email_proof_kind<>'code' OR email_verified_at IS NOT NULL))
    OR (provider<>'email' AND email_proof_kind IS NULL AND email_verified_at IS NULL));
