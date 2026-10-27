package androidrelease

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
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

// HuaweiConfigReceipt binds the inspected public manifest values to the exact
// signed APK. Promotion checks this immutable candidate without rebuilding it.
type HuaweiConfigReceipt struct {
	SchemaVersion int    `json:"schemaVersion"`
	APKSHA256     string `json:"apkSha256"`
	Enabled       bool   `json:"enabled"`
	AppID         string `json:"appId"`
	ProjectID     string `json:"projectId"`
	CPID          string `json:"cpId"`
}

func huaweiMetadata(raw []byte) (map[string]string, error) {
	result := map[string]string{}
	quoted := regexp.MustCompile(`=("(?:[^"\\]|\\.)*")`)
	metadata, name, value := false, "", ""
	hasValue := false
	flush := func() error {
		if !metadata || (!strings.HasPrefix(name, "com.heartbeatchecker.push.huawei.") && name != "com.huawei.hms.client.appid") {
			return nil
		}
		if _, exists := result[name]; exists || !hasValue {
			return deploydata.ErrInvalid
		}
		result[name] = value
		return nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "E: ") {
			if err := flush(); err != nil {
				return nil, err
			}
			metadata = strings.HasPrefix(line, "E: meta-data ")
			name, value, hasValue = "", "", false
			continue
		}
		if !metadata {
			continue
		}
		isName, isValue := strings.HasPrefix(line, "A: android:name("), strings.HasPrefix(line, "A: android:value(")
		if !isName && !isValue {
			continue
		}
		parsed := ""
		if match := quoted.FindStringSubmatch(line); len(match) == 2 {
			var err error
			parsed, err = strconv.Unquote(match[1])
			if err != nil {
				return nil, deploydata.ErrInvalid
			}
		} else if isValue && strings.HasSuffix(line, "=(type 0x12)0xffffffff") {
			parsed = "true"
		} else if isValue && strings.HasSuffix(line, "=(type 0x12)0x0") {
			parsed = "false"
		} else {
			// Resource references cannot establish the exact client identity.
			continue
		}
		if isName {
			name = parsed
		} else {
			value, hasValue = parsed, true
		}
	}
	if scanner.Err() != nil || flush() != nil {
		return nil, deploydata.ErrInvalid
	}
	return result, nil
}

// RunHuawei verifies aapt's manifest dump during build, or the APK-bound receipt
// during promotion. Its diagnostics never include configuration values.
func RunHuawei(_ context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("android-huawei", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var manifest, receipt, apk, output, enabled, app, project, cp string
	for key, dst := range map[string]*string{"manifest": &manifest, "receipt": &receipt, "apk": &apk, "output": &output, "enabled": &enabled, "app-id": &app, "project-id": &project, "cp-id": &cp} {
		f.StringVar(dst, key, "", "")
	}
	fail := func() int { fmt.Fprintln(stderr, "Android Huawei configuration verification failed"); return 1 }
	if f.Parse(args) != nil || f.NArg() != 0 || (enabled != "true" && enabled != "false") || (manifest == "") == (receipt == "") {
		return fail()
	}
	for _, id := range []string{app, project, cp} {
		if (enabled == "true" && !matches(`^[0-9]{1,32}$`, id)) || (enabled == "false" && id != "") {
			return fail()
		}
	}
	info, err := os.Lstat(apk)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() >= 150<<20 {
		return fail()
	}
	file, err := os.Open(apk)
	if err != nil {
		return fail()
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return fail()
	}
	want := HuaweiConfigReceipt{SchemaVersion: 1, APKSHA256: fmt.Sprintf("%x", hash.Sum(nil)), Enabled: enabled == "true", AppID: app, ProjectID: project, CPID: cp}
	path := manifest
	if receipt != "" {
		path = receipt
	}
	file, err = os.Open(path)
	if err != nil {
		return fail()
	}
	raw, err := deploydata.ReadBounded(file, 256<<10)
	file.Close()
	if err != nil {
		return fail()
	}
	if manifest != "" {
		metadata, err := huaweiMetadata(raw)
		if err != nil {
			return fail()
		}
		for name, value := range map[string]string{"enabled": enabled, "app_id": app, "project_id": project, "cp_id": cp} {
			actual, exists := metadata["com.heartbeatchecker.push.huawei."+name]
			if name != "enabled" {
				value = "id=" + value // Force string metadata; aapt must not truncate numeric identifiers.
			}
			if !exists || actual != value {
				return fail()
			}
		}
		if metadata["com.huawei.hms.client.appid"] != "appid="+app || output == "" {
			return fail()
		}
		encoded, err := json.MarshalIndent(want, "", "  ")
		if err != nil || os.WriteFile(output, append(encoded, '\n'), 0600) != nil {
			return fail()
		}
	} else {
		object, err := deploydata.Object(raw)
		var got HuaweiConfigReceipt
		if err != nil || !deploydata.Exact(object, "schemaVersion", "apkSha256", "enabled", "appId", "projectId", "cpId") || json.Unmarshal(raw, &got) != nil || got != want || output != "" {
			return fail()
		}
		if object["enabled"] != want.Enabled || object["appId"] != app || object["projectId"] != project || object["cpId"] != cp {
			return fail() // JSON null is not an empty identifier or a false flag.
		}
	}
	fmt.Fprintln(stdout, "Android Huawei configuration verified")
	return 0
}
