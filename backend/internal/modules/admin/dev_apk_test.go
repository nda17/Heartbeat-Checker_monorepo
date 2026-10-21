package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeDevArtifact(t *testing.T, root string, apk []byte, mutate func(map[string]any)) {
	t.Helper()
	digest := sha256.Sum256(apk)
	hash := hex.EncodeToString(digest[:])
	manifest := map[string]any{
		"schemaVersion":            1,
		"packageName":              "ru.heartbeatchecker.app.dev",
		"versionCode":              28,
		"versionName":              "0.28.3-dev",
		"sourceSha":                strings.Repeat("a", 40),
		"builtAt":                  "2026-10-22T10:00:00Z",
		"sizeBytes":                len(apk),
		"sha256":                   hash,
		"signingCertificateSha256": strings.Repeat("AA:", 31) + "AA",
	}
	if mutate != nil {
		mutate(manifest)
	}
	releaseDir := filepath.Join(root, "releases", hash)
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, devAPKName), apk, 0644); err != nil {
		t.Fatal(err)
	}
}

func devAPKTestServer(root string) (*httptest.Server, *atomic.Int32) {
	var checks atomic.Int32
	mux := http.NewServeMux()
	RegisterDevAPK(mux, root, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			checks.Add(1)
			switch r.Header.Get("X-Test-Role") {
			case "admin":
				next.ServeHTTP(w, r)
			case "user":
				devWrite(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			default:
				devWrite(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
		})
	})
	return httptest.NewServer(mux), &checks
}

func TestDevAPKAuthGateRunsBeforeMethodRangeOrArtifactLookup(t *testing.T) {
	server, checks := devAPKTestServer(filepath.Join(t.TempDir(), "missing-private-artifact"))
	defer server.Close()
	for _, tc := range []struct {
		name, method, role, rangeHeader string
		wantStatus                      int
	}{
		{"anonymous GET", http.MethodGet, "", "", http.StatusUnauthorized},
		{"ordinary user GET", http.MethodGet, "user", "", http.StatusForbidden},
		{"ordinary user HEAD", http.MethodHead, "user", "", http.StatusForbidden},
		{"ordinary user range", http.MethodGet, "user", "bytes=0-3", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, server.URL+devAPKPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.role != "" {
				req.Header.Set("X-Test-Role", tc.role)
			}
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if res.StatusCode == http.StatusForbidden && res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("forbidden response exposed artifact behavior: %#v", res.Header)
			}
		})
	}
	if got := checks.Load(); got != 4 {
		t.Fatalf("auth gate calls = %d, want 4", got)
	}
}

func TestDevAPKDownloadServesVerifiedBytesAndSupportsAuthorizedRange(t *testing.T) {
	root := t.TempDir()
	apk := []byte{0x50, 0x4b, 0x03, 0x04, 0x10, 0x20, 0x30}
	writeDevArtifact(t, root, apk, nil)
	server, _ := devAPKTestServer(root)
	defer server.Close()
	client := server.Client()

	req, err := http.NewRequest(http.MethodGet, server.URL+devAPKPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Role", "admin")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, apk) {
		t.Fatalf("download body = %v, want %v", body, apk)
	}
	if res.Header.Get("Content-Type") != "application/vnd.android.package-archive" || res.Header.Get("ETag") != `"`+hex.EncodeToString(sha256Sum(apk))+`"` {
		t.Fatalf("unsafe or missing APK response headers: %#v", res.Header)
	}
	if res.Header.Get("Cache-Control") != "private, no-store, no-transform" {
		t.Fatalf("Cache-Control = %q", res.Header.Get("Cache-Control"))
	}

	headReq, err := http.NewRequest(http.MethodHead, server.URL+devAPKPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	headReq.Header.Set("X-Test-Role", "admin")
	headRes, err := client.Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	defer headRes.Body.Close()
	if headRes.StatusCode != http.StatusOK || headRes.ContentLength != int64(len(apk)) {
		t.Fatalf("HEAD status/content length = %d/%d, want 200/%d", headRes.StatusCode, headRes.ContentLength, len(apk))
	}
	headBody, err := io.ReadAll(headRes.Body)
	if err != nil || len(headBody) != 0 {
		t.Fatalf("HEAD body = %v, err = %v", headBody, err)
	}

	rangeReq, err := http.NewRequest(http.MethodGet, server.URL+devAPKPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	rangeReq.Header.Set("X-Test-Role", "admin")
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeRes, err := client.Do(rangeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer rangeRes.Body.Close()
	if rangeRes.StatusCode != http.StatusPartialContent {
		t.Fatalf("authorized Range status = %d, want 206", rangeRes.StatusCode)
	}
	ranged, err := io.ReadAll(rangeRes.Body)
	if err != nil || !bytes.Equal(ranged, apk[:4]) {
		t.Fatalf("Range bytes = %v, err = %v", ranged, err)
	}
}

func sha256Sum(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}

func TestDevAPKMetadataRequiresValidManifestAndMatchingBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown manifest field", func(m map[string]any) { m["unexpected"] = true }},
		{"invalid package", func(m map[string]any) { m["packageName"] = "ru.heartbeatchecker.app" }},
		{"wrong size", func(m map[string]any) { m["sizeBytes"] = 999 }},
		{"wrong digest", func(m map[string]any) { m["sha256"] = strings.Repeat("0", 64) }},
		{"invalid timestamp", func(m map[string]any) { m["builtAt"] = "not-a-time" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDevArtifact(t, root, []byte("valid-apk"), tc.mutate)
			server, _ := devAPKTestServer(root)
			defer server.Close()
			req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/admin/android-dev", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Test-Role", "admin")
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("invalid private artifact status = %d, want 404", res.StatusCode)
			}
		})
	}

	t.Run("bytes differ from current manifest digest", func(t *testing.T) {
		root := t.TempDir()
		writeDevArtifact(t, root, []byte("original-apk"), nil)
		files, err := filepath.Glob(filepath.Join(root, "releases", "*", devAPKName))
		if err != nil || len(files) != 1 {
			t.Fatalf("APK fixture files = %v, err = %v", files, err)
		}
		if err := os.WriteFile(files[0], []byte("tampered-apk"), 0644); err != nil {
			t.Fatal(err)
		}
		server, _ := devAPKTestServer(root)
		defer server.Close()
		req, err := http.NewRequest(http.MethodGet, server.URL+devAPKPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Role", "admin")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("tampered artifact status = %d, want 404", res.StatusCode)
		}
	})
}
