package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

func (r *Runner) EncryptBundle(ctx context.Context, dump, destination string, metadata State, keys map[string]string, identity string) error {
	recipient := r.Getenv("BACKUP_AGE_RECIPIENT")
	if !regexp.MustCompile(`^age1[0-9a-z]{58}$`).MatchString(recipient) {
		return failure("invalid_age_recipient")
	}
	if _, err := r.Command(ctx, []string{"age-keygen", "-o", identity}, nil, nil); err != nil {
		return err
	}
	ephemeral, err := r.Command(ctx, []string{"age-keygen", "-y", identity}, nil, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 1200*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "age", "-r", recipient, "-r", strings.TrimSpace(string(ephemeral)), "-o", destination)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		return failure("encryption_failed")
	}
	if err = cmd.Start(); err != nil {
		return failure("encryption_failed")
	}
	archive := tar.NewWriter(input)
	writeErr := func() error {
		f, e := os.Open(dump)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		header, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		header.Name = "database.dump"
		header.Mode = 0600
		if e = archive.WriteHeader(header); e != nil {
			return e
		}
		if _, e = io.Copy(archive, f); e != nil {
			return e
		}
		for _, entry := range []struct {
			name  string
			value any
		}{{"recovery-keys.json", keys}, {"recovery.json", metadata}} {
			body, e := marshal(entry.value)
			if e != nil {
				return e
			}
			if e = archive.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(body)), Mode: 0600}); e != nil {
				return e
			}
			if _, e = archive.Write(body); e != nil {
				return e
			}
		}
		return archive.Close()
	}()
	closeErr := input.Close()
	if writeErr != nil || closeErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return failure("encryption_failed")
	}
	if err = cmd.Wait(); err != nil {
		return failure("encryption_failed")
	}
	return nil
}
func jsonEqual(a, b any) bool {
	x, _ := marshal(a)
	y, _ := marshal(b)
	var left, right any
	if json.Unmarshal(x, &left) != nil || json.Unmarshal(y, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
func recoveryNames(metadata State) ([]string, bool) {
	raw, ok := metadata["recovery_key_names"]
	if !ok {
		return nil, false
	}
	encoded, _ := json.Marshal(raw)
	var names []string
	if json.Unmarshal(encoded, &names) != nil {
		return nil, false
	}
	return names, reflect.DeepEqual(names, LegacyRecoveryKeys) || reflect.DeepEqual(names, RecoveryKeys)
}

func decodeSingleJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return failure("unexpected_trailing_json")
	}
	return nil
}

func (r *Runner) RestoreDownloaded(ctx context.Context, ciphertext, identity, scratch string, metadata State, keys map[string]string) (migrations []any, err error) {
	restoredTar := filepath.Join(scratch, "downloaded.tar")
	if _, err = r.Command(ctx, []string{"age", "-d", "-i", identity, "-o", restoredTar, ciphertext}, nil, nil); err != nil {
		return nil, err
	}
	file, err := os.Open(restoredTar)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	archive := tar.NewReader(file)
	seen := map[string]bool{}
	var restoredMetadata State
	var restoredKeys map[string]string
	dump := filepath.Join(scratch, "downloaded.dump")
	for {
		header, e := archive.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, e
		}
		if seen[header.Name] || !header.FileInfo().Mode().IsRegular() {
			return nil, failure("unexpected_recovery_bundle")
		}
		seen[header.Name] = true
		switch header.Name {
		case "recovery.json":
			if e = decodeSingleJSON(archive, &restoredMetadata); e != nil {
				return nil, failure("recovery_metadata_mismatch")
			}
		case "recovery-keys.json":
			if e = decodeSingleJSON(archive, &restoredKeys); e != nil {
				return nil, failure("recovery_keys_mismatch")
			}
		case "database.dump":
			target, e := os.OpenFile(dump, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return nil, e
			}
			_, e = io.Copy(target, archive)
			closeErr := target.Close()
			if e != nil {
				return nil, e
			}
			if closeErr != nil {
				return nil, closeErr
			}
		default:
			return nil, failure("unexpected_recovery_bundle")
		}
	}
	if len(seen) != 3 {
		return nil, failure("unexpected_recovery_bundle")
	}
	if !jsonEqual(restoredMetadata, metadata) {
		return nil, failure("recovery_metadata_mismatch")
	}
	names, valid := recoveryNames(metadata)
	if !valid || len(restoredKeys) != len(names) {
		return nil, failure("recovery_keys_mismatch")
	}
	for _, name := range names {
		value, ok := restoredKeys[name]
		if !ok || value != keys[name] {
			return nil, failure("recovery_keys_mismatch")
		}
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	if err = os.Remove(restoredTar); err != nil {
		return nil, err
	}
	checksum, err := Digest(dump)
	if err != nil {
		return nil, err
	}
	if checksum != metadata["dump_sha256"] {
		return nil, failure("dump_checksum_mismatch")
	}
	cluster, socket := filepath.Join(scratch, "postgres"), filepath.Join(scratch, "socket")
	if err = os.Mkdir(socket, 0700); err != nil {
		return nil, err
	}
	env := []string{"PATH=" + r.Getenv("PATH"), "HOME=" + scratch, "LANG=C.UTF-8"}
	if _, err = r.Command(ctx, []string{"initdb", "-D", cluster, "--auth-local=trust", "--auth-host=reject", "--no-locale", "-U", "heartbeat"}, env, nil); err != nil {
		return nil, err
	}
	if _, err = r.Command(ctx, []string{"pg_ctl", "-D", cluster, "-l", filepath.Join(scratch, "postgres.log"), "-o", "-c listen_addresses='' -c unix_socket_directories=" + socket, "-w", "start"}, env, nil); err != nil {
		return nil, err
	}
	defer func() {
		_, stopErr := r.Command(context.WithoutCancel(ctx), []string{"pg_ctl", "-D", cluster, "-m", "immediate", "-w", "stop"}, env, nil)
		if stopErr != nil {
			err = stopErr
		}
	}()
	connection := []string{"-h", socket, "-U", "heartbeat"}
	if _, err = r.Command(ctx, append([]string{"createdb"}, append(connection, "restorecheck")...), env, nil); err != nil {
		return nil, err
	}
	if _, err = r.Command(ctx, append([]string{"pg_restore"}, append(connection, "-d", "restorecheck", "--exit-on-error", "--no-owner", "--no-privileges", dump)...), env, nil); err != nil {
		return nil, err
	}
	ledger, err := r.Command(ctx, append([]string{"psql"}, append(connection, "-d", "restorecheck", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT COALESCE(json_agg(row_to_json(m) ORDER BY m.version), '[]') FROM public.schema_migrations m")...), env, nil)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(ledger, &migrations); err != nil {
		return nil, err
	}
	if len(migrations) == 0 {
		return nil, failure("restored_migration_ledger_empty")
	}
	if _, ok := restoredKeys[MFARecoveryKey]; !ok {
		encrypted, e := r.EncryptedMFAData(ctx, append(connection, "-d", "restorecheck"), env)
		if e != nil {
			return nil, e
		}
		if encrypted {
			return nil, failure("missing_mfa_recovery_key")
		}
	}
	return migrations, nil
}
