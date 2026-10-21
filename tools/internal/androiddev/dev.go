// Package androiddev validates and privately publishes an immutable development APK.
package androiddev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const apkName = "Heartbeat-Checker-dev.apk"
const maxAPKBytes = 150 << 20

var hex40 = regexp.MustCompile(`^[a-f0-9]{40}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var fingerprintPattern = regexp.MustCompile(`^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$`)
var packagePattern = regexp.MustCompile(`(?m)^package: name='([^']+)' versionCode='([^']+)' versionName='([^']+)'`)
var signerPattern = regexp.MustCompile(`(?m)^Signer #[0-9]+ certificate SHA-256 digest: ([a-fA-F0-9]{64})$`)

type Manifest struct {
	SchemaVersion            int    `json:"schemaVersion"`
	PackageName              string `json:"packageName"`
	VersionCode              int    `json:"versionCode"`
	VersionName              string `json:"versionName"`
	SourceSHA                string `json:"sourceSha"`
	BuiltAt                  string `json:"builtAt"`
	SizeBytes                int64  `json:"sizeBytes"`
	SHA256                   string `json:"sha256"`
	SigningCertificateSHA256 string `json:"signingCertificateSha256"`
}

func valid(m Manifest) bool {
	built, err := time.Parse(time.RFC3339, m.BuiltAt)
	return m.SchemaVersion == 1 && m.PackageName == "ru.heartbeatchecker.app.dev" &&
		m.VersionCode > 0 && len(m.VersionName) > 0 && len(m.VersionName) <= 64 &&
		!strings.ContainsAny(m.VersionName, " \t\r\n/\\\x00") &&
		hex40.MatchString(m.SourceSHA) && hex64.MatchString(m.SHA256) &&
		fingerprintPattern.MatchString(m.SigningCertificateSHA256) &&
		m.SizeBytes > 0 && m.SizeBytes < maxAPKBytes && err == nil && built.UTC().Format(time.RFC3339) == m.BuiltAt
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("oversized input")
	}
	return data, nil
}

func fileHash(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= maxAPKBytes {
		return 0, "", errors.New("invalid APK file")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(digest.Sum(nil)), nil
}

func fingerprint(raw string) string {
	hexValue := strings.ToUpper(raw)
	parts := make([]string, 32)
	for i := range parts {
		parts[i] = hexValue[2*i : 2*i+2]
	}
	return strings.Join(parts, ":")
}

func Build(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	_ = ctx
	f := flag.NewFlagSet("android-dev-manifest", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var apk, signature, badging, source, expectedFingerprint, output string
	f.StringVar(&apk, "apk", "", "")
	f.StringVar(&signature, "signature", "", "")
	f.StringVar(&badging, "badging", "", "")
	f.StringVar(&source, "source-sha", "", "")
	f.StringVar(&expectedFingerprint, "fingerprint", "", "")
	f.StringVar(&output, "output", "", "")
	fail := func() int { fmt.Fprintln(stderr, "Invalid Android dev APK or provenance"); return 1 }
	if f.Parse(args) != nil || f.NArg() != 0 || !hex40.MatchString(source) || !fingerprintPattern.MatchString(expectedFingerprint) || output == "" {
		return fail()
	}
	sig, err := readBounded(signature, 256<<10)
	if err != nil {
		return fail()
	}
	matches := signerPattern.FindAllSubmatch(sig, -1)
	if len(matches) != 1 || fingerprint(string(matches[0][1])) != expectedFingerprint {
		return fail()
	}
	badge, err := readBounded(badging, 256<<10)
	if err != nil {
		return fail()
	}
	info := packagePattern.FindSubmatch(badge)
	if len(info) != 4 || string(info[1]) != "ru.heartbeatchecker.app.dev" {
		return fail()
	}
	code, err := strconv.Atoi(string(info[2]))
	if err != nil || code <= 0 {
		return fail()
	}
	size, hash, err := fileHash(apk)
	if err != nil {
		return fail()
	}
	m := Manifest{SchemaVersion: 1, PackageName: "ru.heartbeatchecker.app.dev", VersionCode: code, VersionName: string(info[3]), SourceSHA: source, BuiltAt: time.Now().UTC().Format(time.RFC3339), SizeBytes: size, SHA256: hash, SigningCertificateSHA256: expectedFingerprint}
	if !valid(m) {
		return fail()
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil || os.WriteFile(output, append(data, '\n'), 0600) != nil {
		return fail()
	}
	fmt.Fprintln(stdout, hash)
	return 0
}

func Load(path string) (Manifest, []byte, error) {
	var m Manifest
	data, err := readBounded(path, 4096)
	if err != nil {
		return m, nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF || !valid(m) {
		return m, nil, errors.New("invalid dev manifest")
	}
	return m, data, nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func copyAPK(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, 0644)
}

func Publish(ctx context.Context, manifestPath, apkPath, root, source, signer string) (Manifest, error) {
	_ = ctx
	m, data, err := Load(manifestPath)
	if err != nil || m.SourceSHA != source || m.SigningCertificateSHA256 != signer {
		return m, errors.New("dev APK provenance mismatch")
	}
	size, hash, err := fileHash(apkPath)
	if err != nil || size != m.SizeBytes || hash != m.SHA256 {
		return m, errors.New("dev APK checksum mismatch")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return m, errors.New("invalid private root")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 {
		return m, errors.New("private root is unavailable")
	}
	releases := filepath.Join(root, "releases")
	if err = os.Mkdir(releases, 0755); err != nil && !os.IsExist(err) {
		return m, err
	}
	info, err = os.Lstat(releases)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return m, errors.New("invalid releases directory")
	}
	if err = os.Chmod(releases, 0755); err != nil {
		return m, err
	}
	target := filepath.Join(releases, m.SHA256)
	if _, err = os.Lstat(target); os.IsNotExist(err) {
		staging, createErr := os.MkdirTemp(releases, ".incoming-")
		if createErr != nil {
			return m, createErr
		}
		defer os.RemoveAll(staging)
		if err = os.Chmod(staging, 0755); err != nil {
			return m, err
		}
		if err = copyAPK(apkPath, filepath.Join(staging, apkName)); err != nil {
			return m, err
		}
		copiedSize, copiedHash, hashErr := fileHash(filepath.Join(staging, apkName))
		if hashErr != nil || copiedSize != m.SizeBytes || copiedHash != m.SHA256 {
			return m, errors.New("staged dev APK checksum mismatch")
		}
		manifestFile := filepath.Join(staging, "release.json")
		if err = os.WriteFile(manifestFile, data, 0644); err != nil {
			return m, err
		}
		if err = os.Chmod(manifestFile, 0644); err != nil {
			return m, err
		}
		if err = syncDir(staging); err != nil {
			return m, err
		}
		if err = os.Rename(staging, target); err != nil {
			return m, err
		}
		if err = syncDir(releases); err != nil {
			return m, err
		}
	} else if err != nil {
		return m, err
	} else {
		info, err = os.Lstat(target)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 {
			return m, errors.New("invalid immutable dev APK directory")
		}
		for _, name := range []string{apkName, "release.json"} {
			fileInfo, statErr := os.Lstat(filepath.Join(target, name))
			if statErr != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0644 {
				return m, errors.New("invalid immutable dev APK file")
			}
		}
		stored, _, loadErr := Load(filepath.Join(target, "release.json"))
		storedSize, storedHash, hashErr := fileHash(filepath.Join(target, apkName))
		if loadErr != nil || hashErr != nil || stored != m || storedSize != m.SizeBytes || storedHash != m.SHA256 {
			return m, errors.New("immutable dev APK differs")
		}
	}
	current, err := os.CreateTemp(root, ".current-")
	if err != nil {
		return m, err
	}
	defer os.Remove(current.Name())
	if err = current.Chmod(0644); err == nil {
		_, err = current.Write(data)
	}
	if err == nil {
		err = current.Sync()
	}
	closeErr := current.Close()
	if err != nil {
		return m, err
	}
	if closeErr != nil {
		return m, closeErr
	}
	if err = os.Rename(current.Name(), filepath.Join(root, "current.json")); err != nil {
		return m, err
	}
	return m, syncDir(root)
}

func RunPublish(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 5 {
		fmt.Fprintln(stderr, "Expected manifest, APK, private root, source SHA and signer fingerprint")
		return 1
	}
	m, err := Publish(ctx, args[0], args[1], args[2], args[3], args[4])
	if err != nil {
		fmt.Fprintln(stderr, "Android dev APK publication failed")
		return 1
	}
	returnValue := map[string]any{"sourceSha": m.SourceSHA, "sha256": m.SHA256, "sizeBytes": m.SizeBytes, "versionName": m.VersionName}
	if json.NewEncoder(stdout).Encode(returnValue) != nil {
		return 1
	}
	return 0
}
