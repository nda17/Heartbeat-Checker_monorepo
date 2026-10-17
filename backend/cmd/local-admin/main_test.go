package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsPublicDatabaseBeforeReadingPasswordFile(t *testing.T) {
	t.Setenv("APP_ENV", "local")
	t.Setenv("DATABASE_URL", "postgres://local:local@203.0.113.10:55432/heartbeat_local?sslmode=disable")
	missingSecretPath := filepath.Join(t.TempDir(), "does-not-exist")
	args := []string{
		"--email", "admin@example.test",
		"--name", "Local Admin",
		"--quota", "1000",
		"--until", time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"--password-file", missingSecretPath,
		"--reason", "test",
	}
	var output bytes.Buffer
	err := run(args, &output)
	if err == nil || !strings.Contains(err.Error(), "project loopback PostgreSQL") {
		t.Fatalf("run with public database error=%v, want local database guard rejection", err)
	}
	if strings.Contains(err.Error(), "password file") {
		t.Fatalf("run tried to read the password file before rejecting public database: %v", err)
	}
}

func TestReadPasswordFileRequiresMode0600AndPreservesExactBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "password")
	want := []byte("secure local password\n")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readPasswordFile(path)
	if err != nil {
		t.Fatalf("read mode-0600 password: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("read password changed exact bytes: got length %d, want %d", len(got), len(want))
	}
	clear(got)

	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(path); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("read non-0600 password file error=%v, want permissions rejection", err)
	}
}

func TestReadPasswordFileRejectsSymlinkAndLengthOutsideBounds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secure local password"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "password-link")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(symlink); err == nil {
		t.Fatal("readPasswordFile accepted a symlink")
	}

	for _, tc := range []struct {
		name     string
		contents []byte
	}{
		{name: "short", contents: []byte("12345678901")},
		{name: "oversize", contents: make([]byte, 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "password")
			if err := os.WriteFile(path, tc.contents, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readPasswordFile(path); err == nil {
				t.Fatalf("readPasswordFile accepted %d bytes", len(tc.contents))
			}
		})
	}
}
