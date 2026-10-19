-- The account UUID remains as a technical and financial reference. Personal
-- fields and dependent workspace data are removed by one deletion transaction.
ALTER TABLE accounts.accounts DROP CONSTRAINT accounts_status_check;
ALTER TABLE accounts.accounts ADD CONSTRAINT accounts_status_check
    CHECK (status IN ('active', 'blocked', 'deleted'));
ALTER TABLE accounts.accounts ADD COLUMN deleted_at timestamptz;
ALTER TABLE accounts.accounts ADD CONSTRAINT accounts_deleted_at_check
    CHECK ((status = 'deleted') = (deleted_at IS NOT NULL));
