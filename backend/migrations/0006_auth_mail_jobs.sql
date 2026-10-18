CREATE TABLE auth.mail_jobs (
    id uuid PRIMARY KEY,
    purpose text NOT NULL CHECK (purpose IN ('registration', 'reset')),
    token_hash bytea NOT NULL,
    payload bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'expired')),
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    lease_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CHECK (attempts >= 0),
    CHECK ((status = 'sending') = (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX mail_jobs_claim_idx ON auth.mail_jobs(next_attempt_at, created_at)
    WHERE status IN ('pending', 'sending');
