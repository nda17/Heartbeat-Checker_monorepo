-- One-off, create-only owner import. The only generated input is one hex-encoded
-- JSON row supplied over stdin by deploy.sh; no payload value appears in SQL logs.
BEGIN;
SET LOCAL statement_timeout = '15s';
CREATE TEMP TABLE owner_import_input (payload_hex text, payload_sha256 text, source_sha text) ON COMMIT DROP;
CREATE TEMP TABLE owner_import_result (outcome text, original_source_sha text) ON COMMIT DROP;
COPY owner_import_input FROM STDIN;
__OWNER_PAYLOAD_ROW__
\.

DO $owner_import$
DECLARE
    input owner_import_input%ROWTYPE;
    payload jsonb;
    prior admin.audit_events%ROWTYPE;
    owner_id uuid := '51ec7d19-80c4-4fb6-8edb-79dc7abe3120';
BEGIN
    IF (SELECT count(*) FROM owner_import_input) <> 1 THEN
        RAISE EXCEPTION 'invalid owner import input';
    END IF;
    SELECT * INTO STRICT input FROM owner_import_input;
    IF input.payload_sha256 !~ '^[0-9a-f]{64}$' OR input.source_sha !~ '^[0-9a-f]{40}$' THEN
        RAISE EXCEPTION 'invalid owner import provenance';
    END IF;
    payload := convert_from(decode(input.payload_hex, 'hex'), 'UTF8')::jsonb;
    IF (SELECT array_agg(fields.key ORDER BY fields.key)
        FROM jsonb_object_keys(payload) AS fields(key)) <>
       ARRAY['accountId','email','name','passwordHash','quota','validUntil']
       OR payload->>'accountId' <> owner_id::text
       OR payload->>'email' <> 'khv1702@gmail.com'
       OR payload->>'quota' <> '1000'
       OR payload->>'validUntil' <> '2027-10-18T00:00:00Z'
       OR payload->>'passwordHash' !~ '^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$'
       OR length(payload->>'name') NOT BETWEEN 2 AND 100 THEN
        RAISE EXCEPTION 'invalid owner import payload';
    END IF;

    SELECT * INTO prior FROM admin.audit_events
     WHERE operation_id = 'owner-production-2026-10-19-v1' FOR UPDATE;
    IF FOUND THEN
        IF prior.action <> 'owner.provisioned' OR prior.target_account_id <> owner_id
           OR prior.target_id <> owner_id
           OR prior.metadata->>'payloadSha256' <> input.payload_sha256
           OR NOT EXISTS (SELECT 1 FROM accounts.accounts a
                          JOIN auth.credentials c ON c.account_id = a.id
                          WHERE a.id = owner_id AND c.email = 'khv1702@gmail.com') THEN
            RAISE EXCEPTION 'owner import marker conflict';
        END IF;
        INSERT INTO owner_import_result VALUES ('noop', prior.metadata->>'sourceSha');
        RETURN;
    END IF;

    IF EXISTS (SELECT 1 FROM accounts.accounts WHERE id = owner_id)
       OR EXISTS (SELECT 1 FROM auth.credentials WHERE email = 'khv1702@gmail.com') THEN
        RAISE EXCEPTION 'owner account conflict';
    END IF;

    INSERT INTO accounts.accounts (id, name, role, status)
    VALUES (owner_id, payload->>'name', 'admin', 'active');
    INSERT INTO auth.credentials (account_id, email, password_hash)
    VALUES (owner_id, 'khv1702@gmail.com', payload->>'passwordHash');
    INSERT INTO billing.admin_grants (account_id, monitor_limit, valid_until, reason)
    VALUES (owner_id, 1000, '2027-10-18T00:00:00Z',
            'Owner account transfer authorized by owner');
    INSERT INTO admin.audit_events
        (operation_id, actor_account_id, actor_session_id, target_account_id, target_id,
         action, reason, before_state, after_state, metadata)
    VALUES
        ('owner-production-2026-10-19-v1', NULL, NULL, owner_id, owner_id,
         'owner.provisioned', 'Owner account transfer authorized by owner', '{}'::jsonb,
         '{"role":"admin","status":"active","quota":1000,"validUntil":"2027-10-18T00:00:00Z","source":"admin_grant"}'::jsonb,
         jsonb_build_object('payloadSha256', input.payload_sha256,
                            'sourceSha', input.source_sha, 'channel', 'github_actions'));
    INSERT INTO owner_import_result VALUES ('created', input.source_sha);
END
$owner_import$;

SELECT outcome || '|' || original_source_sha FROM owner_import_result;
COMMIT;
