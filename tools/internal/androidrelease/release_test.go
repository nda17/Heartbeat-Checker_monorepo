package androidrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSourceSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testFingerprint = "AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB"

func testManifest(apk []byte) Manifest {
	sum := sha256.Sum256(apk)
	return Manifest{
		SchemaVersion: 1, PackageName: "ru.heartbeatchecker.app", VersionName: "1.2.3", VersionCode: 42,
		SourceSHA: testSourceSHA, ReleasedAt: "2026-10-19T12:34:56Z", SizeBytes: int64(len(apk)),
		SHA256: hex.EncodeToString(sum[:]), SigningCertificateSHA256: testFingerprint,
		DownloadURL: PublicBase + "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk",
	}
}

func writeManifest(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReturnsVersionedImmutableKeyAndChecksProvenance(t *testing.T) {
	apk := []byte("synthetic-apk")
	dir := t.TempDir()
	path := filepath.Join(dir, "release.json")
	manifest := testManifest(apk)
	writeManifest(t, path, manifest)

	got, err := Validate(context.Background(), path, Options{SourceSHA: testSourceSHA, Fingerprint: testFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectKey() != "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk" {
		t.Fatalf("unexpected immutable key %q", got.ObjectKey())
	}
	if _, err := Validate(context.Background(), path, Options{SourceSHA: strings.Repeat("b", 40)}); err == nil || err.Error() != "Source mismatch" {
		t.Fatalf("expected source mismatch, got %v", err)
	}
	if _, err := Validate(context.Background(), path, Options{Fingerprint: strings.Repeat("CD:", 31) + "CD"}); err == nil || err.Error() != "Signing certificate mismatch" {
		t.Fatalf("expected signing mismatch, got %v", err)
	}
}

func TestValidateRejectsExtraDuplicateAndNoncanonicalManifestFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.json")
	valid := testManifest([]byte("synthetic-apk"))

	tests := []struct{ name, raw string }{
		{"extra field", func() string {
			b, _ := json.Marshal(valid)
			return strings.TrimSuffix(string(b), "}") + ",\"secret\":\"must not appear\"}"
		}()},
		{"duplicate field", func() string {
			b, _ := json.Marshal(valid)
			return strings.TrimSuffix(string(b), "}") + ",\"versionCode\":42}"
		}()},
		{"noncanonical URL", func() string {
			v := valid
			v.DownloadURL = strings.Replace(v.DownloadURL, "/1.2.3/", "/latest/", 1)
			b, _ := json.Marshal(v)
			return string(b)
		}()},
		{"URL query", func() string { v := valid; v.DownloadURL += "?token=secret"; b, _ := json.Marshal(v); return string(b) }()},
		{"boolean integer", func() string {
			b, _ := json.Marshal(valid)
			return strings.Replace(string(b), `"versionCode":42`, `"versionCode":true`, 1)
		}()},
		{"bad digest", func() string {
			v := valid
			v.SHA256 = strings.Repeat("z", 64)
			b, _ := json.Marshal(v)
			return string(b)
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(context.Background(), path, Options{}); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestValidateRequiresRegularAPKAndChecksSizeAndDigest(t *testing.T) {
	dir := t.TempDir()
	apk := []byte("synthetic-apk")
	apkPath := filepath.Join(dir, "app.apk")
	if err := os.WriteFile(apkPath, apk, 0600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "release.json")
	manifest := testManifest(apk)
	writeManifest(t, manifestPath, manifest)
	if _, err := Validate(context.Background(), manifestPath, Options{APKPath: apkPath}); err != nil {
		t.Fatalf("valid apk rejected: %v", err)
	}

	manifest.SizeBytes++
	writeManifest(t, manifestPath, manifest)
	if _, err := Validate(context.Background(), manifestPath, Options{APKPath: apkPath}); err == nil || err.Error() != "APK size mismatch" {
		t.Fatalf("expected APK size mismatch, got %v", err)
	}
	manifest.SizeBytes--
	manifest.SHA256 = strings.Repeat("0", 64)
	writeManifest(t, manifestPath, manifest)
	if _, err := Validate(context.Background(), manifestPath, Options{APKPath: apkPath}); err == nil || err.Error() != "APK checksum mismatch" {
		t.Fatalf("expected APK checksum mismatch, got %v", err)
	}

	manifest = testManifest(apk)
	writeManifest(t, manifestPath, manifest)
	link := filepath.Join(dir, "link.apk")
	if err := os.Symlink(apkPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), manifestPath, Options{APKPath: link}); err == nil || err.Error() != "Missing regular APK" {
		t.Fatalf("expected symlink APK rejection, got %v", err)
	}
}
