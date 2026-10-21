package s3store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Set at CI build time. The live suite also has a source-only mode so CI can
// validate provenance before supplying credentials or running any S3 request.
var liveSourceSHA = "unknown"

func TestMain(m *testing.M) {
	if os.Getenv("HC_LIVE_S3_SOURCE_ONLY") == "1" {
		if len(liveSourceSHA) != 40 || !isLowerHex(liveSourceSHA) {
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, liveSourceSHA)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLiveS3Compatibility(t *testing.T) {
	if os.Getenv("HC_LIVE_S3") != "1" {
		t.Skip("set HC_LIVE_S3=1 only after validating the test binary source and checksum")
	}
	if len(liveSourceSHA) != 40 || !isLowerHex(liveSourceSHA) {
		t.Fatal("live S3 test binary lacks a valid baked source revision")
	}
	for _, name := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION", "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY"} {
		if os.Getenv(name) == "" {
			t.Fatalf("live S3 configuration missing: %s", name)
		}
	}
	client, err := New(os.Getenv)
	if err != nil {
		t.Fatal("live S3 configuration rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("could not create unique test namespace")
	}
	prefix := "ops-compat/migration/" + liveSourceSHA + "/" + hex.EncodeToString(nonce[:]) + "/"
	smallKey := prefix + "conditional.bin"
	largeKey := prefix + "multipart.bin"
	createdSmall, createdLarge := false, false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, item := range []struct {
			key     string
			created bool
		}{{smallKey, createdSmall}, {largeKey, createdLarge}} {
			if !item.created {
				continue
			}
			if err := client.Delete(cleanupCtx, item.key); err != nil {
				t.Errorf("cleanup phase failed")
				continue
			}
			if _, err := client.Head(cleanupCtx, item.key); !errors.Is(err, ErrNotFound) {
				t.Errorf("cleanup verification phase failed")
			}
		}
	}()

	small := []byte("heartbeat-checker Go S3 compatibility probe\n")
	if !liveKeyAbsent(ctx, client, smallKey) || !liveKeyAbsent(ctx, client, largeKey) {
		t.Fatal("unique test namespace collision or availability check failed")
	}
	// The service can create an object even when the client loses the response.
	// These keys are unique to this invocation and were just proven absent.
	createdSmall = true
	if err := client.Put(ctx, smallKey, bytes.NewReader(small), int64(len(small)), PutOptions{ContentType: "application/octet-stream", Metadata: map[string]string{"source-sha": liveSourceSHA}, Immutable: true}); err != nil {
		t.Fatal("small conditional create phase failed")
	}
	if err := client.Put(ctx, smallKey, bytes.NewReader([]byte("overwrite must fail")), int64(len("overwrite must fail")), PutOptions{ContentType: "application/octet-stream", Immutable: true}); !isPreconditionFailed(err) {
		t.Fatal("conditional overwrite phase did not return HTTP 412")
	}
	if err := verifyLiveObject(ctx, client, smallKey, small); err != nil {
		t.Fatal("small object read/hash verification phase failed")
	}
	if private, err := client.PrivateACL(ctx, smallKey); err != nil || !private {
		t.Fatal("small object ACL verification phase failed")
	}

	large := bytes.Repeat([]byte("hc-live-s3-multipart-"), (9*1024*1024)/len("hc-live-s3-multipart-")+1)[:9*1024*1024]
	largePath := filepath.Join(t.TempDir(), "multipart.bin")
	if err := os.WriteFile(largePath, large, 0o600); err != nil {
		t.Fatal("multipart fixture preparation phase failed")
	}
	// Upload can complete remotely while its response is lost. Track the exact
	// unique key before starting so cleanup still runs after a canceled request.
	createdLarge = true
	if err := client.Upload(ctx, largeKey, largePath, PutOptions{ContentType: "application/octet-stream", Metadata: map[string]string{"source-sha": liveSourceSHA}}); err != nil {
		t.Fatal("multipart upload phase failed")
	}
	if err := verifyLiveObject(ctx, client, largeKey, large); err != nil {
		t.Fatal("multipart read/hash verification phase failed")
	}
	if private, err := client.PrivateACL(ctx, largeKey); err != nil || !private {
		t.Fatal("multipart object ACL verification phase failed")
	}
	if err := verifyPresignedGet(ctx, largeKey, large); err != nil {
		t.Fatal("presigned GET verification phase failed")
	}
	fmt.Fprintln(os.Stdout, "live S3 compatibility phases passed")
}

func verifyLiveObject(ctx context.Context, client *Client, key string, expected []byte) error {
	info, err := client.Head(ctx, key)
	if err != nil || info.Size != int64(len(expected)) {
		return errors.New("head mismatch")
	}
	h := sha256.New()
	if err := client.Get(ctx, key, h); err != nil {
		return errors.New("get failed")
	}
	actual := hex.EncodeToString(h.Sum(nil))
	want := sha256.Sum256(expected)
	if actual != hex.EncodeToString(want[:]) {
		return errors.New("checksum mismatch")
	}
	return nil
}

func liveKeyAbsent(ctx context.Context, client *Client, key string) bool {
	_, err := client.Head(ctx, key)
	return errors.Is(err, ErrNotFound)
}

func verifyPresignedGet(ctx context.Context, key string, expected []byte) error {
	client, err := New(os.Getenv)
	if err != nil {
		return errors.New("client setup failed")
	}
	url, err := client.Presign(ctx, key, 2*time.Minute)
	if err != nil {
		return errors.New("presign failed")
	}
	// Never print or include a presigned URL in an error. Use the fixed-target
	// host returned by the SDK and avoid ambient proxy and redirect behavior.
	if !strings.HasPrefix(url, "https://s3.twcstorage.ru/") {
		return errors.New("presign target mismatch")
	}
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("request setup failed")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("unexpected response status")
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return errors.New("response read failed")
	}
	actual := hex.EncodeToString(h.Sum(nil))
	want := sha256.Sum256(expected)
	if actual != hex.EncodeToString(want[:]) {
		return errors.New("checksum mismatch")
	}
	return nil
}

func isPreconditionFailed(err error) bool {
	var response *smithyhttp.ResponseError
	return errors.As(err, &response) && response.HTTPStatusCode() == http.StatusPreconditionFailed
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
