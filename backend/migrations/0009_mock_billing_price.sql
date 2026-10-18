-- Keep order amounts internally consistent while the tariff comes from Go.
-- Existing orders retain their original amount when the tariff changes.
ALTER TABLE billing.orders DROP CONSTRAINT orders_check;
ALTER TABLE billing.orders ADD CONSTRAINT orders_amount_minor_check
    CHECK (amount_minor > 0 AND amount_minor % monitor_count = 0);
