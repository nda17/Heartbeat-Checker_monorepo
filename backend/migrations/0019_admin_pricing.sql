CREATE TABLE billing.pricing (
    singleton smallint PRIMARY KEY CHECK (singleton = 1),
    price_per_monitor_minor integer NOT NULL CHECK (price_per_monitor_minor BETWEEN 100 AND 1000000),
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL
);
INSERT INTO billing.pricing(singleton, price_per_monitor_minor, version, updated_at)
VALUES (1, 2900, 1, now());
