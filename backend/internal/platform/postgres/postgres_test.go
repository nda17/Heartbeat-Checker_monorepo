package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenDoesNotExposeMalformedDatabaseURL(t *testing.T) {
	const secret = "postgres://user:private-password%ZZ@db/app"
	_, err := Open(context.Background(), secret, time.Second)
	if err == nil {
		t.Fatal("Open() error = nil, want parse error")
	}
	if strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposes database URL: %v", err)
	}
}
