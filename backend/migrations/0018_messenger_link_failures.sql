-- Keep historical expired attempts unclassified: their original cause is unknown.
ALTER TABLE notifications.messenger_links
    DROP CONSTRAINT messenger_links_status_check,
    ADD CONSTRAINT messenger_links_status_check
        CHECK (status IN ('pending', 'observed', 'candidate', 'confirmed', 'expired', 'failed')),
    ADD COLUMN error_code text NOT NULL DEFAULT ''
        CHECK (error_code IN ('', 'link_expired', 'telegram_private_required',
            'telegram_bot_admin_required', 'telegram_user_admin_required',
            'telegram_target_mismatch', 'telegram_chat_unavailable',
            'verification_unavailable', 'verification_failed'));
