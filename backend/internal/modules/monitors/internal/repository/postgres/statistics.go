package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

type StatisticsRow struct {
	Day                                         time.Time
	Up, Down, Unknown, LatencySum, LatencyCount int64
}

func (r *Repository) Statistics(ctx context.Context, tx pgx.Tx, owner, id string, from, to time.Time) ([]StatisticsRow, error) {
	rows, err := tx.Query(ctx, `SELECT day,up_count,down_count,unknown_count,latency_sum_ms,latency_count
 FROM monitors.daily_statistics WHERE account_id=$1 AND monitor_id=$2 AND day >= $3::date AND day <= $4::date
 ORDER BY day`, owner, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StatisticsRow{}
	for rows.Next() {
		var day StatisticsRow
		if err := rows.Scan(&day.Day, &day.Up, &day.Down, &day.Unknown, &day.LatencySum, &day.LatencyCount); err != nil {
			return nil, err
		}
		result = append(result, day)
	}
	return result, rows.Err()
}

// CheckCountView leaves absent measurements null; UNKNOWN never enters either
// denominator. Latency is the duration of historical checks of any check type.
func CheckCountView(up, down, unknown, latencySum, latencyCount int64) contracts.CheckCounts {
	result := contracts.CheckCounts{Up: up, Down: down, Unknown: unknown, Total: up + down + unknown, LatencySampleCount: latencyCount}
	if up+down > 0 {
		value := 100 * float64(up) / float64(up+down)
		result.SuccessfulCheckPercent = &value
	}
	if latencyCount > 0 {
		value := float64(latencySum) / float64(latencyCount)
		result.AverageLatencyMS = &value
	}
	return result
}

func (r *Repository) IncidentMonitors(ctx context.Context, tx pgx.Tx, owner string, ids []string) (map[string]contracts.IncidentMonitor, error) {
	result := make(map[string]contracts.IncidentMonitor, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,name,check_type,CASE check_type WHEN 'http' THEN url WHEN 'tcp' THEN host||':'||port::text ELSE host END,time_zone FROM monitors.monitors WHERE account_id=$1 AND id=ANY($2::uuid[])`, owner, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var monitor contracts.IncidentMonitor
		if err := rows.Scan(&monitor.ID, &monitor.Name, &monitor.CheckType, &monitor.Target, &monitor.TimeZone); err != nil {
			return nil, err
		}
		result[monitor.ID] = monitor
	}
	return result, rows.Err()
}
