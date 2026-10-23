package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"time"

	incidents "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

type incidentCursor struct {
	Version  int       `json:"v"`
	AsOf     time.Time `json:"asOf"`
	OpenedAt time.Time `json:"openedAt"`
	ID       string    `json:"id"`
	State    string    `json:"state"`
}

func (s *Service) IncidentFeed(ctx context.Context, owner, state string, limit int, cursor string) (contracts.IncidentFeed, error) {
	if (state != "all" && state != "active" && state != "closed") || limit < 1 || limit > 100 || len(cursor) > 1024 {
		return contracts.IncidentFeed{}, ErrInvalid
	}
	asOf := time.Now().UTC().Truncate(time.Microsecond)
	options := incidents.FeedOptions{State: state, Limit: limit, AsOf: asOf}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil {
			return contracts.IncidentFeed{}, ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var saved incidentCursor
		if decoder.Decode(&saved) != nil || decoder.Decode(new(any)) != io.EOF || saved.Version != 1 || saved.State != state || !ValidID(saved.ID) || saved.AsOf.IsZero() || saved.OpenedAt.IsZero() || saved.AsOf.After(asOf) || saved.OpenedAt.After(saved.AsOf) {
			return contracts.IncidentFeed{}, ErrInvalid
		}
		// Accept only the canonical cursor emitted below, including unique JSON
		// keys. Client-supplied ownership is never part of this cursor.
		canonical, err := json.Marshal(saved)
		if err != nil || !bytes.Equal(raw, canonical) {
			return contracts.IncidentFeed{}, ErrInvalid
		}
		options.AsOf = saved.AsOf
		options.BeforeOpenedAt = &saved.OpenedAt
		options.BeforeID = saved.ID
	}
	tx, _, err := s.begin(ctx, owner)
	if err != nil {
		return contracts.IncidentFeed{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := s.incidents.Feed(ctx, tx, owner, options)
	if err != nil {
		return contracts.IncidentFeed{}, err
	}
	result := contracts.IncidentFeed{Incidents: []contracts.IncidentFeedItem{}, AsOf: options.AsOf, Pagination: contracts.IncidentFeedPagination{Limit: limit, HasMore: len(rows) > limit}}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MonitorID)
	}
	monitors, err := s.repo.IncidentMonitors(ctx, tx, owner, ids)
	if err != nil {
		return contracts.IncidentFeed{}, err
	}
	for _, row := range rows {
		monitor, ok := monitors[row.MonitorID]
		if !ok {
			continue
		}
		var closeReason *string
		if row.ClosedAt != nil && row.CloseReason != "" {
			reason := row.CloseReason
			closeReason = &reason
		}
		result.Incidents = append(result.Incidents, contracts.IncidentFeedItem{ID: row.ID, Number: row.Number, MonitorID: row.MonitorID,
			OpenedAt: row.OpenedAt, ConfirmedAt: row.ConfirmedAt, ClosedAt: row.ClosedAt, CloseReason: closeReason, Monitor: monitor})
	}
	if result.Pagination.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		encoded, err := json.Marshal(incidentCursor{Version: 1, AsOf: options.AsOf, OpenedAt: last.OpenedAt, ID: last.ID, State: state})
		if err != nil {
			return contracts.IncidentFeed{}, err
		}
		value := base64.RawURLEncoding.EncodeToString(encoded)
		result.Pagination.NextCursor = &value
	}
	return result, tx.Commit(ctx)
}
