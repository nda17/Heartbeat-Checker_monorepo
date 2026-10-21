package androiddev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const testSource = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testSigner = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"

func fixture(t *testing.T, dir, version string, code int, apk []byte) (string, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	apkPath := filepath.Join(dir, "input.apk")
	signaturePath := filepath.Join(dir, "signature.txt")
	badgingPath := filepath.Join(dir, "badging.txt")
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(apkPath, apk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signaturePath, []byte("Signer #1 certificate SHA-256 digest: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badgingPath, []byte("package: name='ru.heartbeatchecker.app.dev' versionCode='"+strconv.Itoa(code)+"' versionName='"+version+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"-apk", apkPath, "-signature", signaturePath, "-badging", badgingPath, "-source-sha", testSource, "-fingerprint", testSigner, "-output", manifestPath}
	if code := Build(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("Build failed: %s", stderr.String())
	}
	return manifestPath, apkPath
}

func TestBuildRejectsWrongPackageSignerAndUnsafeVersion(t *testing.T) {
	dir := t.TempDir()
	apkPath := filepath.Join(dir, "input.apk")
	if err := os.WriteFile(apkPath, []byte("apk-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(dir, "signature.txt")
	badgingPath := filepath.Join(dir, "badging.txt")
	if err := os.WriteFile(signaturePath, []byte("Signer #1 certificate SHA-256 digest: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, packageName, version, signer string
	}{
		{"wrong package", "com.example.app", "0.28.3", testSigner},
		{"unsafe version filename", "ru.heartbeatchecker.app.dev", "../latest", testSigner},
		{"wrong signer", "ru.heartbeatchecker.app.dev", "0.28.3", strings.Repeat("00:", 31) + "00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(badgingPath, []byte("package: name='"+tc.packageName+"' versionCode='3' versionName='"+tc.version+"'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "rejected-"+strings.ReplaceAll(tc.name, " ", "-")+".json")
			var stdout, stderr bytes.Buffer
			args := []string{"-apk", apkPath, "-signature", signaturePath, "-badging", badgingPath, "-source-sha", testSource, "-fingerprint", tc.signer, "-output", out}
			if Build(context.Background(), args, &stdout, &stderr) == 0 {
				t.Fatal("invalid package, signer, or version was accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected build left a manifest behind: %v", err)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsAndInvalidProvenance(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := fixture(t, dir, "0.28.3", 3, []byte("apk-bytes"))
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown field", func(m map[string]any) { m["unexpected"] = true }},
		{"invalid source format", func(m map[string]any) { m["sourceSha"] = "g" + testSource[1:] }},
		{"bad digest format", func(m map[string]any) { m["sha256"] = strings.Repeat("0", 63) }},
		{"bad certificate", func(m map[string]any) { m["signingCertificateSha256"] = "AA:BB" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			tc.edit(value)
			changed, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Load(path); err == nil {
				t.Fatal("invalid provenance manifest accepted")
			}
		})
	}
}

func TestPublishIsAtomicImmutableAndPrivateUnderRestrictiveUmask(t *testing.T) {
	oldMask := syscall.Umask(0077)
	defer syscall.Umask(oldMask)
	work := t.TempDir()
	root := filepath.Join(work, "private")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	manifestPath, apkPath := fixture(t, work, "0.28.3", 3, []byte("first-apk-payload"))
	first, err := Publish(context.Background(), manifestPath, apkPath, root, testSource, testSigner)
	if err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(root, "current.json")
	firstCurrent, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	firstCurrentInfo, err := os.Stat(currentPath)
	if err != nil {
		t.Fatalf("stat current manifest: %v", err)
	}
	if firstCurrentInfo.Mode().Perm() != 0644 {
		t.Fatalf("current manifest mode = %v; want 0644", firstCurrentInfo.Mode().Perm())
	}
	firstRelease := filepath.Join(root, "releases", first.SHA256)
	for path, mode := range map[string]os.FileMode{
		firstRelease:                                0755,
		filepath.Join(firstRelease, apkName):        0644,
		filepath.Join(firstRelease, "release.json"): 0644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat published path %s: %v", path, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("published path %s mode = %v; want %v", path, info.Mode().Perm(), mode)
		}
	}

	if _, err := Publish(context.Background(), manifestPath, apkPath, root, strings.Repeat("b", 40), testSigner); err == nil {
		t.Fatal("wrong source provenance accepted")
	}
	if got, err := os.ReadFile(currentPath); err != nil || !bytes.Equal(got, firstCurrent) {
		t.Fatalf("rejected publication changed current.json: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".current-") {
			t.Fatalf("failed publication left temporary current file %q", entry.Name())
		}
	}

	secondManifest, secondAPK := fixture(t, filepath.Join(work, "second"), "0.28.4", 4, []byte("second-apk-payload"))
	second, err := Publish(context.Background(), secondManifest, secondAPK, root, testSource, testSigner)
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 == first.SHA256 {
		t.Fatal("fixture hashes unexpectedly match")
	}
	current, _, err := Load(currentPath)
	if err != nil || current.SHA256 != second.SHA256 {
		t.Fatalf("atomic current manifest = %#v, err = %v", current, err)
	}
	if _, err := os.Stat(filepath.Join(firstRelease, apkName)); err != nil {
		t.Fatalf("publishing a new current release removed the immutable prior release: %v", err)
	}
}

func TestFileHashMatchesSHA256AndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apk")
	data := []byte("apk-payload")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	size, digest, err := fileHash(path)
	want := sha256.Sum256(data)
	if err != nil || size != int64(len(data)) || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("fileHash = (%d, %q, %v)", size, digest, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := fileHash(link); err == nil {
		t.Fatal("APK symlink accepted")
	}
}
