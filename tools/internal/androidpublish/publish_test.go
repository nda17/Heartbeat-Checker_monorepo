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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/androidrelease"
	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/s3store"
)

var publisherAPK = []byte("synthetic signed apk")
var publisherRecovery = []byte("age-encryption.org/v1\nsynthetic encrypted signing archive")

const publisherSourceSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const publisherFingerprint = "AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB"

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func publisherManifest() androidrelease.Manifest {
	return androidrelease.Manifest{SchemaVersion: 1, PackageName: "ru.heartbeatchecker.app", VersionName: "1.2.3", VersionCode: 42, SourceSHA: publisherSourceSHA, ReleasedAt: "2026-10-19T12:34:56Z", SizeBytes: int64(len(publisherAPK)), SHA256: digest(publisherAPK), SigningCertificateSHA256: publisherFingerprint, DownloadURL: androidrelease.PublicBase + "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk"}
}

type fakePublicationStore struct {
	objects       map[string][]byte
	metadata      map[string]map[string]string
	acl           map[string]bool
	puts          []string
	putOptions    []s3store.PutOptions
	aclUpdates    []string
	privateBucket bool
	backupKeys    []string
	backupGets    int
}

func newPublicationStore() *fakePublicationStore {
	return &fakePublicationStore{objects: map[string][]byte{}, metadata: map[string]map[string]string{}, acl: map[string]bool{}, privateBucket: true, backupKeys: []string{"heartbeat-checker/production/daily/2026-10-20/bundle.tar.age"}}
}
func (s *fakePublicationStore) Put(_ context.Context, key string, r io.Reader, size int64, o s3store.PutOptions) error {
	if o.Immutable {
		if _, exists := s.objects[key]; exists {
			return errors.New("immutable write precondition failed")
		}
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(body)) != size {
		return fmt.Errorf("size mismatch")
	}
	s.objects[key] = body
	s.metadata[key] = cloneMetadata(o.Metadata)
	s.acl[key] = o.ACL != "public-read"
	s.puts = append(s.puts, key)
	s.putOptions = append(s.putOptions, o)
	return nil
}
func (s *fakePublicationStore) Upload(context.Context, string, string, s3store.PutOptions) error {
	return errors.New("unexpected upload")
}
func (s *fakePublicationStore) Get(_ context.Context, key string, w io.Writer) error {
	if strings.Contains(key, "production/daily/") {
		s.backupGets++
	}
	b, ok := s.objects[key]
	if !ok {
		if strings.Contains(key, "production/daily/") {
			_, err := w.Write([]byte("private backup"))
			return err
		}
		return s3store.ErrNotFound
	}
	_, err := w.Write(b)
	return err
}
func (s *fakePublicationStore) Head(_ context.Context, key string) (s3store.Object, error) {
	b, ok := s.objects[key]
	if !ok {
		return s3store.Object{}, s3store.ErrNotFound
	}
	return s3store.Object{Size: int64(len(b)), Metadata: cloneMetadata(s.metadata[key])}, nil
}
func (s *fakePublicationStore) Delete(context.Context, string) error { return nil }
func (s *fakePublicationStore) List(_ context.Context, prefix string, _ int32) ([]string, error) {
	if strings.Contains(prefix, "production/daily") {
		return append([]string(nil), s.backupKeys...), nil
	}
	return nil, nil
}
func (s *fakePublicationStore) PrivateACL(_ context.Context, key string) (bool, error) {
	if key == "" {
		return s.privateBucket, nil
	}
	return s.acl[key], nil
}
func (s *fakePublicationStore) PublicRead(_ context.Context, key string) error {
	s.aclUpdates = append(s.aclUpdates, key)
	s.acl[key] = false
	return nil
}
func (*fakePublicationStore) Presign(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unexpected presign")
}
func cloneMetadata(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

type publicationHTTP struct {
	urls            []string
	privateIsPublic bool
	publicAPK       []byte
}

func (h *publicationHTTP) Do(req *http.Request) (*http.Response, error) {
	h.urls = append(h.urls, req.URL.String())
	url := req.URL.String()
	status := http.StatusOK
	contentType := "application/vnd.android.package-archive"
	body := publisherAPK
	if strings.Contains(url, "?list-type=2") || strings.Contains(url, "/production/daily/") || strings.Contains(url, "/android/signing/") {
		if h.privateIsPublic {
			status = http.StatusOK
		} else {
			status = http.StatusForbidden
		}
		body = []byte("private probe")
	}
	if strings.Contains(url, "/android/1.2.3/") {
		body = h.publicAPK
		if body == nil {
			body = publisherAPK
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

func newPublisher(t *testing.T, store *fakePublicationStore, httpClient *publicationHTTP) Publisher {
	t.Helper()
	dir := t.TempDir()
	manifest := publisherManifest()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "release.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "Heartbeat-Checker.apk"), publisherAPK, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "recovery.tar.age"), publisherRecovery, 0600); err != nil {
		t.Fatal(err)
	}
	return Publisher{Store: store, HTTP: httpClient, Directory: dir}
}

func TestPublishCreatesImmutableAPKAndPrivateRecoveryAndProvesStorageDenial(t *testing.T) {
	store := newPublicationStore()
	httpClient := &publicationHTTP{}
	publisher := newPublisher(t, store, httpClient)
	var output bytes.Buffer
	err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), &output)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 2 {
		t.Fatalf("expected recovery and APK immutable writes, got %v", store.puts)
	}
	if !strings.Contains(store.puts[0], "/android/signing/") || !strings.Contains(store.puts[1], "/android/1.2.3/") {
		t.Fatalf("unexpected immutable object order: %v", store.puts)
	}
	if store.acl[store.puts[0]] != true || store.acl[store.puts[1]] != false {
		t.Fatalf("recovery/APK ACL mismatch: %#v", store.acl)
	}
	if len(store.putOptions) != 2 || !store.putOptions[0].Immutable || store.putOptions[0].ACL != "private" || !store.putOptions[1].Immutable || store.putOptions[1].ACL != "public-read" {
		t.Fatalf("object writes did not require immutable private-recovery/public-APK options: %#v", store.putOptions)
	}
	if store.putOptions[0].Metadata["sha256"] != digest(publisherRecovery) || store.putOptions[1].Metadata["source-sha"] != publisherSourceSHA || store.putOptions[1].Metadata["sha256"] != digest(publisherAPK) || store.putOptions[1].Metadata["version-code"] != "42" {
		t.Fatalf("object writes lack verified provenance metadata: %#v", store.putOptions)
	}
	if store.putOptions[0].ContentType != "application/octet-stream" || store.putOptions[1].ContentType != "application/vnd.android.package-archive" || !strings.Contains(store.putOptions[1].CacheControl, "immutable") {
		t.Fatalf("object writes lack required content metadata: %#v", store.putOptions)
	}
	if len(store.aclUpdates) != 0 {
		t.Fatalf("new immutable APK unexpectedly needed ACL repair: %v", store.aclUpdates)
	}
	for _, fragment := range []string{"?list-type=2", "/production/daily/", "/android/signing/"} {
		found := false
		for _, u := range httpClient.urls {
			if strings.Contains(u, fragment) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing anonymous privacy probe %s in %v", fragment, httpClient.urls)
		}
	}
	var receipt map[string]any
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["status"] != "published" || receipt["anonymousSigningRecovery"] != "403" {
		t.Fatalf("publication receipt lacks evidence: %#v", receipt)
	}
}

func TestPublishRejectsPublicBucketOrPrivateBackupExposureBeforeUpload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakePublicationStore, *publicationHTTP)
	}{{"public bucket", func(s *fakePublicationStore, _ *publicationHTTP) { s.privateBucket = false }}, {"public backup", func(_ *fakePublicationStore, h *publicationHTTP) { h.privateIsPublic = true }}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPublicationStore()
			httpClient := &publicationHTTP{}
			tc.configure(store, httpClient)
			publisher := newPublisher(t, store, httpClient)
			if err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), io.Discard); err == nil {
				t.Fatal("public storage exposure accepted")
			}
			if len(store.puts) != 0 {
				t.Fatalf("uploaded after privacy check failure: %v", store.puts)
			}
		})
	}
}

func TestPublishNeverOverwritesExistingAPKWithDifferentProvenanceOrBytes(t *testing.T) {
	key := "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk"
	for _, tc := range []struct {
		name     string
		metadata map[string]string
		body     []byte
		want     string
	}{{"different provenance", map[string]string{"source-sha": strings.Repeat("b", 40)}, publisherAPK, "immutable_version_provenance_mismatch"}, {"different bytes", map[string]string{"source-sha": publisherSourceSHA, "sha256": digest(publisherAPK), "signing-certificate-sha256": publisherFingerprint, "version-code": "42"}, bytes.Repeat([]byte("x"), len(publisherAPK)), "existing_apk_checksum_mismatch"}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPublicationStore()
			store.objects[key] = tc.body
			store.metadata[key] = tc.metadata
			store.acl[key] = false
			publisher := newPublisher(t, store, &publicationHTTP{})
			err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), io.Discard)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("expected %s, got %v", tc.want, err)
			}
			for _, written := range store.puts {
				if written == key {
					t.Fatal("existing APK was overwritten")
				}
			}
			for _, updated := range store.aclUpdates {
				if updated == key {
					t.Fatal("ACL updated despite existing content mismatch")
				}
			}
		})
	}
}

func TestPublishExistingRecoveryProvenanceBlocksAPKAndPublicChecksumMustMatch(t *testing.T) {
	store := newPublicationStore()
	recoveryKey := "heartbeat-checker/android/signing/" + strings.ToLower(strings.ReplaceAll(publisherFingerprint, ":", "")) + "/recovery.tar.age"
	store.objects[recoveryKey] = publisherRecovery
	store.metadata[recoveryKey] = map[string]string{"sha256": strings.Repeat("0", 64), "signing-certificate-sha256": publisherFingerprint}
	store.acl[recoveryKey] = true
	publisher := newPublisher(t, store, &publicationHTTP{})
	if err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), io.Discard); err == nil || err.Error() != "immutable_signing_archive_differs" {
		t.Fatalf("unexpected recovery provenance error: %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("APK published despite recovery conflict: %v", store.puts)
	}
	store = newPublicationStore()
	httpClient := &publicationHTTP{publicAPK: []byte("wrong public bytes")}
	publisher = newPublisher(t, store, httpClient)
	err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), io.Discard)
	if err == nil || err.Error() != "public_apk_checksum_mismatch" {
		t.Fatalf("unexpected public APK verification result: %v", err)
	}
}

func TestPublishRejectsPublicRecoveryACLBeforeAPKPublication(t *testing.T) {
	store := newPublicationStore() // mark the newly uploaded recovery archive public through the ACL seam.
	// This store reports a public recovery ACL only after it has been uploaded.
	store.puts = nil
	store.acl = map[string]bool{}
	publisher := newPublisher(t, store, &publicationHTTP{})
	// The injected store uses a test wrapper below to model an ACL that changed after write.
	publication := publisher.Store.(*fakePublicationStore)
	_ = publication
	// Public recovery is exercised by seeding an immutable object with the wrong ACL.
	key := "heartbeat-checker/android/signing/" + strings.ToLower(strings.ReplaceAll(publisherFingerprint, ":", "")) + "/recovery.tar.age"
	store.objects[key] = publisherRecovery
	store.metadata[key] = map[string]string{"sha256": digest(publisherRecovery), "signing-certificate-sha256": publisherFingerprint}
	store.acl[key] = false
	if err := publisher.Publish(context.Background(), publisherSourceSHA, publisherFingerprint, digest(publisherRecovery), io.Discard); err == nil || err.Error() != "signing_archive_acl_must_remain_private" {
		t.Fatalf("public recovery ACL was accepted: %v", err)
	}
	for _, written := range store.puts {
		if strings.Contains(written, "/android/1.2.3/") {
			t.Fatal("APK published while recovery ACL was public")
		}
	}
}

func TestLimitedBufferEnforcesLimitThroughIOCopy(t *testing.T) {
	buffer := &limitedBuffer{Limit: 5}
	n, err := io.Copy(buffer, strings.NewReader("123456"))
	if err == nil || n > 5 || len(buffer.Bytes()) > 5 {
		t.Fatalf("io.Copy bypassed the object-size limit: copied=%d buffered=%d err=%v", n, len(buffer.Bytes()), err)
	}
}
