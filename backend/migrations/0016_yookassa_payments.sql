ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_source_check
    CHECK (source IN ('local_test', 'mock_test', 'admin_grant', 'yookassa_test'));

ALTER TABLE billing.orders DROP CONSTRAINT orders_provider_check;
ALTER TABLE billing.orders DROP CONSTRAINT orders_check2;
ALTER TABLE billing.orders
    ALTER COLUMN expires_at DROP NOT NULL,
    ADD CONSTRAINT orders_provider_check CHECK (provider IN ('mock', 'yookassa')),
    ADD CONSTRAINT orders_active_until_check CHECK (active_until IS NULL OR status = 'succeeded'),
    ADD CONSTRAINT orders_mock_fulfillment_check CHECK (provider <> 'mock' OR (expires_at IS NOT NULL AND ((status = 'succeeded') = (active_until IS NOT NULL)))),
    ADD COLUMN provider_mode text NOT NULL DEFAULT 'test' CHECK (provider_mode = 'test'),
    ADD COLUMN provider_shop_id text,
    ADD COLUMN provider_payment_id text,
    ADD COLUMN provider_request bytea,
    ADD COLUMN first_attempt_at timestamptz,
    ADD COLUMN idempotency_deadline timestamptz,
    ADD COLUMN confirmation_url text NOT NULL DEFAULT '' CHECK (length(confirmation_url) <= 2048),
    ADD COLUMN reconciliation_status text NOT NULL DEFAULT 'pending' CHECK (reconciliation_status IN ('pending', 'complete', 'manual_review', 'stopped')),
    ADD COLUMN next_reconcile_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN reconcile_token uuid,
    ADD COLUMN reconcile_lease_until timestamptz,
    ADD COLUMN reconcile_attempts integer NOT NULL DEFAULT 0 CHECK (reconcile_attempts >= 0),
    ADD COLUMN last_error_code text NOT NULL DEFAULT '' CHECK (length(last_error_code) <= 64),
    ADD COLUMN fulfillment_status text NOT NULL DEFAULT 'pending' CHECK (fulfillment_status IN ('pending', 'fulfilled', 'blocked', 'not_required')),
    ADD COLUMN fulfillment_reason text NOT NULL DEFAULT '' CHECK (fulfillment_reason IN ('', 'admin_grant', 'account_deleted', 'account_inactive', 'capacity_changed')),
    ADD COLUMN fulfilled_at timestamptz,
    ADD CONSTRAINT orders_yookassa_config CHECK (provider <> 'yookassa' OR (provider_shop_id IS NOT NULL AND length(provider_shop_id) BETWEEN 1 AND 32 AND provider_request IS NOT NULL AND octet_length(provider_request) BETWEEN 1 AND 8192)),
    ADD CONSTRAINT orders_provider_id CHECK (provider_payment_id IS NULL OR length(provider_payment_id) BETWEEN 16 AND 64),
    ADD CONSTRAINT orders_attempt_window CHECK ((first_attempt_at IS NULL AND idempotency_deadline IS NULL) OR (first_attempt_at IS NOT NULL AND idempotency_deadline IS NOT NULL AND idempotency_deadline = first_attempt_at + interval '23 hours')),
    ADD CONSTRAINT orders_reconcile_lease CHECK ((reconcile_token IS NULL) = (reconcile_lease_until IS NULL)),
    ADD CONSTRAINT orders_fulfilled_check CHECK (fulfillment_status <> 'fulfilled' OR (status = 'succeeded' AND active_until IS NOT NULL AND fulfilled_at IS NOT NULL));

UPDATE billing.orders SET
    fulfillment_status = CASE status WHEN 'succeeded' THEN 'fulfilled' WHEN 'canceled' THEN 'not_required' ELSE 'pending' END,
    fulfilled_at = CASE WHEN status = 'succeeded' THEN completed_at END,
    reconciliation_status = CASE WHEN status = 'pending' THEN 'pending' ELSE 'complete' END;

CREATE UNIQUE INDEX orders_provider_payment_idx ON billing.orders(provider, provider_shop_id, provider_payment_id) WHERE provider_payment_id IS NOT NULL;
CREATE INDEX orders_reconciliation_idx ON billing.orders(next_reconcile_at) WHERE provider = 'yookassa' AND status = 'pending' AND reconciliation_status = 'pending';

-- Preserve the exact operation across retries and process restarts. The provider
-- idempotence key is the globally unique order UUID, not the account-scoped key.
CREATE FUNCTION billing.keep_payment_request_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.provider = 'yookassa' AND (
        NEW.provider IS DISTINCT FROM OLD.provider OR NEW.account_id IS DISTINCT FROM OLD.account_id OR
        NEW.monitor_count IS DISTINCT FROM OLD.monitor_count OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor OR
        NEW.currency IS DISTINCT FROM OLD.currency OR NEW.provider_mode IS DISTINCT FROM OLD.provider_mode OR
        NEW.provider_shop_id IS DISTINCT FROM OLD.provider_shop_id OR NEW.provider_request IS DISTINCT FROM OLD.provider_request OR
        NEW.id IS DISTINCT FROM OLD.id OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
        (OLD.first_attempt_at IS NOT NULL AND (NEW.first_attempt_at IS DISTINCT FROM OLD.first_attempt_at OR NEW.idempotency_deadline IS DISTINCT FROM OLD.idempotency_deadline)) OR
        (OLD.provider_payment_id IS NOT NULL AND NEW.provider_payment_id IS DISTINCT FROM OLD.provider_payment_id)
    ) THEN
        RAISE EXCEPTION 'payment operation is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER payment_request_immutable BEFORE UPDATE ON billing.orders
    FOR EACH ROW EXECUTE FUNCTION billing.keep_payment_request_immutable();

-- Store only reconciliation hints, never raw provider payloads or card data.
CREATE TABLE billing.webhook_inbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id uuid NOT NULL REFERENCES billing.orders(id) ON DELETE CASCADE,
    payment_id text NOT NULL CHECK (length(payment_id) BETWEEN 16 AND 64),
    event text NOT NULL CHECK (event IN ('payment.succeeded', 'payment.canceled')),
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    result text NOT NULL DEFAULT '' CHECK (result IN ('', 'accepted', 'rejected')),
    UNIQUE(payment_id, event)
);
CREATE INDEX webhook_inbox_pending_idx ON billing.webhook_inbox(order_id, received_at) WHERE processed_at IS NULL;
