CREATE SCHEMA support;
CREATE TABLE support.inbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL CHECK (provider IN ('telegram','max')),
    event_key text NOT NULL,
    event_kind text NOT NULL DEFAULT 'message' CHECK (event_kind IN ('message','discovery')),
    sender_key bytea NOT NULL,
    payload_hash bytea NOT NULL,
    payload bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed','recorded')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    first_attempt_at timestamptz,
    lease_token text,
    lease_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE(provider,event_key),
    CHECK ((status='sending') = (lease_token IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK ((event_kind='discovery') = (status='recorded'))
);
CREATE INDEX inbox_claim_idx ON support.inbox(next_attempt_at,id) WHERE status IN ('pending','sending');
CREATE INDEX inbox_sender_admission_idx ON support.inbox(provider,sender_key,created_at);
CREATE INDEX inbox_discovery_retention_idx ON support.inbox(created_at) WHERE event_kind='discovery';
CREATE TABLE support.message_routes (
    provider text NOT NULL CHECK (provider IN ('telegram','max')),
    operator_chat_id bigint NOT NULL,
    operator_message_id text NOT NULL,
    client_chat_id bigint NOT NULL,
    client_user_id bigint NOT NULL,
    client_message_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(provider,operator_chat_id,operator_message_id)
);
CREATE INDEX message_routes_retention_idx ON support.message_routes(created_at);
CREATE TABLE support.target_rates (
    provider text NOT NULL CHECK (provider IN ('telegram','max')),
    target_key bytea NOT NULL,
    next_send_at timestamptz NOT NULL,
    PRIMARY KEY(provider,target_key)
);
