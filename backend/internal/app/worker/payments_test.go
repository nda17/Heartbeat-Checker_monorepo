package worker

import (
	"testing"
	"time"
)

func TestPaymentRetryAfterUsesLargestBoundedProviderHint(t *testing.T) {
	tests := []struct {
		name   string
		header string
		body   string
		want   time.Duration
	}{
		{name: "header seconds", header: "7", want: 7 * time.Second},
		{name: "body takes precedence", header: "5", body: `{"parameters":{"retry_after":11}}`, want: 11 * time.Second},
		{name: "header takes precedence", header: "13", body: `{"parameters":{"retry_after":4}}`, want: 13 * time.Second},
		{name: "maximum accepted body delay", body: `{"parameters":{"retry_after":86400}}`, want: 24 * time.Hour},
		{name: "oversized hints ignored", header: "86401", body: `{"parameters":{"retry_after":86401}}`, want: 0},
		{name: "invalid hints ignored", header: "invalid", body: `{"parameters":{"retry_after":-1}}`, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := paymentRetryAfter(test.header, []byte(test.body)); got != test.want {
				t.Fatalf("paymentRetryAfter(%q, %q)=%s, want %s", test.header, test.body, got, test.want)
			}
		})
	}
}
