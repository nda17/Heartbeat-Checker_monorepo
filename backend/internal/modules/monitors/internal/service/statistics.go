package service

import (
	"context"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/repository/postgres"
)

func (s *Service) Statistics(ctx context.Context, owner, id string, days int) (contracts.Statistics, error) {
	if !ValidID(id) {
		return contracts.Statistics{}, ErrNotFound
	}
	if days != 7 && days != 30 && days != 90 {
		return contracts.Statistics{}, ErrInvalid
	}
	tx, _, err := s.begin(ctx, owner)
	if err != nil {
		return contracts.Statistics{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.repo.Get(ctx, tx, owner, id, false); err != nil {
		return contracts.Statistics{}, translate(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, 1-days)
	rows, err := s.repo.Statistics(ctx, tx, owner, id, from, today)
	if err != nil {
		return contracts.Statistics{}, err
	}
	result := contracts.Statistics{MonitorID: id, GeneratedAt: now, TimeZone: "UTC", PeriodDays: days, From: from, To: now,
		Methodology: "accepted_checks_v1", RetentionDays: 90, Coverage: contracts.StatisticsCoverage{Kind: "recorded_checks", ScheduleCompleteness: "unknown"}, Days: make([]contracts.StatisticsDay, 0, days)}
	indexed := make(map[string]postgres.StatisticsRow, len(rows))
	for _, row := range rows {
		indexed[row.Day.Format("2006-01-02")] = row
	}
	var up, down, unknown, latencySum, latencyCount int64
	for offset := 0; offset < days; offset++ {
		date := from.AddDate(0, 0, offset).Format("2006-01-02")
		row := indexed[date]
		count := postgres.CheckCountView(row.Up, row.Down, row.Unknown, row.LatencySum, row.LatencyCount)
		result.Days = append(result.Days, contracts.StatisticsDay{Date: date, CheckCounts: count, Partial: offset == days-1})
		if count.Total > 0 {
			result.Coverage.DaysWithData++
			if result.Coverage.FirstRecordedDay == nil {
				first := date
				result.Coverage.FirstRecordedDay = &first
			}
		}
		up += row.Up
		down += row.Down
		unknown += row.Unknown
		latencySum += row.LatencySum
		latencyCount += row.LatencyCount
	}
	result.Totals = postgres.CheckCountView(up, down, unknown, latencySum, latencyCount)
	return result, tx.Commit(ctx)
}
