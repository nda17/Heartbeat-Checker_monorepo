package notifications

import (
	"strings"
	"testing"
	"time"
)

func emailTemplateSnapshot(kind, checkType string) messengerSnapshot {
	opened := time.Date(2026, time.January, 2, 22, 15, 0, 0, time.UTC)
	confirmed := opened.Add(3 * time.Minute)
	closed := opened.Add(90 * time.Minute)
	return messengerSnapshot{
		CheckType: checkType, Host: "status.example.test", Port: 443,
		MonitorID: "monitor id/1", IncidentID: "incident?1", Number: 42,
		Kind: kind, Name: "Payments API", URL: "https://status.example.test/health?x=1&y=2",
		Outcome: "DOWN", HTTPStatus: 503, ErrorCode: "unexpected_status",
		LatencyMS: 125, CheckedAt: closed, OpenedAt: opened,
		ConfirmedAt: confirmed, ClosedAt: &closed,
	}
}

func TestEmailMessageHasDistinctIncidentSubjectsAndStates(t *testing.T) {
	tests := []struct {
		kind, subject, title string
	}{
		{"down", "Heartbeat Checker · Сбой подтверждён · Payments API", "Сбой подтверждён"},
		{"up", "Heartbeat Checker · Сервис восстановился · Payments API", "Сервис восстановился"},
		{"reminder", "Heartbeat Checker · Сбой продолжается · Payments API", "Сбой продолжается"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			subject, body, err := emailMessage(emailTemplateSnapshot(tt.kind, "http"))
			if err != nil {
				t.Fatal(err)
			}
			if subject != tt.subject {
				t.Fatalf("subject=%q, want %q", subject, tt.subject)
			}
			if !strings.Contains(body.HTML, "<h1") || !strings.Contains(body.HTML, ">"+tt.title+"</h1>") {
				t.Fatalf("HTML does not contain state title %q", tt.title)
			}
			if !strings.Contains(body.HTML, "Открыть инцидент") || !strings.Contains(body.HTML, "Открыть сайт") {
				t.Fatal("expected incident and valid HTTP(S) site calls to action")
			}
			causeVisible := strings.Contains(body.HTML, "получен неожиданный HTTP-статус")
			if tt.kind == "up" && causeVisible {
				t.Fatal("recovery email should not retain the outage cause")
			}
			if tt.kind != "up" && !causeVisible {
				t.Fatal("down and reminder emails should include the outage cause")
			}
		})
	}
}

func TestEmailMessageIncludesHTTPAndNetworkCheckFields(t *testing.T) {
	tests := []struct {
		checkType string
		want      []string
	}{
		{"http", []string{"Тип проверки", "HTTP", "Сайт", "https://status.example.test/health?x=1&amp;y=2", ">503</td>", "125 мс", "получен неожиданный HTTP-статус"}},
		{"tcp", []string{"Тип проверки", "TCP", "status.example.test:443", "TCP-соединение отклонено", "6 мс"}},
		{"icmp", []string{"Тип проверки", "ICMP Ping", "status.example.test", "не выполнена", "6 мс", "19 мс"}},
	}
	for _, tt := range tests {
		t.Run(tt.checkType, func(t *testing.T) {
			snapshot := emailTemplateSnapshot("down", tt.checkType)
			if tt.checkType == "tcp" {
				snapshot.ErrorCode = "tcp_connection_refused"
			}
			if tt.checkType == "icmp" {
				snapshot.ErrorCode = "icmp_failed"
				rtt := int64(19)
				snapshot.RoundTripMS = &rtt
			}
			if tt.checkType != "http" {
				snapshot.URL = ""
				snapshot.HTTPStatus = 0
				snapshot.LatencyMS = 6
			}
			_, body, err := emailMessage(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range tt.want {
				if !strings.Contains(body.HTML, value) {
					t.Errorf("HTML missing %q", value)
				}
			}
			if tt.checkType != "http" {
				for _, invalidField := range []string{"Открыть сайт", ">503</td>", "125 мс", "Время HTTP-проверки"} {
					if strings.Contains(body.HTML, invalidField) {
						t.Errorf("%s email contains HTTP-only field %q", tt.checkType, invalidField)
					}
				}
			}
		})
	}
}

func TestEmailMessageOmitsUnavailableHTTPFieldsForNetworkChecks(t *testing.T) {
	for _, checkType := range []string{"tcp", "icmp"} {
		t.Run(checkType, func(t *testing.T) {
			snapshot := emailTemplateSnapshot("down", checkType)
			snapshot.URL = ""
			snapshot.HTTPStatus = 0
			snapshot.LatencyMS = -1
			snapshot.RoundTripMS = nil
			_, body, err := emailMessage(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, absent := range []string{"Открыть сайт", "Сайт</td>", ">503</td>", "мс</td>"} {
				if strings.Contains(body.HTML, absent) {
					t.Errorf("%s email contains unavailable field %q", checkType, absent)
				}
			}
		})
	}
}

func TestEmailMessageEscapesUntrustedFieldsAndKeepsOnlySafeSiteCTA(t *testing.T) {
	snapshot := emailTemplateSnapshot("down", "http")
	snapshot.Name = `<img src=x onerror=alert(1)>`
	snapshot.URL = `javascript:alert(1)`
	subject, body, err := emailMessage(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(subject, "\n") || strings.Contains(subject, "\r") {
		t.Fatalf("subject contains a line break: %q", subject)
	}
	if strings.Contains(body.HTML, snapshot.Name) || !strings.Contains(body.HTML, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatal("unsafe monitor name was not HTML-escaped")
	}
	if strings.Contains(body.HTML, `href="javascript:`) || strings.Contains(body.HTML, "Открыть сайт") {
		t.Fatal("unsafe URL was exposed as a site CTA")
	}
	if !strings.Contains(body.HTML, `href="https://heartbeat-checker.ru/app?monitor=`) || !strings.Contains(body.HTML, `incident%3F1`) {
		t.Fatal("incident CTA is missing or unsafe")
	}
}

func TestIncidentEmailSubjectSanitizesNameAndTimeUsesMoscowOffsetAcrossDateBoundary(t *testing.T) {
	name := "  API\r\n  <critical>\x00\tmonitor  "
	if got, want := incidentEmailSubjectName(name), "API <critical> monitor"; got != want {
		t.Fatalf("sanitized name=%q, want %q", got, want)
	}
	if got := incidentEmailSubjectName(strings.Repeat("Я", 101)); len([]rune(got)) != 100 {
		t.Fatalf("subject name has %d runes, want 100", len([]rune(got)))
	}
	if got := incidentEmailSubjectName("\n\t"); got != "Монитор" {
		t.Fatalf("empty subject name fallback=%q", got)
	}

	tests := []struct {
		at, want string
	}{
		{"2026-01-02T23:15:00Z", "03.01.2026 02:15 МСК (UTC+03)"},
		{"2026-01-02T11:15:00Z", "02.01.2026 14:15 МСК (UTC+03)"},
		{"2026-12-31T21:30:00Z", "01.01.2027 00:30 МСК (UTC+03)"},
	}
	for _, tt := range tests {
		t.Run(tt.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := messengerTime(at); got != tt.want {
				t.Fatalf("messengerTime=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmailSnapshotFormatsSelectedZoneKeepsDurationStableAndSupportsLegacyZone(t *testing.T) {
	snapshot := emailTemplateSnapshot("up", "http")
	snapshot.OpenedAt = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	closed := snapshot.OpenedAt.Add(90 * time.Minute)
	snapshot.CheckedAt = closed
	snapshot.ClosedAt = &closed
	snapshot.TimeZone = "Europe/Berlin"
	snapshot.ProviderNames = []string{"Cloudflare", "Timeweb"}
	_, berlin, err := emailMessage(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(berlin.HTML, "15.01.2026 13:00 CET (UTC&#43;01)") || !strings.Contains(berlin.HTML, "15.01.2026 14:30 CET (UTC&#43;01)") || !strings.Contains(berlin.HTML, "1 ч 30 мин") {
		t.Fatalf("Berlin email is missing localized times or elapsed duration: %s", berlin.HTML)
	}
	if !strings.Contains(berlin.HTML, "Cloudflare; Timeweb") {
		t.Fatal("email snapshot did not preserve provider names")
	}
	snapshot.TimeZone = "Asia/Kathmandu"
	_, kathmandu, err := emailMessage(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kathmandu.HTML, "15.01.2026 17:45 Asia/Kathmandu (UTC&#43;05:45)") || !strings.Contains(kathmandu.HTML, "1 ч 30 мин") {
		t.Fatalf("Kathmandu email changed event offset or elapsed duration: %s", kathmandu.HTML)
	}

	snapshot.TimeZone = ""
	_, legacy, err := emailMessage(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(legacy.HTML, "15.01.2026 15:00 МСК (UTC&#43;03)") || !strings.Contains(legacy.HTML, "1 ч 30 мин") {
		t.Fatalf("legacy email snapshot did not use Moscow and stable duration: %s", legacy.HTML)
	}
}
