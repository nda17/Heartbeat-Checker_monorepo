// Package timezones keeps monitor input and display on one pinned catalog.
package timezones

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

const Default = "Europe/Moscow"

// Russian exemplar cities: Unicode CLDR 48.0.0, Unicode License v3.
// https://github.com/unicode-org/cldr-json/tree/48.0.0/cldr-json/cldr-dates-full/main/ru
// Modern IANA spellings are included alongside CLDR compatibility aliases.
//
//go:embed catalog.json
var files embed.FS

type Zone struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var catalog []Zone
var allowed map[string]bool

func init() {
	data, err := files.ReadFile("catalog.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	allowed = make(map[string]bool, len(catalog))
	for _, zone := range catalog {
		allowed[zone.ID] = true
	}
}

func Catalog() []Zone        { return append([]Zone(nil), catalog...) }
func Valid(zone string) bool { return allowed[zone] }

// Format uses the offset at the instant of the event, including DST transitions.
// Missing zones occur in immutable delivery snapshots created before timezones.
func Format(at time.Time, zone string) string {
	if !Valid(zone) {
		zone = Default
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.FixedZone("MSK", 3*3600)
	}
	local := at.In(location)
	abbreviation, offset := local.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	utc := fmt.Sprintf("UTC%s%02d", sign, offset/3600)
	if offset%3600 != 0 {
		utc += fmt.Sprintf(":%02d", offset%3600/60)
	}
	if zone == Default {
		abbreviation = "МСК"
	}
	if strings.HasPrefix(abbreviation, "+") || strings.HasPrefix(abbreviation, "-") {
		abbreviation = zone
	}
	return local.Format("02.01.2006 15:04") + " " + abbreviation + " (" + utc + ")"
}
