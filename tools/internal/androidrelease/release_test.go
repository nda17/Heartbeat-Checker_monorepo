package androidrelease

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

func TestRunHuaweiBindsDistinctManifestIdentitiesToAPKReceipt(t *testing.T) {
	dir := t.TempDir()
	apk := []byte("synthetic-huawei-release-apk")
	apkPath := filepath.Join(dir, "release.apk")
	manifestPath := filepath.Join(dir, "manifest.txt")
	receiptPath := filepath.Join(dir, "huawei-receipt.json")
	if err := os.WriteFile(apkPath, apk, 0600); err != nil {
		t.Fatal(err)
	}
	const appID, projectID, cpID = "11001", "22002", "33003"
	if appID == projectID || projectID == cpID || appID == cpID {
		t.Fatal("fixture IDs must be distinct")
	}
	if err := os.WriteFile(manifestPath, []byte(huaweiManifestDump(true, appID, projectID, cpID)), 0600); err != nil {
		t.Fatal(err)
	}

	args := []string{"--manifest", manifestPath, "--apk", apkPath, "--output", receiptPath,
		"--enabled", "true", "--app-id", appID, "--project-id", projectID, "--cp-id", cpID}
	var stdout, stderr bytes.Buffer
	if got := RunHuawei(context.Background(), args, &stdout, &stderr); got != 0 {
		t.Fatalf("valid Huawei manifest rejected: exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	var receipt HuaweiConfigReceipt
	raw, err := os.ReadFile(receiptPath)
	if err != nil || json.Unmarshal(raw, &receipt) != nil {
		t.Fatalf("read Huawei receipt: %v", err)
	}
	hash := sha256.Sum256(apk)
	if receipt != (HuaweiConfigReceipt{SchemaVersion: 1, APKSHA256: hex.EncodeToString(hash[:]), Enabled: true, AppID: appID, ProjectID: projectID, CPID: cpID}) {
		t.Fatalf("receipt does not bind exact app/project/client IDs and APK: %+v", receipt)
	}

	verify := func(apkPath, app, project, cp string) int {
		return RunHuawei(context.Background(), []string{"--receipt", receiptPath, "--apk", apkPath,
			"--enabled", "true", "--app-id", app, "--project-id", project, "--cp-id", cp}, io.Discard, io.Discard)
	}
	if got := verify(apkPath, appID, projectID, cpID); got != 0 {
		t.Fatalf("matching receipt rejected: exit=%d", got)
	}

	otherAPK := filepath.Join(dir, "other.apk")
	if err := os.WriteFile(otherAPK, []byte("different-synthetic-apk"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := verify(otherAPK, appID, projectID, cpID); got == 0 {
		t.Fatal("receipt accepted a different signed APK")
	}
	if got := verify(apkPath, appID, "99999", cpID); got == 0 {
		t.Fatal("receipt accepted a different Huawei project ID")
	}
	if got := verify(apkPath, "99999", projectID, cpID); got == 0 {
		t.Fatal("receipt accepted a different Huawei app ID")
	}
	if got := verify(apkPath, appID, projectID, "99999"); got == 0 {
		t.Fatal("receipt accepted a different Huawei CP/client ID")
	}
}

func TestRunHuaweiDisabledManifestRequiresEmptyHuaweiIDs(t *testing.T) {
	dir := t.TempDir()
	apkPath := filepath.Join(dir, "dev.apk")
	manifestPath := filepath.Join(dir, "dev-manifest.txt")
	receiptPath := filepath.Join(dir, "dev-huawei-receipt.json")
	if err := os.WriteFile(apkPath, []byte("synthetic-dev-apk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(huaweiManifestDump(false, "", "", "")), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--manifest", manifestPath, "--apk", apkPath, "--output", receiptPath, "--enabled", "false"}
	if got := RunHuawei(context.Background(), args, io.Discard, io.Discard); got != 0 {
		t.Fatalf("disabled manifest with empty IDs rejected: exit=%d", got)
	}
	var receipt HuaweiConfigReceipt
	raw, err := os.ReadFile(receiptPath)
	if err != nil || json.Unmarshal(raw, &receipt) != nil {
		t.Fatalf("read disabled receipt: %v", err)
	}
	if receipt.Enabled || receipt.AppID != "" || receipt.ProjectID != "" || receipt.CPID != "" {
		t.Fatalf("disabled receipt retained Huawei identity: %+v", receipt)
	}
	if got := RunHuawei(context.Background(), []string{"--manifest", manifestPath, "--apk", apkPath, "--output", receiptPath,
		"--enabled", "false", "--app-id", "11001"}, io.Discard, io.Discard); got == 0 {
		t.Fatal("disabled/dev manifest accepted a configured Huawei identity")
	}
}

func TestRunHuaweiRejectsMissingOrDuplicateManifestMetadata(t *testing.T) {
	dir := t.TempDir()
	apkPath := filepath.Join(dir, "release.apk")
	manifestPath := filepath.Join(dir, "manifest.txt")
	if err := os.WriteFile(apkPath, []byte("synthetic-apk"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := huaweiManifestDump(true, "11001", "22002", "33003")
	for _, tc := range []struct{ name, dump string }{
		{name: "missing project metadata", dump: strings.Replace(valid, huaweiMetadataLine("project_id", "id=22002"), "", 1)},
		{name: "duplicate app metadata", dump: valid + huaweiMetadataLine("app_id", "id=11001")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(manifestPath, []byte(tc.dump), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--manifest", manifestPath, "--apk", apkPath, "--output", filepath.Join(dir, "receipt.json"),
				"--enabled", "true", "--app-id", "11001", "--project-id", "22002", "--cp-id", "33003"}
			if got := RunHuawei(context.Background(), args, io.Discard, io.Discard); got == 0 {
				t.Fatal("invalid Huawei manifest accepted")
			}
		})
	}
}

func huaweiMetadataLine(name, value string) string {
	return "E: meta-data (line=1)\n A: android:name(0x01010003)=\"com.heartbeatchecker.push.huawei." + name + "\"\n A: android:value(0x01010024)=\"" + value + "\"\n"
}

func huaweiManifestDump(enabled bool, app, project, cp string) string {
	flag := "false"
	if enabled {
		flag = "true"
	}
	return huaweiMetadataLine("enabled", flag) + huaweiMetadataLine("app_id", "id="+app) +
		huaweiMetadataLine("project_id", "id="+project) + huaweiMetadataLine("cp_id", "id="+cp) +
		"E: meta-data (line=1)\n A: android:name(0x01010003)=\"com.huawei.hms.client.appid\"\n A: android:value(0x01010024)=\"appid=" + app + "\"\n"
}
