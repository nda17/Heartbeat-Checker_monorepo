ALTER TABLE billing.orders ADD COLUMN checkout_kind text NOT NULL DEFAULT 'redirect'
    CHECK (checkout_kind IN ('redirect', 'android_sdk'));
ALTER TABLE billing.orders DROP CONSTRAINT orders_reconciliation_status_check;
ALTER TABLE billing.orders ADD CONSTRAINT orders_reconciliation_status_check
    CHECK (reconciliation_status IN ('pending', 'awaiting_token', 'complete', 'manual_review', 'stopped'));
ALTER TABLE billing.orders ADD CONSTRAINT orders_sdk_checkout_check
    CHECK (checkout_kind <> 'android_sdk' OR
           (provider = 'yookassa' AND return_target = 'android' AND expires_at IS NULL));

CREATE FUNCTION billing.keep_checkout_kind_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.checkout_kind IS DISTINCT FROM OLD.checkout_kind THEN
        RAISE EXCEPTION 'checkout kind is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER order_checkout_kind_immutable BEFORE UPDATE ON billing.orders
    FOR EACH ROW EXECUTE FUNCTION billing.keep_checkout_kind_immutable();

CREATE TABLE billing.sdk_payment_requests (
    order_id uuid PRIMARY KEY REFERENCES billing.orders(id) ON DELETE CASCADE,
    draft_expires_at timestamptz NOT NULL,
    request_expires_at timestamptz NOT NULL,
    token_hash bytea UNIQUE CHECK (token_hash IS NULL OR octet_length(token_hash) = 32),
    request_hash bytea CHECK (request_hash IS NULL OR octet_length(request_hash) = 32),
    encrypted_request bytea CHECK (encrypted_request IS NULL OR octet_length(encrypted_request) BETWEEN 29 AND 16384),
    payment_method_type text CHECK (payment_method_type IS NULL OR payment_method_type = 'BANK_CARD'),
    attached_at timestamptz,
    CHECK (request_expires_at = draft_expires_at + interval '40 minutes'),
    CHECK ((token_hash IS NULL AND request_hash IS NULL AND encrypted_request IS NULL AND payment_method_type IS NULL AND attached_at IS NULL) OR
           (token_hash IS NOT NULL AND request_hash IS NOT NULL AND payment_method_type IS NOT NULL AND attached_at IS NOT NULL))
);
CREATE FUNCTION billing.keep_sdk_request_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.order_id IS DISTINCT FROM OLD.order_id OR
       NEW.draft_expires_at IS DISTINCT FROM OLD.draft_expires_at OR
       NEW.request_expires_at IS DISTINCT FROM OLD.request_expires_at OR
       (OLD.token_hash IS NOT NULL AND (NEW.token_hash IS DISTINCT FROM OLD.token_hash OR
        NEW.request_hash IS DISTINCT FROM OLD.request_hash OR
        NEW.payment_method_type IS DISTINCT FROM OLD.payment_method_type OR
        NEW.attached_at IS DISTINCT FROM OLD.attached_at)) OR
       (OLD.encrypted_request IS NULL AND OLD.token_hash IS NOT NULL AND NEW.encrypted_request IS NOT NULL) OR
       (OLD.encrypted_request IS NOT NULL AND NEW.encrypted_request IS NOT NULL AND
        NEW.encrypted_request IS DISTINCT FROM OLD.encrypted_request) THEN
        RAISE EXCEPTION 'SDK payment request is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER sdk_request_immutable BEFORE UPDATE ON billing.sdk_payment_requests
    FOR EACH ROW EXECUTE FUNCTION billing.keep_sdk_request_immutable();
