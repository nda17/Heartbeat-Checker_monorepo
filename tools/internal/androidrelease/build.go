package androidrelease

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunBuild replaces the workflow's public manifest construction only. Signing
// and apksigner/aapt verification still run in the existing Android workflow.
func RunBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("android-manifest", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var apk, signature, badging, output, source, fingerprint, version string
	var code int
	for key, dst := range map[string]*string{"apk": &apk, "signature": &signature, "badging": &badging, "output": &output, "source-sha": &source, "fingerprint": &fingerprint, "version-name": &version} {
		f.StringVar(dst, key, "", "")
	}
	f.IntVar(&code, "version-code", 0, "")
	invalid := func() int { fmt.Fprintln(stderr, "Signed Android metadata construction failed"); return 1 }
	if f.Parse(args) != nil || f.NArg() != 0 || code <= 0 || !matches(`^[1-9][0-9]*\.[0-9]+\.[0-9]+$`, version) || !matches(`^[0-9a-f]{40}$`, source) || !matches(`^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$`, fingerprint) {
		return invalid()
	}
	read := func(path string) ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return deploydata.ReadBounded(f, 256<<10)
	}
	sig, err := read(signature)
	if err != nil {
		return invalid()
	}
	certificates := regexp.MustCompile(`(?m)^Signer #[0-9]+ certificate SHA-256 digest: ([a-fA-F0-9]{64})$`).FindAllSubmatch(sig, -1)
	if len(certificates) != 1 {
		return invalid()
	}
	hex := strings.ToUpper(string(certificates[0][1]))
	pairs := make([]string, 32)
	for i := range pairs {
		pairs[i] = hex[i*2 : i*2+2]
	}
	if strings.Join(pairs, ":") != fingerprint {
		return invalid()
	}
	badge, err := read(badging)
	if err != nil || strings.Contains(string(badge), "application-debuggable") {
		return invalid()
	}
	info := regexp.MustCompile(`(?m)^package: name='([^']+)' versionCode='([^']+)' versionName='([^']+)'`).FindSubmatch(badge)
	if len(info) != 4 || string(info[1]) != "ru.heartbeatchecker.app" || string(info[2]) != strconv.Itoa(code) || string(info[3]) != version {
		return invalid()
	}
	i, err := os.Lstat(apk)
	if err != nil || !i.Mode().IsRegular() || i.Size() <= 0 || i.Size() >= 100<<20 {
		return invalid()
	}
	file, err := os.Open(apk)
	if err != nil {
		return invalid()
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return invalid()
	}
	m := Manifest{SchemaVersion: 1, PackageName: "ru.heartbeatchecker.app", VersionName: version, VersionCode: code, SourceSHA: source, ReleasedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"), SizeBytes: i.Size(), SHA256: fmt.Sprintf("%x", hash.Sum(nil)), SigningCertificateSHA256: fingerprint}
	m.DownloadURL = PublicBase + m.ObjectKey()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil || os.WriteFile(output, append(b, '\n'), 0600) != nil {
		return invalid()
	}
	if _, err := Validate(ctx, output, Options{SourceSHA: source, Fingerprint: fingerprint, APKPath: apk}); err != nil {
		return invalid()
	}
	fmt.Fprintf(stdout, "%x\n", sha256.Sum256(append(b, '\n')))
	return 0
}
