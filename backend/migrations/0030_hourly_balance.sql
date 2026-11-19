-- Preserve the original order parameters and historical package UUIDs, while
-- retiring all operational package and grant rows.
ALTER TABLE billing.orders
    DROP CONSTRAINT orders_renew_package_id_fkey,
    DROP CONSTRAINT orders_fulfilled_package_id_fkey,
    DROP CONSTRAINT orders_purchase_kind_check,
    DROP CONSTRAINT orders_package_selection_check,
    DROP CONSTRAINT orders_monitor_count_check,
    DROP CONSTRAINT orders_amount_minor_check,
    DROP CONSTRAINT orders_fulfilled_check,
    DROP CONSTRAINT orders_fulfillment_reason_check,
    ADD CONSTRAINT orders_purchase_kind_check CHECK (purchase_kind IN ('legacy', 'purchase', 'renew', 'topup')),
    ADD CONSTRAINT orders_package_selection_check CHECK ((purchase_kind = 'renew') = (renew_package_id IS NOT NULL)),
    ADD CONSTRAINT orders_monitor_count_check CHECK (
        (purchase_kind = 'topup' AND provider = 'yookassa' AND monitor_count = 0 AND fulfilled_package_id IS NULL AND active_until IS NULL)
        OR (purchase_kind <> 'topup' AND monitor_count BETWEEN 1 AND 1000)
    ),
    ADD CONSTRAINT orders_amount_minor_check CHECK (
        amount_minor > 0 AND CASE WHEN purchase_kind = 'topup' THEN amount_minor BETWEEN 100 AND 35000000
        ELSE amount_minor % monitor_count = 0 END
    ),
    ADD CONSTRAINT orders_fulfilled_check CHECK (
        fulfillment_status <> 'fulfilled' OR (status = 'succeeded' AND fulfilled_at IS NOT NULL
        AND (purchase_kind = 'topup' OR active_until IS NOT NULL))
    ),
    ADD CONSTRAINT orders_fulfillment_reason_check CHECK (
        fulfillment_reason IN ('', 'admin_grant', 'account_deleted', 'account_inactive', 'capacity_changed', 'paid_entitlement', 'legacy_checkout_retired')
    );

DELETE FROM billing.entitlements;
DELETE FROM billing.admin_grants;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_retired CHECK (false);
ALTER TABLE billing.admin_grants ADD CONSTRAINT admin_grants_retired CHECK (false);

-- A historical checkout cannot be replayed into the wallet. Unknown payment
-- outcomes remain visible for financial investigation with their original IDs.
UPDATE billing.sdk_payment_requests SET encrypted_request = NULL
WHERE encrypted_request IS NOT NULL;
UPDATE billing.orders
SET reconciliation_status = 'manual_review', reconcile_token = NULL, reconcile_lease_until = NULL,
    fulfillment_status = 'blocked', fulfillment_reason = 'legacy_checkout_retired'
WHERE status = 'pending';

DROP INDEX billing.orders_pending_idx;
CREATE UNIQUE INDEX orders_pending_topup_idx ON billing.orders(account_id)
    WHERE status = 'pending' AND purchase_kind = 'topup';

CREATE FUNCTION billing.require_topup_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.purchase_kind <> 'topup' AND (TG_OP = 'INSERT' OR NEW.reconciliation_status IN ('awaiting_token', 'pending')) THEN
        RAISE EXCEPTION 'package checkout is retired';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER orders_topup_only BEFORE INSERT OR UPDATE OF reconciliation_status ON billing.orders
    FOR EACH ROW EXECUTE FUNCTION billing.require_topup_order();

CREATE TABLE billing.wallet_tariffs (
    version bigint PRIMARY KEY CHECK (version > 0),
    effective_at timestamptz NOT NULL UNIQUE,
    price_per_monitor_minor integer NOT NULL CHECK (price_per_monitor_minor BETWEEN 100 AND 1000000)
);

-- Adopt the wallet's initial tariff for untouched installations. Keep prices
-- explicitly set by an administrator together with their original versions.
UPDATE billing.pricing
SET price_per_monitor_minor = 2600
WHERE singleton = 1 AND version = 1 AND price_per_monitor_minor = 2900;

INSERT INTO billing.wallet_tariffs(version, effective_at, price_per_monitor_minor)
SELECT version, updated_at, price_per_monitor_minor FROM billing.pricing WHERE singleton = 1;

-- Each integer unit is one 32,622,912,000,000,000th of a kopeck. This
-- represents the exact per-microsecond price in 28–31 day calendar months.
CREATE TABLE billing.wallets (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    available_units numeric(38,0) NOT NULL DEFAULT 0 CHECK (available_units >= 0),
    settled_at timestamptz NOT NULL,
    billed_monitors integer NOT NULL DEFAULT 0 CHECK (billed_monitors >= 0),
    funded_until timestamptz,
    next_settlement_at timestamptz,
    activity_epoch bigint NOT NULL DEFAULT 0 CHECK (activity_epoch >= 0)
);
CREATE INDEX wallets_due_idx ON billing.wallets(next_settlement_at) WHERE next_settlement_at IS NOT NULL;

CREATE TABLE billing.wallet_usage (
    account_id uuid PRIMARY KEY REFERENCES billing.wallets(account_id) ON DELETE CASCADE,
    hour_start timestamptz NOT NULL,
    from_at timestamptz NOT NULL,
    until_at timestamptz NOT NULL,
    units numeric(38,0) NOT NULL CHECK (units > 0),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    CHECK (hour_start <= from_at AND from_at <= until_at AND until_at <= hour_start + interval '1 hour')
);

CREATE TABLE billing.wallet_ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('top_up', 'admin_credit', 'usage', 'local_credit')),
    units numeric(38,0) NOT NULL,
    amount_minor bigint NOT NULL,
    created_at timestamptz NOT NULL,
    order_id uuid UNIQUE REFERENCES billing.orders(id) ON DELETE CASCADE,
    request_id uuid,
    request_hash bytea CHECK (request_hash IS NULL OR octet_length(request_hash) = 32),
    hour_start timestamptz,
    period_start timestamptz,
    period_end timestamptz,
    UNIQUE (account_id, request_id),
    UNIQUE (account_id, hour_start),
    CHECK ((kind = 'top_up') = (order_id IS NOT NULL)),
    CHECK ((kind = 'admin_credit') = (request_id IS NOT NULL AND request_hash IS NOT NULL)),
    CHECK (kind <> 'usage' OR (units < 0 AND amount_minor <= 0 AND period_start IS NOT NULL
           AND hour_start IS NOT NULL AND period_end IS NOT NULL AND hour_start <= period_start
           AND period_start <= period_end AND period_end <= hour_start + interval '1 hour')),
    CHECK (kind = 'usage' OR (units > 0 AND amount_minor > 0 AND hour_start IS NULL
           AND period_start IS NULL AND period_end IS NULL))
);
CREATE INDEX wallet_ledger_history_idx ON billing.wallet_ledger(account_id, created_at DESC, id DESC);

-- Any in-flight check belonged to the retired entitlement model.
UPDATE monitors.monitors SET run_id = NULL, attempt_token = NULL, lease_until = NULL WHERE lease_until IS NOT NULL;
