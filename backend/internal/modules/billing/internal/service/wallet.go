package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

const unitsPerMinor int64 = 32622912000000000
const daysLCM int64 = 377580

var minorUnit = big.NewInt(unitsPerMinor)
var moscow = time.FixedZone("MSK", 3*60*60)

type walletTariff struct {
	version     int64
	minor       int
	effectiveAt time.Time
}

type usageBucket struct {
	hour, from, until time.Time
	units             big.Int
	minor             int64
}

type wallet struct {
	exists      bool
	account     string
	enabled     int
	active      bool
	units       big.Int
	settledAt   time.Time
	billed      int
	epoch       int64
	fundedUntil *time.Time
	tariff      walletTariff
	open        *usageBucket
	closed      []*usageBucket
}

func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(($1::uuid)::text, 61729))`, accountID)
	return err
}

func minorAmount(units *big.Int) int64 {
	var minor big.Int
	return minor.Quo(units, minorUnit).Int64()
}

func monthEnd(at time.Time) (time.Time, int64) {
	local := at.In(moscow)
	last := time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, moscow)
	return last.AddDate(0, 0, 1).UTC(), int64(last.Day())
}

func rateUnits(price, monitors int, days int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(int64(price)*daysLCM/days), big.NewInt(int64(monitors)))
}

func fundedUntil(at time.Time, balance *big.Int, price, monitors int) *time.Time {
	if balance.Sign() == 0 || monitors == 0 {
		return nil
	}
	end, days := monthEnd(at)
	rate := rateUnits(price, monitors, days)
	cost := new(big.Int).Mul(rate, big.NewInt(end.Sub(at).Microseconds()))
	if balance.Cmp(cost) < 0 {
		micros := new(big.Int).Quo(balance, rate).Int64()
		if micros == 0 {
			return nil
		}
		until := at.Add(time.Duration(micros) * time.Microsecond)
		return &until
	}
	remaining := new(big.Int).Sub(balance, cost)
	monthCost := new(big.Int).Mul(minorUnit, big.NewInt(int64(price)))
	monthCost.Mul(monthCost, big.NewInt(int64(monitors)))
	var months, fraction big.Int
	months.QuoRem(remaining, monthCost, &fraction)
	// HTTP timestamps must be RFC3339. Such a balance is at least thousands
	// of years of service; the following boundary will be advanced on use.
	last := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	if !months.IsInt64() || months.Int64() > 95000 {
		return &last
	}
	start := end.In(moscow).AddDate(0, int(months.Int64()), 0).UTC()
	if !start.Before(last) {
		return &last
	}
	_, days = monthEnd(start)
	micros := fraction.Quo(&fraction, rateUnits(price, monitors, days)).Int64()
	until := start.Add(time.Duration(micros) * time.Microsecond)
	return &until
}

func (w *wallet) record(from, until time.Time, units *big.Int) {
	hour := from.Truncate(time.Hour)
	if w.open != nil && !w.open.hour.Equal(hour) {
		w.closed = append(w.closed, w.open)
		w.open = nil
	}
	if w.open == nil {
		w.open = &usageBucket{hour: hour, from: from}
	}
	before := minorAmount(&w.units)
	w.units.Sub(&w.units, units)
	w.open.units.Add(&w.open.units, units)
	w.open.until = until
	w.open.minor += before - minorAmount(&w.units)
}

// settle charges each interval under the tariff and enabled count in effect
// during that interval. An exhausted wallet advances across the unpaid gap
// without charging it when money later arrives.
func (w *wallet) settle(now time.Time, tariffs []walletTariff) {
	cursor := w.settledAt
	next := 0
	for cursor.Before(now) && w.billed > 0 && w.units.Sign() > 0 {
		for next+1 < len(tariffs) && !tariffs[next+1].effectiveAt.After(cursor) {
			next++
		}
		monthUntil, days := monthEnd(cursor)
		stop := cursor.Truncate(time.Hour).Add(time.Hour)
		if monthUntil.Before(stop) {
			stop = monthUntil
		}
		if now.Before(stop) {
			stop = now
		}
		if next+1 < len(tariffs) && tariffs[next+1].effectiveAt.Before(stop) {
			stop = tariffs[next+1].effectiveAt
		}
		rate := rateUnits(tariffs[next].minor, w.billed, days)
		micros := stop.Sub(cursor).Microseconds()
		cost := new(big.Int).Mul(rate, big.NewInt(micros))
		if w.units.Cmp(cost) < 0 {
			fundedMicros := new(big.Int).Quo(&w.units, rate).Int64()
			stop = cursor.Add(time.Duration(fundedMicros) * time.Microsecond)
			// The part smaller than a database clock tick is debited at the
			// cutoff. It cannot buy an additional billable microsecond.
			cost.Set(&w.units)
		}
		w.record(cursor, stop, cost)
		cursor = stop
	}
	w.settledAt = now
	w.tariff = tariffs[len(tariffs)-1]
	w.project()
}

func (w *wallet) project() {
	previous := w.fundedUntil
	w.fundedUntil = fundedUntil(w.settledAt, &w.units, w.tariff.minor, w.billed)
	if w.fundedUntil == nil && w.billed > 0 && w.units.Sign() > 0 {
		// The remaining units cannot fund one database clock tick.
		w.record(w.settledAt, w.settledAt, new(big.Int).Set(&w.units))
	}
	if (previous == nil) != (w.fundedUntil == nil) {
		w.epoch++
	}
}

func (w *wallet) view() contracts.Balance {
	minor := minorAmount(&w.units)
	var active int
	if w.fundedUntil != nil {
		active = w.billed
	}
	return contracts.Balance{
		AvailableMinor: minor, PositiveBelowCent: minor == 0 && w.units.Sign() > 0,
		Currency: "RUB", EnabledMonitors: w.enabled, AccountActive: w.active, AsOf: w.settledAt, FundedUntil: w.fundedUntil,
		Suspended: w.active && w.enabled > 0 && active == 0, ActivityEpoch: w.epoch, ActiveMonitors: active,
		PricePerMonitorMinor: w.tariff.minor, PriceVersion: w.tariff.version,
	}
}

func (s *Service) loadWallet(ctx context.Context, tx pgx.Tx, account string) (*wallet, error) {
	if err := lockAccount(ctx, tx, account); err != nil {
		return nil, err
	}
	// Taking this shared lock first keeps the clock and history on the same
	// side of a concurrent administrative price update.
	price, err := s.PricingTx(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	w := &wallet{account: account}
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT clock_timestamp(),
        EXISTS (SELECT 1 FROM accounts.accounts WHERE id=$1 AND status<>'deleted'),
        EXISTS (SELECT 1 FROM accounts.accounts WHERE id=$1 AND status='active'),
        (SELECT count(*) FROM monitors.monitors WHERE account_id=$1 AND enabled)`, account).Scan(&now, &w.exists, &w.active, &w.enabled)
	if err != nil {
		return nil, err
	}
	w.settledAt, w.tariff = now.UTC(), walletTariff{price.Version, price.PricePerMonitorMinor, price.UpdatedAt}
	if !w.exists {
		return w, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO billing.wallets(account_id,settled_at) VALUES($1,$2) ON CONFLICT DO NOTHING`, account, now); err != nil {
		return nil, err
	}
	var units string
	if err = tx.QueryRow(ctx, `SELECT available_units::text,settled_at,billed_monitors,funded_until,activity_epoch FROM billing.wallets WHERE account_id=$1 FOR UPDATE`, account).Scan(&units, &w.settledAt, &w.billed, &w.fundedUntil, &w.epoch); err != nil {
		return nil, err
	}
	if _, ok := w.units.SetString(units, 10); !ok {
		return nil, errors.New("invalid persisted wallet units")
	}
	w.settledAt = w.settledAt.UTC()
	now = now.UTC()
	if now.Before(w.settledAt) {
		now = w.settledAt
	}
	var bucket usageBucket
	err = tx.QueryRow(ctx, `SELECT hour_start,from_at,until_at,units::text,amount_minor FROM billing.wallet_usage WHERE account_id=$1`, account).Scan(&bucket.hour, &bucket.from, &bucket.until, &units, &bucket.minor)
	if err == nil {
		if _, ok := bucket.units.SetString(units, 10); !ok {
			return nil, errors.New("invalid persisted wallet usage units")
		}
		w.open = &bucket
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	tariffs := []walletTariff{w.tariff}
	if price.UpdatedAt.After(now) || (w.billed > 0 && w.units.Sign() > 0 && w.settledAt.Before(price.UpdatedAt)) {
		rows, err := tx.Query(ctx, `SELECT version,price_per_monitor_minor,effective_at FROM billing.wallet_tariffs
            WHERE effective_at>=COALESCE(
                (SELECT max(effective_at) FROM billing.wallet_tariffs WHERE effective_at<=$1),
                '-infinity'::timestamptz)
            AND effective_at<=$2 ORDER BY effective_at`, w.settledAt, now)
		if err != nil {
			return nil, err
		}
		tariffs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (walletTariff, error) {
			var t walletTariff
			err := row.Scan(&t.version, &t.minor, &t.effectiveAt)
			return t, err
		})
		if err != nil {
			return nil, err
		}
		if len(tariffs) == 0 {
			return nil, ErrPricingUnavailable
		}
	}
	w.settle(now, tariffs)
	return w, nil
}

func (w *wallet) sync() {
	w.billed = 0
	if w.active {
		w.billed = w.enabled
	}
	w.project()
}

func (w *wallet) save(ctx context.Context, tx pgx.Tx) error {
	if !w.exists {
		return nil
	}
	now := w.settledAt
	if w.open != nil && !w.open.hour.Add(time.Hour).After(now) {
		w.closed = append(w.closed, w.open)
		w.open = nil
	}
	var due *time.Time
	if w.fundedUntil != nil || w.open != nil {
		nextHour := now.Truncate(time.Hour).Add(time.Hour)
		due = &nextHour
		if w.fundedUntil != nil && w.fundedUntil.Before(nextHour) {
			due = w.fundedUntil
		}
	}
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE billing.wallets SET available_units=$2::text::numeric,settled_at=$3,billed_monitors=$4,
        funded_until=$5,next_settlement_at=$6,activity_epoch=$7 WHERE account_id=$1`, w.account, w.units.String(), now, w.billed, w.fundedUntil, due, w.epoch)
	if w.open == nil {
		batch.Queue(`DELETE FROM billing.wallet_usage WHERE account_id=$1`, w.account)
	} else {
		b := w.open
		batch.Queue(`INSERT INTO billing.wallet_usage(account_id,hour_start,from_at,until_at,units,amount_minor)
            VALUES($1,$2,$3,$4,$5::text::numeric,$6)
            ON CONFLICT(account_id) DO UPDATE SET hour_start=EXCLUDED.hour_start,from_at=EXCLUDED.from_at,
            until_at=EXCLUDED.until_at,units=EXCLUDED.units,amount_minor=EXCLUDED.amount_minor`, w.account, b.hour, b.from, b.until, b.units.String(), b.minor)
	}
	if len(w.closed) > 0 {
		hours := make([]time.Time, 0, len(w.closed))
		from := make([]time.Time, 0, len(w.closed))
		until := make([]time.Time, 0, len(w.closed))
		units := make([]string, 0, len(w.closed))
		amounts := make([]int64, 0, len(w.closed))
		for _, b := range w.closed {
			hours = append(hours, b.hour)
			from = append(from, b.from)
			until = append(until, b.until)
			units = append(units, b.units.String())
			amounts = append(amounts, b.minor)
		}
		batch.Queue(`INSERT INTO billing.wallet_ledger(account_id,kind,units,amount_minor,created_at,hour_start,period_start,period_end)
            SELECT $1,'usage',-usage.units::numeric,-usage.amount,usage.hour+interval '1 hour',usage.hour,usage.from_at,usage.until_at
            FROM unnest($2::timestamptz[],$3::timestamptz[],$4::timestamptz[],$5::text[],$6::bigint[]) AS usage(hour,from_at,until_at,units,amount)`,
			w.account, hours, from, until, units, amounts)
	}
	return tx.SendBatch(ctx, batch).Close()
}

// WalletTx advances the previous monitoring interval and reads account intent
// under the same account lock held by monitor, credit and account mutations.
func (s *Service) WalletTx(ctx context.Context, tx pgx.Tx, account string) (contracts.Balance, error) {
	w, err := s.loadWallet(ctx, tx, account)
	if err != nil {
		return contracts.Balance{}, err
	}
	w.sync()
	return w.view(), w.save(ctx, tx)
}

func (s *Service) Balance(ctx context.Context, account string) (contracts.Balance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Balance{}, err
	}
	defer tx.Rollback(ctx)
	balance, err := s.WalletTx(ctx, tx, account)
	if err != nil {
		return balance, err
	}
	return balance, tx.Commit(ctx)
}

func (s *Service) ProcessOneWallet(ctx context.Context) (bool, error) {
	var account string
	err := s.pool.QueryRow(ctx, `SELECT account_id::text FROM billing.wallets
        WHERE next_settlement_at<=statement_timestamp() ORDER BY next_settlement_at,account_id LIMIT 1`).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = s.Balance(ctx, account)
	return true, err
}

func (s *Service) AdminCreditTx(ctx context.Context, tx pgx.Tx, account, actor string, amount int, key, reason string) (before, after contracts.Balance, applied bool, err error) {
	if amount < 1 || amount > MaxOrderAmountMinor || !validID(key) {
		return before, after, false, ErrInvalid
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%s", actor, amount, reason)))
	return s.creditWallet(ctx, tx, account, amount, "admin_credit", nil, key, hash[:])
}

func (s *Service) creditWallet(ctx context.Context, tx pgx.Tx, account string, amount int, kind string, orderID *string, requestID string, hash []byte) (before, after contracts.Balance, applied bool, err error) {
	w, err := s.loadWallet(ctx, tx, account)
	if err != nil {
		return before, after, false, err
	}
	if !w.exists {
		return before, after, false, ErrNotFound
	}
	before = w.view()
	units := new(big.Int).Mul(big.NewInt(int64(amount)), minorUnit)
	tag, err := tx.Exec(ctx, `INSERT INTO billing.wallet_ledger(account_id,kind,units,amount_minor,created_at,order_id,request_id,request_hash)
        VALUES($1,$2,$3::text::numeric,$4,$5,$6,NULLIF($7,'')::uuid,$8) ON CONFLICT DO NOTHING`,
		account, kind, units.String(), amount, w.settledAt, orderID, requestID, hash)
	if err != nil {
		return before, after, false, err
	}
	if tag.RowsAffected() == 0 && requestID != "" {
		var matches bool
		err := tx.QueryRow(ctx, `SELECT request_hash=$3 FROM billing.wallet_ledger WHERE account_id=$1 AND request_id=$2`, account, requestID, hash).Scan(&matches)
		if err != nil {
			return before, after, false, err
		}
		if !matches {
			return before, after, false, ErrConflict
		}
	}
	if tag.RowsAffected() != 0 {
		w.units.Add(&w.units, units)
	}
	w.sync()
	return before, w.view(), tag.RowsAffected() != 0, w.save(ctx, tx)
}

func (s *Service) Ledger(ctx context.Context, account string, page, pageSize int) (contracts.LedgerPage, error) {
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return contracts.LedgerPage{}, ErrInvalid
	}
	if _, err := s.Balance(ctx, account); err != nil {
		return contracts.LedgerPage{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return contracts.LedgerPage{}, err
	}
	defer tx.Rollback(ctx)
	result := contracts.LedgerPage{Entries: []contracts.LedgerEntry{}}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing.wallet_ledger WHERE account_id=$1`, account).Scan(&total); err != nil {
		return result, err
	}
	totalPages := max(1, (total+pageSize-1)/pageSize)
	page = min(page, totalPages)
	result.Pagination = contracts.Pagination{Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages}
	rows, err := tx.Query(ctx, `SELECT id::text,CASE WHEN kind='local_credit' THEN 'admin_credit' ELSE kind END,amount_minor,created_at,period_start,period_end
        FROM billing.wallet_ledger WHERE account_id=$1
        ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, account, pageSize, (page-1)*pageSize)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var e contracts.LedgerEntry
		if err = rows.Scan(&e.ID, &e.Kind, &e.AmountMinor, &e.CreatedAt, &e.PeriodStart, &e.PeriodEnd); err != nil {
			return result, err
		}
		e.PositiveBelowCent = e.Kind == "usage" && e.AmountMinor == 0
		result.Entries = append(result.Entries, e)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
