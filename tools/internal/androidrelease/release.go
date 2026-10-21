// Package androidrelease validates public metadata without storage credentials.
package androidrelease

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
	"os/exec"
	"regexp"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

const PublicBase = "https://s3.twcstorage.ru/heartbeat-data-1/"

type Manifest struct {
	SchemaVersion            int    `json:"schemaVersion"`
	PackageName              string `json:"packageName"`
	VersionCode              int    `json:"versionCode"`
	VersionName              string `json:"versionName"`
	SourceSHA                string `json:"sourceSha"`
	ReleasedAt               string `json:"releasedAt"`
	SizeBytes                int64  `json:"sizeBytes"`
	SHA256                   string `json:"sha256"`
	SigningCertificateSHA256 string `json:"signingCertificateSha256"`
	DownloadURL              string `json:"downloadUrl"`
}
type Options struct{ SourceSHA, Fingerprint, FrontendSHA, APKPath string }

func (m Manifest) ObjectKey() string {
	return "heartbeat-checker/android/" + m.VersionName + "/Heartbeat-Checker.apk"
}
func matches(pattern, value string) bool { return regexp.MustCompile(pattern).MatchString(value) }

func Validate(ctx context.Context, manifestPath string, opts Options) (Manifest, error) {
	var m Manifest
	fail := func(message string) (Manifest, error) { return Manifest{}, errors.New(message) }
	f, err := os.Open(manifestPath)
	if err != nil {
		return fail("Missing release manifest")
	}
	b, err := deploydata.ReadBounded(f, 65536)
	f.Close()
	if err != nil {
		return fail("Invalid release manifest")
	}
	value, err := deploydata.Object(b)
	if err != nil || !deploydata.Exact(value, "schemaVersion", "packageName", "versionName", "versionCode", "sourceSha", "releasedAt", "sizeBytes", "sha256", "signingCertificateSha256", "downloadUrl") {
		return fail("Invalid release fields")
	}
	if json.Unmarshal(b, &m) != nil {
		return fail("Invalid release fields")
	}
	if m.SchemaVersion != 1 || m.PackageName != "ru.heartbeatchecker.app" {
		return fail("Invalid application")
	}
	if !matches(`^[1-9][0-9]*\.[0-9]+\.[0-9]+$`, m.VersionName) {
		return fail("Invalid version")
	}
	if m.VersionCode <= 0 {
		return fail("Invalid version code")
	}
	if !matches(`^[a-f0-9]{40}$`, m.SourceSHA) {
		return fail("Invalid source SHA")
	}
	if !matches(`^[a-f0-9]{64}$`, m.SHA256) {
		return fail("Invalid APK digest")
	}
	if !matches(`^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$`, m.SigningCertificateSHA256) {
		return fail("Invalid signing fingerprint")
	}
	if !matches(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`, m.ReleasedAt) {
		return fail("Invalid release date")
	}
	if m.SizeBytes <= 0 || m.SizeBytes >= 100*1024*1024 {
		return fail("Invalid APK size")
	}
	if m.DownloadURL != PublicBase+m.ObjectKey() {
		return fail("Invalid immutable public URL")
	}
	if opts.SourceSHA != "" && m.SourceSHA != opts.SourceSHA {
		return fail("Source mismatch")
	}
	if opts.Fingerprint != "" && m.SigningCertificateSHA256 != opts.Fingerprint {
		return fail("Signing certificate mismatch")
	}
	if opts.FrontendSHA != "" {
		if !matches(`^[a-f0-9]{40}$`, opts.FrontendSHA) {
			return fail("Invalid frontend SHA")
		}
		if exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", m.SourceSHA, opts.FrontendSHA).Run() != nil {
			return fail("Android source is not a frontend ancestor")
		}
	}
	if opts.APKPath != "" {
		i, err := os.Lstat(opts.APKPath)
		if err != nil || !i.Mode().IsRegular() {
			return fail("Missing regular APK")
		}
		if i.Size() != m.SizeBytes {
			return fail("APK size mismatch")
		}
		apk, err := os.Open(opts.APKPath)
		if err != nil {
			return fail("Missing regular APK")
		}
		h := sha256.New()
		_, err = io.Copy(h, apk)
		apk.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
			return fail("APK checksum mismatch")
		}
	}
	return m, nil
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Expected release manifest")
		return 1
	}
	manifest := args[0]
	f := flag.NewFlagSet("android-release", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var o Options
	var output string
	f.StringVar(&o.SourceSHA, "source-sha", "", "")
	f.StringVar(&o.FrontendSHA, "frontend-sha", "", "")
	f.StringVar(&o.Fingerprint, "fingerprint", "", "")
	f.StringVar(&o.APKPath, "apk", "", "")
	f.StringVar(&output, "output", "", "")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || o.Fingerprint == "" {
		fmt.Fprintln(stderr, "Invalid Android release arguments")
		return 1
	}
	m, err := Validate(ctx, manifest, o)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if output != "" {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil || os.WriteFile(output, append(b, '\n'), 0600) != nil {
			fmt.Fprintln(stderr, "Cannot write release metadata")
			return 1
		}
	}
	b, err := os.ReadFile(manifest)
	if err != nil {
		fmt.Fprintln(stderr, "Cannot read release metadata")
		return 1
	}
	fmt.Fprintf(stdout, "%x\n", sha256.Sum256(b))
	return 0
}
