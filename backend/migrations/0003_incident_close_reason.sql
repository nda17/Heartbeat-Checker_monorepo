ALTER TABLE incidents.incidents
    ADD COLUMN close_reason text NOT NULL DEFAULT ''
    CHECK (close_reason IN ('', 'recovered', 'config_changed', 'paused', 'deleted'));

-- Before this migration only an accepted UP result could close an incident.
UPDATE incidents.incidents SET close_reason = 'recovered' WHERE closed_at IS NOT NULL;

ALTER TABLE incidents.incidents ADD CONSTRAINT incident_close_reason_matches_state
    CHECK ((closed_at IS NULL AND close_reason = '') OR
           (closed_at IS NOT NULL AND close_reason <> ''));
