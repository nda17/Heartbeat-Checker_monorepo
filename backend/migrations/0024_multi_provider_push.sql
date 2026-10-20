-- Existing installations and strict legacy clients retain their RuStore route.
ALTER TABLE notifications.push_installations DROP CONSTRAINT push_installations_provider_check;
ALTER TABLE notifications.push_installations ADD CONSTRAINT push_installations_provider_check
    CHECK (provider IN ('rustore','fcm','huawei'));

-- The first claim freezes the external destination. Retries never target another
-- provider/token after a registration change or an ambiguous external response.
ALTER TABLE notifications.push_deliveries
    ADD COLUMN delivery_provider text CHECK (delivery_provider IN ('rustore','fcm','huawei')),
    ADD COLUMN encrypted_destination bytea,
    ADD CONSTRAINT push_delivery_destination_check CHECK ((delivery_provider IS NULL)=(encrypted_destination IS NULL));

-- Pre-migration attempts did not retain their external destination. They cannot
-- be safely retried using the installation's possibly rotated token. Preserve
-- readability of already attempted envelopes, but do not emit them again.
UPDATE notifications.push_deliveries SET status='failed',attempt_token=NULL,lease_until=NULL,
    completed_at=now(),error_code='route_migration'
    WHERE attempts>0 AND status IN ('pending','leased');
