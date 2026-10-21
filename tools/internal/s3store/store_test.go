package s3store

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type requestRecorder struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	failPart bool
	groupACL bool
}

func (r *requestRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	r.mu.Lock()
	r.requests = append(r.requests, request.Clone(request.Context()))
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	status := http.StatusOK
	headers := make(http.Header)
	responseBody := ""
	switch {
	case request.Method == http.MethodPost && request.URL.Query().Has("uploads"):
		responseBody = `<InitiateMultipartUploadResult><Bucket>heartbeat-data-1</Bucket><Key>bundle</Key><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>`
	case request.Method == http.MethodPut && request.URL.Query().Has("partNumber"):
		headers.Set("ETag", `"fixture-part-etag"`)
		if r.failPart {
			status = http.StatusInternalServerError
			responseBody = `<Error><Code>InternalError</Code><Message>fixture</Message></Error>`
		}
	case request.Method == http.MethodPost && request.URL.Query().Has("uploadId"):
		responseBody = `<CompleteMultipartUploadResult><Location>https://s3.twcstorage.ru/heartbeat-data-1/bundle</Location><Bucket>heartbeat-data-1</Bucket><Key>bundle</Key><ETag>"fixture"</ETag></CompleteMultipartUploadResult>`
	case request.Method == http.MethodDelete && request.URL.Query().Has("uploadId"):
		status = http.StatusNoContent
	case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2":
		responseBody = `<ListBucketResult><Name>heartbeat-data-1</Name><Prefix>heartbeat-checker/prefix/</Prefix><KeyCount>1</KeyCount><MaxKeys>1</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>heartbeat-checker/prefix/item.tar.age</Key><Size>12</Size></Contents></ListBucketResult>`
	case request.URL.Query().Has("acl") && request.Method == http.MethodGet:
		responseBody = `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>owner</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`
		if r.groupACL {
			responseBody = `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant></AccessControlList></AccessControlPolicy>`
		}
	case request.URL.Query().Has("acl") && request.Method == http.MethodPut:
		status = http.StatusOK
	case request.Method == http.MethodPut:
		headers.Set("ETag", `"fixture-etag"`)
	default:
		status = http.StatusNotFound
		responseBody = `<Error><Code>NoSuchKey</Code><Message>fixture</Message></Error>`
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
}

func sdkTestClient(rt http.RoundTripper) *Client {
	httpClient := &http.Client{Transport: rt}
	config := aws.Config{Region: "ru-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access-id", "fixture-secret-key", ""), HTTPClient: httpClient, Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 1 }) }}
	sdk := s3.NewFromConfig(config, func(options *s3.Options) {
		options.BaseEndpoint = aws.String("https://s3.twcstorage.ru")
		options.UsePathStyle = true
	})
	return &Client{client: sdk}
}

func TestNewUsesFixedS3TargetExplicitCredentialsAndNoAmbientProxy(t *testing.T) {
	base := map[string]string{"BACKUP_S3_ENDPOINT": "https://s3.twcstorage.ru", "BACKUP_S3_BUCKET": Bucket, "BACKUP_S3_REGION": "ru-1", "BACKUP_S3_ACCESS_KEY_ID": "synthetic-access-id", "BACKUP_S3_SECRET_ACCESS_KEY": "synthetic-secret-key"}
	getenv := func(key string) string { return base[key] }
	client, err := New(getenv)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := client.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessKeyID != "synthetic-access-id" || credential.SecretAccessKey != "synthetic-secret-key" {
		t.Fatalf("ambient or wrong credentials selected: %#v", credential)
	}
	httpClient, ok := client.client.Options().HTTPClient.(*http.Client)
	if !ok {
		t.Fatalf("unexpected HTTP client %T", client.client.Options().HTTPClient)
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("S3 transport may inherit ambient proxy: %#v", httpClient.Transport)
	}
	if err := httpClient.CheckRedirect(&http.Request{}, nil); err == nil || err.Error() != "http_redirect_rejected" {
		t.Fatalf("redirect was not blocked: %v", err)
	}
	for _, tc := range []struct{ key, value string }{{"BACKUP_S3_ENDPOINT", "http://s3.twcstorage.ru"}, {"BACKUP_S3_BUCKET", "other-bucket"}, {"BACKUP_S3_REGION", "us-east-1"}, {"BACKUP_S3_ACCESS_KEY_ID", ""}, {"BACKUP_S3_SECRET_ACCESS_KEY", ""}} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			original := base[tc.key]
			base[tc.key] = tc.value
			defer func() { base[tc.key] = original }()
			if _, err := New(getenv); err == nil {
				t.Fatal("invalid S3 configuration accepted")
			}
		})
	}
}

func TestPutSignsFixedPathStyleRequestWithImmutableAndPublicationHeaders(t *testing.T) {
	recorder := &requestRecorder{}
	client := sdkTestClient(recorder)
	key := "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk"
	body := []byte("fixture apk bytes")
	options := PutOptions{Metadata: map[string]string{"source-sha": strings.Repeat("a", 40), "sha256": strings.Repeat("b", 64)}, ContentType: "application/vnd.android.package-archive", ContentDisposition: `attachment; filename="Heartbeat-Checker.apk"`, CacheControl: "public, max-age=31536000, immutable", ACL: "public-read", Immutable: true}
	if err := client.Put(context.Background(), key, bytes.NewReader(body), int64(len(body)), options); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("expected one S3 request, got %d", len(recorder.requests))
	}
	request := recorder.requests[0]
	if request.Method != http.MethodPut || request.URL.Scheme != "https" || request.URL.Host != "s3.twcstorage.ru" || request.URL.Path != "/heartbeat-data-1/"+key {
		t.Fatalf("request did not use fixed path-style S3 endpoint: %s %s", request.Method, request.URL)
	}
	for header, want := range map[string]string{"If-None-Match": "*", "X-Amz-Acl": "public-read", "X-Amz-Meta-Source-Sha": strings.Repeat("a", 40), "X-Amz-Meta-Sha256": strings.Repeat("b", 64), "Content-Type": "application/vnd.android.package-archive", "Content-Disposition": `attachment; filename="Heartbeat-Checker.apk"`, "Cache-Control": "public, max-age=31536000, immutable"} {
		if got := request.Header.Get(header); got != want {
			t.Errorf("%s=%q, want %q", header, got, want)
		}
	}
	if !strings.Contains(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256") || !strings.Contains(request.Header.Get("Authorization"), "fixture-access-id") {
		t.Fatalf("request was not signed with explicit test credentials: %q", request.Header.Get("Authorization"))
	}
	if !bytes.Equal(recorder.bodies[0], body) {
		t.Fatalf("signed request body changed: %q", recorder.bodies[0])
	}
}

func TestMultipartUploadCompletesAndAbortsFailedPart(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		fail                    bool
		wantComplete, wantAbort bool
	}{{"complete", false, true, false}, {"abort", true, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &requestRecorder{failPart: tc.fail}
			client := sdkTestClient(recorder)
			path := filepath.Join(t.TempDir(), "large.bundle")
			body := bytes.Repeat([]byte("x"), 8*1024*1024+1)
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			err := client.Upload(context.Background(), "heartbeat-checker/production/daily/2026-10-20/bundle.tar.age", path, PutOptions{Metadata: map[string]string{"sha256": "fixture"}, ContentType: "application/octet-stream"})
			if tc.fail && err == nil {
				t.Fatal("failed multipart part reported success")
			}
			if !tc.fail && err != nil {
				t.Fatal(err)
			}
			var create, part, complete, abort int
			var partBodies [][]byte
			for i, request := range recorder.requests {
				switch {
				case request.Method == http.MethodPost && request.URL.Query().Has("uploads"):
					create++
				case request.Method == http.MethodPut && request.URL.Query().Has("partNumber"):
					part++
					partBodies = append(partBodies, recorder.bodies[i])
				case request.Method == http.MethodPost && request.URL.Query().Has("uploadId"):
					complete++
				case request.Method == http.MethodDelete && request.URL.Query().Has("uploadId"):
					abort++
				}
			}
			if create != 1 || part < 1 || complete != btoi(tc.wantComplete) || abort != btoi(tc.wantAbort) {
				t.Fatalf("multipart lifecycle create=%d part=%d complete=%d abort=%d requests=%v", create, part, complete, abort, recorder.requests)
			}
			if len(partBodies) == 0 || len(partBodies[0]) == 0 {
				t.Fatalf("multipart part body missing: %d", len(partBodies))
			}
			if len(partBodies[0]) > 8*1024*1024 {
				t.Fatalf("part exceeded configured size: %d", len(partBodies[0]))
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestHeadMapsNotFoundAndListHonorsExactPrefix(t *testing.T) {
	recorder := &requestRecorder{}
	client := sdkTestClient(recorder)
	if _, err := client.Head(context.Background(), "missing"); err == nil {
		t.Fatal("missing object returned as present")
	}
	if len(recorder.requests) != 1 || recorder.requests[0].Method != http.MethodHead || recorder.requests[0].URL.Path != "/heartbeat-data-1/missing" {
		t.Fatalf("Head request contract changed: %#v", recorder.requests)
	}
	keys, err := client.List(context.Background(), "heartbeat-checker/prefix/", 1)
	if err != nil || len(keys) != 1 || keys[0] != "heartbeat-checker/prefix/item.tar.age" {
		t.Fatalf("prefix list failed: %v %v", keys, err)
	}
	request := recorder.requests[1]
	if request.Method != http.MethodGet || request.URL.Path != "/heartbeat-data-1" || request.URL.Query().Get("prefix") != "heartbeat-checker/prefix/" || request.URL.Query().Get("max-keys") != "1" {
		t.Fatalf("list request did not preserve fixed bucket and prefix: %s %s", request.Method, request.URL)
	}
}

func TestPrivateACLDetectsPublicGroupGrantAndPublicReadUsesCannedACL(t *testing.T) {
	recorder := &requestRecorder{groupACL: true}
	client := sdkTestClient(recorder)
	private, err := client.PrivateACL(context.Background(), "heartbeat-checker/production/daily/private.age")
	if err != nil || private {
		t.Fatalf("public group ACL was reported private: private=%v err=%v", private, err)
	}
	if err := client.PublicRead(context.Background(), "heartbeat-checker/android/1.2.3/Heartbeat-Checker.apk"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 2 {
		t.Fatalf("unexpected ACL calls: %d", len(recorder.requests))
	}
	request := recorder.requests[1]
	if request.Method != http.MethodPut || !request.URL.Query().Has("acl") || request.Header.Get("X-Amz-Acl") != "public-read" {
		t.Fatalf("PublicRead did not set the canned ACL: %s %s %#v", request.Method, request.URL, request.Header)
	}
}

func TestPresignUsesFixedPathStyleEndpointAndRequestedLifetime(t *testing.T) {
	client := sdkTestClient(&requestRecorder{})
	signedURL, err := client.Presign(context.Background(), "heartbeat-checker/production/daily/2026-10-20/bundle.tar.age", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "s3.twcstorage.ru" || parsed.Path != "/heartbeat-data-1/heartbeat-checker/production/daily/2026-10-20/bundle.tar.age" || parsed.Query().Get("X-Amz-Expires") != "604800" || parsed.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("signed URL violated storage/lifetime contract: %s", signedURL)
	}
}
