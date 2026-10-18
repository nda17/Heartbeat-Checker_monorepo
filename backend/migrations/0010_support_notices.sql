-- Welcome and acknowledgement use the existing encrypted, leased support inbox.
ALTER TABLE support.inbox DROP CONSTRAINT inbox_event_kind_check;
ALTER TABLE support.inbox ADD CONSTRAINT inbox_event_kind_check
    CHECK (event_kind IN ('message','discovery','welcome','ack'));
