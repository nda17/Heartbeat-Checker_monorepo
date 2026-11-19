package service

import (
	"math/big"
	"testing"
	"time"
)

func TestTariffMonthUsesMoscowCalendarBoundary(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		end  time.Time
		days int64
	}{
		{"January before February in Moscow", time.Date(2027, 1, 31, 20, 59, 59, 0, time.UTC), time.Date(2027, 1, 31, 21, 0, 0, 0, time.UTC), 31},
		{"common February", time.Date(2027, 1, 31, 21, 0, 0, 0, time.UTC), time.Date(2027, 2, 28, 21, 0, 0, 0, time.UTC), 28},
		{"leap February", time.Date(2028, 1, 31, 21, 0, 0, 0, time.UTC), time.Date(2028, 2, 29, 21, 0, 0, 0, time.UTC), 29},
		{"April", time.Date(2028, 4, 15, 12, 0, 0, 0, time.UTC), time.Date(2028, 4, 30, 21, 0, 0, 0, time.UTC), 30},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			end, days := monthEnd(test.at)
			if !end.Equal(test.end) || days != test.days {
				t.Fatalf("monthEnd(%s) = (%s,%d), want (%s,%d)", test.at, end, days, test.end, test.days)
			}
		})
	}
}

func TestWalletCutoffConsumesUnusableFractionAtExactTick(t *testing.T) {
	at := time.Date(2028, 2, 25, 11, 0, 0, 0, time.UTC)
	tariff := walletTariff{version: 1, minor: 2600, effectiveAt: at}
	w := wallet{settledAt: at, billed: 1, tariff: tariff}
	rate := rateUnits(tariff.minor, 1, 29)
	w.units.Add(rate, big.NewInt(1))
	w.project()
	cutoff := at.Add(time.Microsecond)
	if w.fundedUntil == nil || !w.fundedUntil.Equal(cutoff) {
		t.Fatalf("funded cutoff %v, want %s", w.fundedUntil, cutoff)
	}
	w.settle(cutoff, []walletTariff{tariff})
	if w.fundedUntil != nil || w.units.Sign() != 0 || w.open == nil || w.open.units.Cmp(new(big.Int).Add(rate, big.NewInt(1))) != 0 || !w.open.until.Equal(cutoff) {
		t.Fatalf("cutoff did not close exact available balance: %+v", w)
	}
}
