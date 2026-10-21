// Package androidpublish publishes an immutable APK while proving private storage isolation.
package androidpublish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/androidrelease"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/s3store"
)

const failureMessage = "Android publication failed; immutable object and private-storage checks require review."

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type Publisher struct {
	Store     s3store.Store
	HTTP      HTTPClient
	Directory string
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 {
		fmt.Fprintln(stderr, failureMessage)
		return 1
	}
	store, err := s3store.New(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, failureMessage)
		return 1
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	p := Publisher{Store: store, Directory: "/release", HTTP: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("public_redirect_rejected") }}}
	if err = p.Publish(ctx, args[0], args[1], args[2], stdout); err != nil {
		fmt.Fprintln(stderr, failureMessage)
		return 1
	}
	return 0
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (p Publisher) denied(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	response, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		return errors.New("private_storage_became_public")
	}
	return nil
}
func (p Publisher) privateChecks(ctx context.Context) error {
	private, err := p.Store.PrivateACL(ctx, "")
	if err != nil {
		return err
	}
	if !private {
		return errors.New("bucket_acl_must_remain_private")
	}
	for _, address := range []string{s3store.PublicBase + "?list-type=2&max-keys=1", s3store.PublicBase + "?list-type=2&prefix=heartbeat-checker%2Fproduction%2Fdaily%2F&max-keys=1"} {
		if err = p.denied(ctx, address); err != nil {
			return err
		}
	}
	return nil
}
func (p Publisher) Publish(ctx context.Context, sourceSHA, fingerprint, recoverySHA string, stdout io.Writer) error {
	manifestPath := filepath.Join(p.Directory, "release.json")
	value, err := androidrelease.Validate(ctx, manifestPath, androidrelease.Options{SourceSHA: sourceSHA, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	apk, err := os.ReadFile(filepath.Join(p.Directory, "Heartbeat-Checker.apk"))
	if err != nil {
		return err
	}
	if int64(len(apk)) != value.SizeBytes || hash(apk) != value.SHA256 {
		return errors.New("apk_checksum_mismatch")
	}
	if err = p.privateChecks(ctx); err != nil {
		return err
	}
	backups, err := p.Store.List(ctx, "heartbeat-checker/production/daily/", 1)
	if err != nil {
		return err
	}
	if len(backups) == 0 {
		return errors.New("no_private_backup_for_isolation_check")
	}
	backupURL := s3store.PublicBase + escapeKey(backups[0])
	if err = p.denied(ctx, backupURL); err != nil {
		return err
	}
	recovery, err := os.ReadFile(filepath.Join(p.Directory, "recovery.tar.age"))
	if err != nil {
		return err
	}
	if len(recovery) == 0 || len(recovery) >= 1024*1024 || !bytes.HasPrefix(recovery, []byte("age-encryption.org/v1\n")) || hash(recovery) != recoverySHA {
		return errors.New("invalid_encrypted_signing_archive")
	}
	recoveryKey := "heartbeat-checker/android/signing/" + strings.ToLower(strings.ReplaceAll(value.SigningCertificateSHA256, ":", "")) + "/recovery.tar.age"
	recoveryMetadata := map[string]string{"sha256": recoverySHA, "signing-certificate-sha256": value.SigningCertificateSHA256}
	saved, err := p.Store.Head(ctx, recoveryKey)
	if err == nil {
		if saved.Size != int64(len(recovery)) || !maps.Equal(saved.Metadata, recoveryMetadata) {
			return errors.New("immutable_signing_archive_differs")
		}
	} else if errors.Is(err, s3store.ErrNotFound) {
		if err = p.Store.Put(ctx, recoveryKey, bytes.NewReader(recovery), int64(len(recovery)), s3store.PutOptions{ACL: "private", Immutable: true, Metadata: recoveryMetadata, ContentType: "application/octet-stream"}); err != nil {
			return err
		}
	} else {
		return err
	}
	stored := &limitedBuffer{Limit: 1024*1024 + 1}
	if err = p.Store.Get(ctx, recoveryKey, stored); err != nil {
		return err
	}
	if hash(stored.Bytes()) != recoverySHA {
		return errors.New("signing_archive_round_trip_checksum_mismatch")
	}
	private, err := p.Store.PrivateACL(ctx, recoveryKey)
	if err != nil {
		return err
	}
	if !private {
		return errors.New("signing_archive_acl_must_remain_private")
	}
	recoveryURL := s3store.PublicBase + recoveryKey
	if err = p.denied(ctx, recoveryURL); err != nil {
		return err
	}
	key := value.ObjectKey()
	metadata := map[string]string{"source-sha": value.SourceSHA, "sha256": value.SHA256, "signing-certificate-sha256": value.SigningCertificateSHA256, "version-code": fmt.Sprint(value.VersionCode)}
	existing, err := p.Store.Head(ctx, key)
	if err == nil {
		if existing.Size != int64(len(apk)) || !maps.Equal(existing.Metadata, metadata) {
			return errors.New("immutable_version_provenance_mismatch")
		}
		body := &limitedBuffer{Limit: 100*1024*1024 + 1}
		if err = p.Store.Get(ctx, key, body); err != nil {
			return err
		}
		if hash(body.Bytes()) != value.SHA256 {
			return errors.New("existing_apk_checksum_mismatch")
		}
		if err = p.Store.PublicRead(ctx, key); err != nil {
			return err
		}
	} else if errors.Is(err, s3store.ErrNotFound) {
		if err = p.Store.Put(ctx, key, bytes.NewReader(apk), int64(len(apk)), s3store.PutOptions{ACL: "public-read", Immutable: true, Metadata: metadata, ContentType: "application/vnd.android.package-archive", ContentDisposition: `attachment; filename="Heartbeat-Checker.apk"`, CacheControl: "public, max-age=31536000, immutable"}); err != nil {
			return err
		}
	} else {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, value.DownloadURL, nil)
	if err != nil {
		return err
	}
	response, err := p.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || strings.Split(response.Header.Get("Content-Type"), ";")[0] != "application/vnd.android.package-archive" {
		return errors.New("invalid_public_apk_response")
	}
	received, err := io.ReadAll(io.LimitReader(response.Body, 100*1024*1024+1))
	if err != nil {
		return err
	}
	if len(received) != len(apk) || hash(received) != value.SHA256 {
		return errors.New("public_apk_checksum_mismatch")
	}
	if err = p.privateChecks(ctx); err != nil {
		return err
	}
	if err = p.denied(ctx, backupURL); err != nil {
		return err
	}
	if err = p.denied(ctx, recoveryURL); err != nil {
		return err
	}
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"status": "published", "release": value, "manifestSha256": hash(rawManifest), "anonymousListing": "403", "anonymousBackupPrefixListing": "403", "anonymousBackupObject": "403", "anonymousSigningRecovery": "403", "signingRecoverySha256": recoverySHA})
}
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

type limitedBuffer struct {
	buffer bytes.Buffer
	Limit  int
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.Limit-b.buffer.Len() {
		return 0, errors.New("object_size_limit_exceeded")
	}
	return b.buffer.Write(data)
}
