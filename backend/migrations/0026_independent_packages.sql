-- Preserve every existing subscription/local/test package without deriving any
-- grant from historical orders. In particular terminal blocked orders stay so.
LOCK TABLE billing.entitlements IN ACCESS EXCLUSIVE MODE;
ALTER TABLE billing.entitlements ADD COLUMN id uuid;
UPDATE billing.entitlements SET id = account_id;
ALTER TABLE billing.entitlements ALTER COLUMN id SET NOT NULL;
ALTER TABLE billing.entitlements ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_pkey;
ALTER TABLE billing.entitlements ADD PRIMARY KEY (id);
CREATE INDEX entitlements_account_idx ON billing.entitlements(account_id, id);
CREATE UNIQUE INDEX entitlements_local_account_idx ON billing.entitlements(account_id)
    WHERE source = 'local_test';
ALTER TABLE billing.entitlements ADD COLUMN origin_order_id uuid UNIQUE REFERENCES billing.orders(id);

ALTER TABLE billing.orders
    ADD COLUMN purchase_kind text NOT NULL DEFAULT 'legacy'
        CHECK (purchase_kind IN ('legacy', 'purchase', 'renew')),
    ADD COLUMN renew_package_id uuid REFERENCES billing.entitlements(id),
    ADD COLUMN fulfilled_package_id uuid REFERENCES billing.entitlements(id),
    ADD CONSTRAINT orders_package_selection_check CHECK
        ((purchase_kind = 'renew' AND renew_package_id IS NOT NULL)
            OR (purchase_kind IN ('legacy', 'purchase') AND renew_package_id IS NULL)),
    ADD CONSTRAINT orders_fulfilled_package_check CHECK
        (fulfilled_package_id IS NULL OR (status = 'succeeded' AND fulfillment_status = 'fulfilled'));

-- Keep the original payment request guard unchanged and extend immutability to
-- package selection for both providers. A result link may only be assigned once.
CREATE FUNCTION billing.keep_order_package_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.purchase_kind IS DISTINCT FROM OLD.purchase_kind
        OR NEW.renew_package_id IS DISTINCT FROM OLD.renew_package_id
        OR (OLD.fulfilled_package_id IS NOT NULL
            AND NEW.fulfilled_package_id IS DISTINCT FROM OLD.fulfilled_package_id) THEN
        RAISE EXCEPTION 'payment package selection is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER order_package_immutable BEFORE UPDATE ON billing.orders
    FOR EACH ROW EXECUTE FUNCTION billing.keep_order_package_immutable();
