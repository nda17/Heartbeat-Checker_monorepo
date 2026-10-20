ALTER TABLE billing.orders DROP CONSTRAINT orders_provider_mode_check;
ALTER TABLE billing.orders ADD CONSTRAINT orders_provider_mode_check
    CHECK (provider_mode IN ('test', 'production'));

ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_source_check
    CHECK (source IN ('local_test', 'mock_test', 'admin_grant', 'yookassa_test', 'yookassa'));

ALTER TABLE billing.orders DROP CONSTRAINT orders_fulfillment_reason_check;
ALTER TABLE billing.orders ADD CONSTRAINT orders_fulfillment_reason_check
    CHECK (fulfillment_reason IN ('', 'admin_grant', 'account_deleted', 'account_inactive', 'capacity_changed', 'paid_entitlement'));
