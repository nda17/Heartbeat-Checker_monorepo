package service

import (
	"testing"
	"time"
)

func TestNextMockMonthClampsMonthEndAndKeepsAnchor(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		want  time.Time
	}{
		{
			name:  "october to shorter november",
			start: time.Date(2026, time.October, 31, 12, 34, 56, 123, moscow),
			want:  time.Date(2026, time.November, 30, 12, 34, 56, 123, moscow),
		},
		{
			name:  "clamped day returns to anchor in december",
			start: time.Date(2026, time.November, 30, 12, 34, 56, 123, moscow),
			want:  time.Date(2026, time.December, 31, 12, 34, 56, 123, moscow),
		},
		{
			name:  "common year february",
			start: time.Date(2027, time.January, 31, 12, 34, 56, 123, moscow),
			want:  time.Date(2027, time.February, 28, 12, 34, 56, 123, moscow),
		},
		{
			name:  "leap year february",
			start: time.Date(2028, time.January, 31, 12, 34, 56, 123, moscow),
			want:  time.Date(2028, time.February, 29, 12, 34, 56, 123, moscow),
		},
		{
			name:  "february to march restores anchor",
			start: time.Date(2028, time.February, 29, 12, 34, 56, 123, moscow),
			want:  time.Date(2028, time.March, 31, 12, 34, 56, 123, moscow),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := nextMockMonth(test.start, 31)
			if !got.Equal(test.want) {
				t.Fatalf("nextMockMonth(%s) = %s, want %s", test.start, got, test.want)
			}
		})
	}
}
