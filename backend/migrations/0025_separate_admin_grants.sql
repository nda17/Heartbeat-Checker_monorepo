-- Move only administrative grants. Paid, local and mock entitlements, including
-- their versions and payment history, remain in the existing table.
CREATE TABLE billing.admin_grants (
    account_id uuid PRIMARY KEY REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    monitor_limit integer NOT NULL CHECK (monitor_limit BETWEEN 1 AND 1000),
    valid_until timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    granted_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500)
);

-- Hold writers while moving the exact old rows, including expired grants.
LOCK TABLE billing.entitlements IN ACCESS EXCLUSIVE MODE;
INSERT INTO billing.admin_grants (account_id, monitor_limit, valid_until, version, granted_at, reason)
SELECT account_id, monitor_limit, valid_until, version, granted_at, reason
FROM billing.entitlements WHERE source = 'admin_grant';
DELETE FROM billing.entitlements WHERE source = 'admin_grant';

ALTER TABLE billing.entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE billing.entitlements ADD CONSTRAINT entitlements_source_check
    CHECK (source IN ('local_test', 'mock_test', 'yookassa_test', 'yookassa'));
