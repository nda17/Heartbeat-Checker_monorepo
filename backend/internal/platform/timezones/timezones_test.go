package timezones

import (
	"testing"
	"time"
)

func TestCatalogHas438UniqueLoadableZones(t *testing.T) {
	zones := Catalog()
	if len(zones) != 438 {
		t.Fatalf("timezone catalog contains %d entries, want 438", len(zones))
	}
	seen := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		if zone.ID == "" || zone.Label == "" {
			t.Errorf("catalog contains an empty timezone ID or label: %+v", zone)
			continue
		}
		if _, duplicate := seen[zone.ID]; duplicate {
			t.Errorf("timezone catalog repeats %q", zone.ID)
			continue
		}
		seen[zone.ID] = struct{}{}
		if _, err := time.LoadLocation(zone.ID); err != nil {
			t.Errorf("timezone %q cannot be loaded: %v", zone.ID, err)
		}
	}
}

func TestFormatUsesOffsetAtEventTimeAndFallsBackForLegacySnapshots(t *testing.T) {
	tests := []struct {
		name, zone, at, want string
	}{
		{"Berlin winter", "Europe/Berlin", "2026-01-15T12:00:00Z", "15.01.2026 13:00 CET (UTC+01)"},
		{"Berlin summer", "Europe/Berlin", "2026-07-15T12:00:00Z", "15.07.2026 14:00 CEST (UTC+02)"},
		{"Kathmandu fractional offset", "Asia/Kathmandu", "2026-01-15T12:00:00Z", "15.01.2026 17:45 Asia/Kathmandu (UTC+05:45)"},
		{"legacy missing zone", "", "2026-01-15T12:00:00Z", "15.01.2026 15:00 МСК (UTC+03)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := Format(at, tt.zone); got != tt.want {
				t.Fatalf("Format(%s, %q) = %q, want %q", at, tt.zone, got, tt.want)
			}
		})
	}
}
