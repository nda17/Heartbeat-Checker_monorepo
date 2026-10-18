ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_source_check CHECK (source IN ('local_test', 'mock_test'));
ALTER TABLE billing.entitlements ADD COLUMN renewal_anchor_day smallint CHECK (renewal_anchor_day BETWEEN 1 AND 31);

CREATE TABLE billing.orders (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    idempotency_key uuid NOT NULL,
    provider text NOT NULL CHECK (provider = 'mock'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'canceled')),
    monitor_count integer NOT NULL CHECK (monitor_count BETWEEN 1 AND 1000),
    amount_minor integer NOT NULL CHECK (amount_minor = monitor_count * 2400),
    currency text NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '30 minutes',
    completed_at timestamptz,
    active_until timestamptz,
    UNIQUE (account_id, idempotency_key),
    CHECK ((status = 'pending' AND completed_at IS NULL) OR (status <> 'pending' AND completed_at IS NOT NULL)),
    CHECK ((status = 'succeeded' AND active_until IS NOT NULL) OR (status <> 'succeeded' AND active_until IS NULL))
);
CREATE UNIQUE INDEX orders_pending_idx ON billing.orders(account_id) WHERE status = 'pending';
CREATE INDEX orders_history_idx ON billing.orders(account_id, created_at DESC, id DESC);

-- Contains only payment identifiers and technical state; contact data and bot credentials are absent.
CREATE TABLE billing.payment_reports (
    order_id uuid PRIMARY KEY REFERENCES billing.orders(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    attempt_token uuid,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    last_error_code text CHECK (last_error_code IN ('provider_unavailable', 'provider_http', 'provider_rejected', 'provider_response_invalid', 'report_invalid', 'request_invalid', 'encode_failed', 'delivery_unconfirmed')),
    last_http_status smallint CHECK (last_http_status BETWEEN 100 AND 599),
    CHECK ((status = 'sending' AND attempt_token IS NOT NULL AND lease_until IS NOT NULL) OR
           (status <> 'sending' AND attempt_token IS NULL AND lease_until IS NULL))
);
CREATE INDEX payment_reports_pending_idx ON billing.payment_reports(next_attempt_at) WHERE status IN ('pending', 'sending');
