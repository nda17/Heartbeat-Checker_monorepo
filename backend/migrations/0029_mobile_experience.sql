-- A test is an explicit request for one installation, independent of monitor
-- quota and incident sequences. Existing incident delivery shape stays intact.
CREATE TABLE notifications.push_test_deliveries (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL,
    account_id uuid NOT NULL REFERENCES accounts.accounts(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES auth.sessions(id) ON DELETE CASCADE,
    installation_id uuid NOT NULL REFERENCES notifications.push_installations(id) ON DELETE CASCADE,
    binding_generation bigint NOT NULL CHECK (binding_generation>0),
    request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint)=32),
    encrypted_snapshot bytea NOT NULL,
    receipt_token_hash bytea NOT NULL CHECK (octet_length(receipt_token_hash)=32),
    registration_version bigint NOT NULL CHECK (registration_version>0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','leased','accepted','failed','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    attempt_token uuid,
    deadline_at timestamptz NOT NULL,
    delivery_provider text CHECK (delivery_provider IN ('rustore','fcm','huawei')),
    encrypted_destination bytea,
    error_code text NOT NULL DEFAULT '' CHECK (length(error_code)<=64),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    received_at timestamptz,
    display_submitted_at timestamptz,
    display_failed_at timestamptz,
    UNIQUE(account_id,request_id),
    CHECK ((delivery_provider IS NULL)=(encrypted_destination IS NULL)),
    CHECK (display_submitted_at IS NULL OR received_at IS NOT NULL),
    CHECK (display_failed_at IS NULL OR received_at IS NOT NULL)
);
CREATE INDEX push_tests_ready_idx ON notifications.push_test_deliveries(next_attempt_at,id) WHERE status IN ('pending','leased');
CREATE INDEX push_tests_installation_idx ON notifications.push_test_deliveries(installation_id,created_at DESC);
CREATE INDEX push_tests_account_idx ON notifications.push_test_deliveries(account_id,created_at DESC);
CREATE INDEX push_tests_retention_idx ON notifications.push_test_deliveries(created_at,id);
CREATE INDEX incidents_owner_feed_idx ON incidents.incidents(account_id,opened_at DESC,id DESC);

-- Cancel only the pending link attempt; destinations and incident routes retain
-- their existing lifecycle. Preserve every previously supported failure code.
ALTER TABLE notifications.messenger_links
    DROP CONSTRAINT messenger_links_error_code_check,
    ADD CONSTRAINT messenger_links_error_code_check
        CHECK (error_code IN ('', 'link_expired', 'telegram_private_required',
            'telegram_bot_admin_required', 'telegram_user_admin_required',
            'telegram_target_mismatch', 'telegram_chat_unavailable',
            'verification_unavailable', 'verification_failed', 'link_cancelled'));
