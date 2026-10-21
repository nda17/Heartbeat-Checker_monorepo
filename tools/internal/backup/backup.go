// Package backup creates encrypted recovery bundles and durable delivery reports.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/s3store"
)

const Prefix = "heartbeat-checker/production/daily/"
const MFARecoveryKey = "AUTH_MFA_ENCRYPTION_KEY"

var LegacyRecoveryKeys = []string{"WEBHOOK_ENCRYPTION_KEY", "AUTH_MAIL_ENCRYPTION_KEY", "SUPPORT_ENCRYPTION_KEY"}
var RecoveryKeys = append(append([]string{}, LegacyRecoveryKeys...), MFARecoveryKey)

type State = map[string]any
type CommandFunc func(context.Context, []string, []string, io.Writer) ([]byte, error)
type Runner struct {
	Store    s3store.Store
	Getenv   func(string) string
	Now      func() time.Time
	StateDir string
	Command  CommandFunc
	Encrypt  func(context.Context, string, string, State, map[string]string, string) error
	Restore  func(context.Context, string, string, string, State, map[string]string) ([]any, error)
	Telegram HTTPClient
}

func New(store s3store.Store) *Runner {
	r := &Runner{Store: store, Getenv: os.Getenv, Now: time.Now, StateDir: os.Getenv("BACKUP_STATE_DIR"), Command: Command}
	if r.StateDir == "" {
		r.StateDir = "/state"
	}
	r.Encrypt = r.EncryptBundle
	r.Restore = r.RestoreDownloaded
	return r
}
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "run" && args[0] != "report") {
		fmt.Fprintln(stderr, "usage: hc-ops backup {run,report}")
		return 2
	}
	syscall.Umask(0077)
	store, err := s3store.New(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "Backup operation failed: "+safeCode(err))
		return 1
	}
	failed, err := New(store).Execute(ctx, args[0])
	if err != nil {
		fmt.Fprintln(stderr, "Backup operation failed: "+safeCode(err))
		return 1
	}
	if failed {
		fmt.Fprintln(stdout, "Backup/report requires retry")
		return 1
	}
	fmt.Fprintln(stdout, "Backup operation completed")
	return 0
}

type backupError string

func (e backupError) Error() string { return string(e) }
func failure(code string) error     { return backupError(code) }
func safeCode(err error) string {
	var e backupError
	if errors.As(err, &e) {
		return string(e)
	}
	switch err.Error() {
	case "invalid_s3_target", "missing_s3_credentials":
		return err.Error()
	}
	return "operation_failed"
}
func (r *Runner) timestamp() string { return r.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00") }
func moscow() *time.Location        { zone, _ := time.LoadLocation("Europe/Moscow"); return zone }
func stringify(v any) string        { s, _ := v.(string); return s }
func truth(v any) bool              { b, _ := v.(bool); return b }
func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}
func Digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
func Save(path string, value State) error {
	partial := strings.TrimSuffix(path, filepath.Ext(path)) + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(partial, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func Command(ctx context.Context, args, env []string, output io.Writer) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 1200*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	if output == nil {
		cmd.Stdout = &out
	} else {
		cmd.Stdout = output
	}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, failure("command_failed:" + filepath.Base(args[0]))
	}
	return out.Bytes(), nil
}
func (r *Runner) DatabaseEnv(raw string) ([]string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, failure("invalid_database_target")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || parsed.User == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() != "postgres" || parsed.Port() != "5432" || parsed.User.Username() != "heartbeat" || parsed.Path != "/heartbeat" || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return nil, failure("invalid_database_target")
	}
	password, _ := parsed.User.Password()
	return []string{"PATH=" + r.Getenv("PATH"), "HOME=/tmp", "LANG=C.UTF-8", "PGHOST=postgres", "PGPORT=5432", "PGUSER=heartbeat", "PGDATABASE=heartbeat", "PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=10"}, nil
}
func (r *Runner) RecoveryKeys() (map[string]string, []string, error) {
	keys := map[string]string{}
	names := []string{}
	for _, key := range RecoveryKeys {
		value := r.Getenv(key)
		if key == MFARecoveryKey && value == "" {
			continue
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(decoded) != 32 || strings.ContainsAny(value, "\r\n") {
			return nil, nil, failure("invalid_recovery_key:" + key)
		}
		keys[key] = value
		names = append(names, key)
	}
	return keys, names, nil
}
func (r *Runner) EncryptedMFAData(ctx context.Context, connection, env []string) (bool, error) {
	options := append([]string{"psql"}, connection...)
	options = append(options, "-At", "-v", "ON_ERROR_STOP=1", "-c")
	tables, err := r.Command(ctx, append(options, "SELECT to_regclass('auth.mfa_factors') IS NOT NULL, to_regclass('auth.mfa_enrollments') IS NOT NULL"), env, nil)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(tables)) {
	case "f|f":
		return false, nil
	case "t|t":
	default:
		return false, failure("incomplete_mfa_schema")
	}
	rows, err := r.Command(ctx, append(options, "SELECT EXISTS(SELECT 1 FROM auth.mfa_factors) OR EXISTS(SELECT 1 FROM auth.mfa_enrollments)"), env, nil)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(rows)) {
	case "t":
		return true, nil
	case "f":
		return false, nil
	}
	return false, failure("invalid_mfa_data_check")
}
func (r *Runner) Execute(ctx context.Context, mode string) (bool, error) {
	if err := os.MkdirAll(r.StateDir, 0700); err != nil {
		return false, err
	}
	failed := false
	if mode == "run" {
		day := r.Now().In(moscow()).Format("2006-01-02")
		path := filepath.Join(r.StateDir, day+".json")
		state := State{"date": day}
		if raw, err := os.ReadFile(path); err == nil {
			if err = json.Unmarshal(raw, &state); err != nil {
				return false, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err := r.RunBackup(ctx, state, path); err != nil {
			failed = true
		}
	}
	paths, err := filepath.Glob(filepath.Join(r.StateDir, "????-??-??.json"))
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		var state State
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		if err = json.Unmarshal(raw, &state); err != nil {
			return false, err
		}
		if state["status"] == "running" {
			state["status"] = "failed"
			state["error"] = "interrupted"
			state["failed_at"] = r.timestamp()
			state["report_sent"] = false
			if err = Save(path, state); err != nil {
				return false, err
			}
		}
		sent, err := r.Report(ctx, state, path)
		if err != nil {
			return false, err
		}
		if !sent {
			failed = true
		}
		if truth(state["report_sent"]) && stringify(state["date"]) < r.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02") {
			if err = os.Remove(path); err != nil {
				return false, err
			}
			if err = os.Remove(filepath.Join(r.StateDir, stringify(state["date"])+".tar.age")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
	}
	return failed, nil
}
func (r *Runner) RunBackup(ctx context.Context, state State, path string) (err error) {
	if state["status"] == "success" {
		return nil
	}
	state["status"] = "running"
	state["phase"] = "dump"
	state["started_at"] = r.timestamp()
	state["report_sent"] = false
	if err = Save(path, state); err != nil {
		return err
	}
	encrypted := filepath.Join(r.StateDir, stringify(state["date"])+".tar.age")
	partial := strings.TrimSuffix(encrypted, ".age") + ".partial"
	defer func() {
		if err != nil {
			code := safeCode(err)
			state["status"] = "failed"
			state["error"] = code
			state["failed_at"] = r.timestamp()
			state["report_sent"] = false
			if saveErr := Save(path, state); saveErr != nil {
				err = saveErr
			} else {
				err = failure(code)
			}
			_ = os.Remove(partial)
		}
	}()
	scratch, err := os.MkdirTemp("/tmp", "heartbeat-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	keys, names, err := r.RecoveryKeys()
	if err != nil {
		return err
	}
	env, err := r.DatabaseEnv(r.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	if _, ok := keys[MFARecoveryKey]; !ok {
		encryptedRows, e := r.EncryptedMFAData(ctx, nil, env)
		if e != nil {
			return e
		}
		if encryptedRows {
			return failure("missing_mfa_recovery_key")
		}
	}
	dump := filepath.Join(scratch, "database.dump")
	target, err := os.OpenFile(dump, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = r.Command(ctx, []string{"pg_dump", "--format=custom", "--no-owner", "--no-privileges"}, env, target)
	closeErr := target.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(dump)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return failure("empty_dump")
	}
	dumpHash, err := Digest(dump)
	if err != nil {
		return err
	}
	version, err := r.Command(ctx, []string{"pg_dump", "--version"}, nil, nil)
	if err != nil {
		return err
	}
	metadata := State{"format_version": 1, "database": "heartbeat", "created_at": state["started_at"], "source_sha": r.Getenv("BACKUP_SOURCE_SHA"), "dump_sha256": dumpHash, "recovery_key_names": names, "postgres_version": strings.TrimSpace(string(version))}
	state["phase"] = "encrypt"
	if err = Save(path, state); err != nil {
		return err
	}
	identity := filepath.Join(scratch, "ephemeral.agekey")
	if err = r.Encrypt(ctx, dump, partial, metadata, keys, identity); err != nil {
		return err
	}
	if err = os.Rename(partial, encrypted); err != nil {
		return err
	}
	if err = os.Remove(dump); err != nil {
		return err
	}
	checksum, err := Digest(encrypted)
	if err != nil {
		return err
	}
	info, err = os.Stat(encrypted)
	if err != nil {
		return err
	}
	key := Prefix + stringify(state["date"]) + "/bundle.tar.age"
	state["phase"] = "upload"
	state["object_key"] = key
	state["ciphertext_sha256"] = checksum
	state["bytes"] = info.Size()
	if err = Save(path, state); err != nil {
		return err
	}
	manifestKey := Prefix + stringify(state["date"]) + "/manifest.json"
	if err = r.Store.Delete(ctx, manifestKey); err != nil {
		return err
	}
	if err = r.Store.Upload(ctx, key, encrypted, s3store.PutOptions{Metadata: map[string]string{"sha256": checksum}, ContentType: "application/octet-stream"}); err != nil {
		return err
	}
	state["phase"] = "download_and_restore"
	if err = Save(path, state); err != nil {
		return err
	}
	downloaded := filepath.Join(scratch, "downloaded.age")
	if err = r.download(ctx, key, downloaded); err != nil {
		return err
	}
	downloadHash, err := Digest(downloaded)
	if err != nil {
		return err
	}
	if downloadHash != checksum {
		return failure("s3_checksum_mismatch")
	}
	migrations, err := r.Restore(ctx, downloaded, identity, scratch, metadata, keys)
	if err != nil {
		return err
	}
	manifest := State{}
	for k, v := range metadata {
		manifest[k] = v
	}
	manifest["object_key"] = key
	manifest["ciphertext_sha256"] = checksum
	manifest["bytes"] = state["bytes"]
	manifest["restore_checked_at"] = r.timestamp()
	manifest["migrations"] = migrations
	body, err := marshal(manifest)
	if err != nil {
		return err
	}
	if err = r.Store.Put(ctx, manifestKey, bytes.NewReader(body), int64(len(body)), s3store.PutOptions{ContentType: "application/json"}); err != nil {
		return err
	}
	var received bytes.Buffer
	if err = r.Store.Get(ctx, manifestKey, &received); err != nil {
		return err
	}
	if !bytes.Equal(received.Bytes(), body) {
		return failure("s3_manifest_mismatch")
	}
	state["status"] = "success"
	state["phase"] = "complete"
	state["completed_at"] = r.timestamp()
	state["manifest_key"] = manifestKey
	state["migrations"] = len(migrations)
	state["report_sent"] = false
	delete(state, "error")
	if err = Save(path, state); err != nil {
		return err
	}
	if err = os.Remove(encrypted); err != nil {
		return err
	}
	if pruneErr := r.Prune(ctx); pruneErr != nil {
		state["retention_warning"] = true
		state["report_sent"] = false
		if err = Save(path, state); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) download(ctx context.Context, key, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	err = r.Store.Get(ctx, key, f)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (r *Runner) Prune(ctx context.Context) error {
	cutoff := r.Now().In(moscow()).AddDate(0, 0, -14).Format("2006-01-02")
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(Prefix) + `(\d{4}-\d{2}-\d{2})/(bundle\.tar\.age|manifest\.json)$`)
	keys, err := r.Store.List(ctx, Prefix, 0)
	if err != nil {
		return err
	}
	for _, key := range keys {
		match := pattern.FindStringSubmatch(key)
		if match != nil && match[1] < cutoff {
			if err = r.Store.Delete(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}
