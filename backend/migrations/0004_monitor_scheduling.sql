ALTER TABLE monitors.monitors
    ADD COLUMN interval_minutes integer NOT NULL DEFAULT 1 CHECK (interval_minutes BETWEEN 1 AND 10),
    ADD COLUMN failure_threshold integer NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10);

-- Existing rows retain one minute; new rows use the five-minute default.
ALTER TABLE monitors.monitors ALTER COLUMN interval_minutes SET DEFAULT 5;
