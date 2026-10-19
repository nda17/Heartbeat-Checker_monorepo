-- Old rows and historical checks retain their HTTP meaning.
ALTER TABLE monitors.monitors
    ADD COLUMN check_type text NOT NULL DEFAULT 'http' CHECK (check_type IN ('http', 'tcp', 'icmp')),
    ADD COLUMN host text,
    ADD COLUMN port integer,
    ADD COLUMN last_round_trip_ms bigint,
    ALTER COLUMN url DROP NOT NULL,
    ALTER COLUMN method DROP NOT NULL,
    ALTER COLUMN expected_status DROP NOT NULL,
    ALTER COLUMN body_contains DROP NOT NULL,
    ADD CONSTRAINT monitor_check_config CHECK (
        (check_type = 'http' AND url IS NOT NULL AND method IS NOT NULL AND expected_status IS NOT NULL AND body_contains IS NOT NULL AND host IS NULL AND port IS NULL)
        OR (check_type = 'tcp' AND url IS NULL AND method IS NULL AND expected_status IS NULL AND body_contains IS NULL AND host IS NOT NULL AND length(host) BETWEEN 1 AND 253 AND port IS NOT NULL AND port BETWEEN 1 AND 65535)
        OR (check_type = 'icmp' AND url IS NULL AND method IS NULL AND expected_status IS NULL AND body_contains IS NULL AND host IS NOT NULL AND length(host) BETWEEN 1 AND 253 AND port IS NULL)
    ),
    ADD CONSTRAINT monitor_round_trip CHECK (last_round_trip_ms IS NULL OR (check_type = 'icmp' AND outcome = 'UP' AND last_latency_ms IS NOT NULL AND last_round_trip_ms BETWEEN 0 AND last_latency_ms)),
    ADD CONSTRAINT monitor_protocol_metrics CHECK (check_type = 'http' OR last_http_status IS NULL);

ALTER TABLE monitors.results
    ADD COLUMN check_type text NOT NULL DEFAULT 'http' CHECK (check_type IN ('http', 'tcp', 'icmp')),
    ADD COLUMN round_trip_ms bigint,
    ADD CONSTRAINT result_protocol_metrics CHECK ((check_type = 'http' OR http_status IS NULL) AND (round_trip_ms IS NULL OR (check_type = 'icmp' AND outcome = 'UP' AND round_trip_ms BETWEEN 0 AND latency_ms)));
