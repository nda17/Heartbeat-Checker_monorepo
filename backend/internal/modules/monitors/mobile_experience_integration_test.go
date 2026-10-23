package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

func TestStatisticsPeriodsKeepUnknownSeparateAndWeightLatencySamples(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Mobile statistics owner")
	other := f.newActor(t, "Foreign mobile statistics owner")
	f.grant(t, owner, 3)
	f.grant(t, other, 1)
	monitor, status := f.create(t, owner, "Statistics monitor", "statistics.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create monitor status=%d", status)
	}
	empty, status := f.create(t, owner, "Empty statistics", "empty-statistics.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create empty monitor status=%d", status)
	}
	unknown, status := f.create(t, owner, "Unknown-only statistics", "unknown-statistics.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create unknown-only monitor status=%d", status)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.daily_statistics
	 (monitor_id,account_id,day,up_count,down_count,unknown_count,latency_sum_ms,latency_count) VALUES
	 ($1,$2,(now() AT TIME ZONE 'UTC')::date-1,2,1,8,50,2),
	 ($1,$2,(now() AT TIME ZONE 'UTC')::date,1,0,0,10,1),
	 ($1,$2,(now() AT TIME ZONE 'UTC')::date-100,100,0,0,999,1)`, monitor.ID, owner.account.ID); err != nil {
		t.Fatalf("seed isolated daily aggregate rows: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO monitors.daily_statistics
	 (monitor_id,account_id,day,unknown_count) VALUES ($1,$2,(now() AT TIME ZONE 'UTC')::date-1,4)`, unknown.ID, owner.account.ID); err != nil {
		t.Fatalf("seed UNKNOWN-only aggregate: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE billing.entitlements SET valid_until=now()-interval '1 second' WHERE account_id=$1`, owner.account.ID); err != nil {
		t.Fatalf("expire owner quota for read-only statistics case: %v", err)
	}

	for _, days := range []int{7, 30, 90} {
		response := f.request(t, owner, http.MethodGet, fmt.Sprintf("/api/v1/monitors/%s/statistics?days=%d", monitor.ID, days), nil, "", "")
		var body struct {
			Statistics monitorcontracts.Statistics `json:"statistics"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("statistics days=%d status=%d body=%s", days, response.Code, response.Body.String())
		}
		stats := body.Statistics
		if stats.PeriodDays != days || len(stats.Days) != days || stats.TimeZone != "UTC" || stats.RetentionDays != 90 || stats.Methodology != "accepted_checks_v1" {
			t.Errorf("statistics contract days=%d: %+v", days, stats)
		}
		if stats.Totals.Up != 3 || stats.Totals.Down != 1 || stats.Totals.Unknown != 8 || stats.Totals.Total != 12 {
			t.Errorf("statistics totals days=%d: %+v", days, stats.Totals)
		}
		if stats.Totals.SuccessfulCheckPercent == nil || *stats.Totals.SuccessfulCheckPercent != 75 {
			t.Errorf("success percentage must use UP/(UP+DOWN), excluding UNKNOWN: %+v", stats.Totals)
		}
		if stats.Totals.AverageLatencyMS == nil || *stats.Totals.AverageLatencyMS != 20 || stats.Totals.LatencySampleCount != 3 {
			t.Errorf("latency must use aggregate sum/count, not average of daily averages: %+v", stats.Totals)
		}
		if stats.Days[days-1].Date != time.Now().UTC().Format("2006-01-02") || !stats.Days[days-1].Partial {
			t.Errorf("current UTC day should be last and partial: %+v", stats.Days[days-1])
		}
		if stats.Coverage.DaysWithData != 2 || stats.Coverage.FirstRecordedDay == nil {
			t.Errorf("statistics coverage should include only the two in-period UTC days: %+v", stats.Coverage)
		}
	}
	emptyResponse := f.request(t, owner, http.MethodGet, "/api/v1/monitors/"+empty.ID+"/statistics?days=7", nil, "", "")
	var emptyBody struct {
		Statistics monitorcontracts.Statistics `json:"statistics"`
	}
	if emptyResponse.Code != http.StatusOK || json.Unmarshal(emptyResponse.Body.Bytes(), &emptyBody) != nil {
		t.Fatalf("empty statistics status=%d body=%s", emptyResponse.Code, emptyResponse.Body.String())
	}
	if emptyBody.Statistics.Totals.SuccessfulCheckPercent != nil || emptyBody.Statistics.Totals.AverageLatencyMS != nil || emptyBody.Statistics.Coverage.DaysWithData != 0 {
		t.Fatalf("no-data values must stay null with empty coverage: %+v", emptyBody.Statistics)
	}
	unknownResponse := f.request(t, owner, http.MethodGet, "/api/v1/monitors/"+unknown.ID+"/statistics?days=7", nil, "", "")
	var unknownBody struct {
		Statistics monitorcontracts.Statistics `json:"statistics"`
	}
	if unknownResponse.Code != http.StatusOK || json.Unmarshal(unknownResponse.Body.Bytes(), &unknownBody) != nil {
		t.Fatalf("UNKNOWN-only statistics status=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}
	if unknownBody.Statistics.Totals.Unknown != 4 || unknownBody.Statistics.Totals.Total != 4 || unknownBody.Statistics.Totals.SuccessfulCheckPercent != nil || unknownBody.Statistics.Totals.AverageLatencyMS != nil {
		t.Fatalf("UNKNOWN-only period must leave success and latency undefined: %+v", unknownBody.Statistics.Totals)
	}
	foreignRead := f.request(t, other, http.MethodGet, "/api/v1/monitors/"+monitor.ID+"/statistics?days=7", nil, "", "")
	if foreignRead.Code != http.StatusNotFound {
		t.Fatalf("foreign owner statistics read status=%d body=%s, want 404", foreignRead.Code, foreignRead.Body.String())
	}
	for _, query := range []string{"?days=14", "?days=7&days=30", "?unknown=1"} {
		invalid := f.request(t, owner, http.MethodGet, "/api/v1/monitors/"+monitor.ID+"/statistics"+query, nil, "", "")
		if invalid.Code != http.StatusBadRequest {
			t.Errorf("invalid statistics query %q returned %d: %s", query, invalid.Code, invalid.Body.String())
		}
	}
}

func TestOwnerIncidentFeedUsesAsOfKeysetAndNinetyDayRetention(t *testing.T) {
	f := newMonitorFixture(t)
	owner := f.newActor(t, "Mobile incidents owner")
	other := f.newActor(t, "Foreign incidents owner")
	f.grant(t, owner, 8)
	f.grant(t, other, 1)
	monitors := make([]monitorcontracts.Monitor, 4)
	for index := range monitors {
		var status int
		monitors[index], status = f.create(t, owner, fmt.Sprintf("Feed monitor %d", index+1), fmt.Sprintf("feed-%d.example", index+1), true)
		if status != http.StatusCreated {
			t.Fatalf("create owner monitor %d status=%d", index+1, status)
		}
	}
	foreignMonitor, status := f.create(t, other, "Foreign feed monitor", "foreign-feed.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create foreign monitor status=%d", status)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ids := []string{
		"60000000-0000-4000-8000-000000000001",
		"60000000-0000-4000-8000-000000000002",
		"60000000-0000-4000-8000-000000000003",
		"60000000-0000-4000-8000-000000000004",
	}
	for index, id := range ids {
		var closed any
		reason := ""
		if index == 3 {
			closed = base.Add(time.Minute)
			reason = "recovered"
		}
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`, id, monitors[index].ID, owner.account.ID, base, base, closed, reason); err != nil {
			t.Fatalf("seed owner incident %s: %v", id, err)
		}
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason) VALUES
	 ('60000000-0000-4000-8000-000000000005',$1,$2,now()-interval '100 days',now()-interval '100 days',now()-interval '99 days','recovered'),
	 ('60000000-0000-4000-8000-000000000006',$3,$4,$5,$5,NULL,'')`, monitors[0].ID, owner.account.ID, foreignMonitor.ID, other.account.ID, base); err != nil {
		t.Fatalf("seed retention and foreign-owner incidents: %v", err)
	}

	first := f.request(t, owner, http.MethodGet, "/api/v1/incidents?state=all&limit=2", nil, "", "")
	var firstPage monitorcontracts.IncidentFeed
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || len(firstPage.Incidents) != 2 {
		t.Fatalf("first owner feed page status=%d body=%s", first.Code, first.Body.String())
	}
	if !firstPage.Pagination.HasMore || firstPage.Pagination.NextCursor == nil {
		t.Fatalf("feed should expose a cursor after two of four retained rows: %+v", firstPage.Pagination)
	}
	if firstPage.Incidents[0].ID != ids[3] || firstPage.Incidents[1].ID != ids[2] {
		t.Fatalf("equal opened_at rows must use descending UUID tie-breaker: %v, %v", firstPage.Incidents[0].ID, firstPage.Incidents[1].ID)
	}
	if firstPage.Incidents[0].ClosedAt == nil || firstPage.Incidents[0].CloseReason == nil {
		t.Fatalf("closed incident must include its close fields: %+v", firstPage.Incidents[0])
	}
	if firstPage.Incidents[1].ClosedAt != nil || firstPage.Incidents[1].CloseReason != nil {
		t.Fatalf("active incident must serialize close fields as null: %+v", firstPage.Incidents[1])
	}
	var rawFirst struct {
		Incidents []map[string]json.RawMessage `json:"incidents"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &rawFirst); err != nil {
		t.Fatal(err)
	}
	if string(rawFirst.Incidents[1]["closedAt"]) != "null" || string(rawFirst.Incidents[1]["closeReason"]) != "null" {
		t.Fatalf("active DTO must include explicit null close fields: closedAt=%s closeReason=%s", rawFirst.Incidents[1]["closedAt"], rawFirst.Incidents[1]["closeReason"])
	}
	if firstPage.Incidents[0].Monitor.ID != monitors[3].ID || firstPage.Incidents[0].Monitor.Name != "Feed monitor 4" {
		t.Fatalf("feed must attach owner-scoped monitor details: %+v", firstPage.Incidents[0].Monitor)
	}
	// A backdated row confirmed after page one's snapshot must not shift page two.
	lateMonitor, status := f.create(t, owner, "Late feed monitor", "late-feed.example", true)
	if status != http.StatusCreated {
		t.Fatalf("create late-confirmed monitor status=%d", status)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason) VALUES
	 ('60000000-0000-4000-8000-000000000007',$1,$2,$3,now()+interval '1 minute',NULL,'')`, lateMonitor.ID, owner.account.ID, base.Add(-time.Minute)); err != nil {
		t.Fatalf("seed late-confirmed incident: %v", err)
	}
	// Closing after the first page snapshot must leave an open-looking item in
	// the continuation's as-of view. An old open incident is retained too.
	if _, err := f.pool.Exec(context.Background(), `UPDATE incidents.incidents SET closed_at=$2,close_reason='recovered' WHERE id=$1`, ids[0], firstPage.AsOf.Add(time.Minute)); err != nil {
		t.Fatalf("close an incident after page-one asOf: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO incidents.incidents(id,monitor_id,account_id,opened_at,confirmed_at,closed_at,close_reason) VALUES
	 ('60000000-0000-4000-8000-000000000008',$1,$2,now()-interval '100 days',now()-interval '100 days',NULL,'')`, monitors[0].ID, owner.account.ID); err != nil {
		t.Fatalf("seed retained old open incident: %v", err)
	}
	second := f.request(t, owner, http.MethodGet, "/api/v1/incidents?state=all&limit=2&cursor="+*firstPage.Pagination.NextCursor, nil, "", "")
	var secondPage monitorcontracts.IncidentFeed
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || len(secondPage.Incidents) != 2 {
		t.Fatalf("second owner feed page status=%d body=%s", second.Code, second.Body.String())
	}
	if !secondPage.AsOf.Equal(firstPage.AsOf) || secondPage.Incidents[0].ID != ids[1] || secondPage.Incidents[1].ID != ids[0] || !secondPage.Pagination.HasMore {
		t.Fatalf("snapshot cursor page shifted or leaked late row: first=%+v second=%+v", firstPage, secondPage)
	}
	if secondPage.Incidents[1].ClosedAt != nil || secondPage.Incidents[1].CloseReason != nil {
		t.Fatalf("an incident closed after asOf must still appear open in the old snapshot: %+v", secondPage.Incidents[1])
	}
	active := f.request(t, owner, http.MethodGet, "/api/v1/incidents?state=active&limit=10", nil, "", "")
	var activePage monitorcontracts.IncidentFeed
	if active.Code != http.StatusOK || json.Unmarshal(active.Body.Bytes(), &activePage) != nil || len(activePage.Incidents) != 4 {
		t.Fatalf("active feed status=%d incidents=%d body=%s", active.Code, len(activePage.Incidents), active.Body.String())
	}
	retainedOpen := false
	for _, incident := range activePage.Incidents {
		if incident.ID == "60000000-0000-4000-8000-000000000008" {
			retainedOpen = true
		}
	}
	if !retainedOpen {
		t.Fatal("an open incident older than 90 days must remain in active feed")
	}
	closed := f.request(t, owner, http.MethodGet, "/api/v1/incidents?state=closed&limit=10", nil, "", "")
	var closedPage monitorcontracts.IncidentFeed
	if closed.Code != http.StatusOK || json.Unmarshal(closed.Body.Bytes(), &closedPage) != nil || len(closedPage.Incidents) != 1 || closedPage.Incidents[0].ID != ids[3] || closedPage.Incidents[0].CloseReason == nil || *closedPage.Incidents[0].CloseReason != "recovered" {
		t.Fatalf("closed feed status=%d body=%s", closed.Code, closed.Body.String())
	}
	foreign := f.request(t, other, http.MethodGet, "/api/v1/incidents?state=all&limit=10", nil, "", "")
	var foreignPage monitorcontracts.IncidentFeed
	if foreign.Code != http.StatusOK || json.Unmarshal(foreign.Body.Bytes(), &foreignPage) != nil || len(foreignPage.Incidents) != 1 || foreignPage.Incidents[0].MonitorID != foreignMonitor.ID {
		t.Fatalf("foreign owner feed escaped account scope: status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	for _, query := range []string{"state=unknown", "state=all&state=closed", "limit=101", "cursor=%%%"} {
		invalid := f.request(t, owner, http.MethodGet, "/api/v1/incidents?"+query, nil, "", "")
		if invalid.Code != http.StatusBadRequest {
			t.Errorf("invalid feed query %q returned %d: %s", query, invalid.Code, invalid.Body.String())
		}
	}
}
