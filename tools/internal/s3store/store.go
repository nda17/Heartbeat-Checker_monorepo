// Package s3store is the fixed-target, explicitly credentialed operations store.
package s3store

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const Bucket = "heartbeat-data-1"
const PublicBase = "https://s3.twcstorage.ru/heartbeat-data-1/"

var ErrNotFound = errors.New("object_not_found")

type Object struct {
	Size     int64
	Metadata map[string]string
}
type PutOptions struct {
	Metadata                                           map[string]string
	ContentType, ContentDisposition, CacheControl, ACL string
	Immutable                                          bool
}

// Store keeps SDK types out of the backup and publication contracts.
type Store interface {
	Put(context.Context, string, io.Reader, int64, PutOptions) error
	Upload(context.Context, string, string, PutOptions) error
	Get(context.Context, string, io.Writer) error
	Head(context.Context, string) (Object, error)
	Delete(context.Context, string) error
	List(context.Context, string, int32) ([]string, error)
	PrivateACL(context.Context, string) (bool, error)
	PublicRead(context.Context, string) error
	Presign(context.Context, string, time.Duration) (string, error)
}
type Client struct{ client *s3.Client }

// The old transport bounded each socket read, rather than the complete bundle.
// Keep that distinction for databases that take longer than a minute to stream.
type idleConnection struct{ net.Conn }

func (c idleConnection) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c idleConnection) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func New(getenv func(string) string) (*Client, error) {
	if getenv("BACKUP_S3_ENDPOINT") != "https://s3.twcstorage.ru" || getenv("BACKUP_S3_BUCKET") != Bucket || getenv("BACKUP_S3_REGION") != "ru-1" {
		return nil, errors.New("invalid_s3_target")
	}
	if getenv("BACKUP_S3_ACCESS_KEY_ID") == "" || getenv("BACKUP_S3_SECRET_ACCESS_KEY") == "" {
		return nil, errors.New("missing_s3_credentials")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return idleConnection{conn}, nil
	}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("http_redirect_rejected") }}
	cfg := aws.Config{Region: "ru-1", Credentials: credentials.NewStaticCredentialsProvider(getenv("BACKUP_S3_ACCESS_KEY_ID"), getenv("BACKUP_S3_SECRET_ACCESS_KEY"), ""), HTTPClient: httpClient, Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 4 }) }, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	return &Client{client: s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String("https://s3.twcstorage.ru"); o.UsePathStyle = true })}, nil
}
func putInput(key string, body io.Reader, size int64, o PutOptions) *s3.PutObjectInput {
	in := &s3.PutObjectInput{Bucket: aws.String(Bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), Metadata: o.Metadata}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.ContentDisposition != "" {
		in.ContentDisposition = aws.String(o.ContentDisposition)
	}
	if o.CacheControl != "" {
		in.CacheControl = aws.String(o.CacheControl)
	}
	if o.ACL != "" {
		in.ACL = types.ObjectCannedACL(o.ACL)
	}
	if o.Immutable {
		in.IfNoneMatch = aws.String("*")
	}
	return in
}
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, o PutOptions) error {
	_, err := c.client.PutObject(ctx, putInput(key, r, size, o))
	return err
}

// Upload streams large database bundles through bounded multipart sections.
func (c *Client) Upload(ctx context.Context, key, path string, o PutOptions) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	const threshold int64 = 8 * 1024 * 1024
	if info.Size() < threshold {
		return c.Put(ctx, key, f, info.Size(), o)
	}
	// Backup uploads use only metadata and a content type. Refuse unsupported
	// multipart options rather than silently losing publication guarantees.
	if o.Immutable || o.ACL != "" || o.CacheControl != "" || o.ContentDisposition != "" {
		return errors.New("unsupported_multipart_options")
	}
	start, err := c.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(Bucket), Key: aws.String(key), Metadata: o.Metadata, ContentType: aws.String(o.ContentType)})
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 70*time.Second)
			defer cancel()
			_, _ = c.client.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{Bucket: aws.String(Bucket), Key: aws.String(key), UploadId: start.UploadId})
		}
	}()
	partSize := threshold
	if n := (info.Size() + 9999) / 10000; n > partSize {
		partSize = n
	}
	parts := []types.CompletedPart{}
	for offset, number := int64(0), int32(1); offset < info.Size(); offset, number = offset+partSize, number+1 {
		length := min(partSize, info.Size()-offset)
		part, e := c.client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(Bucket), Key: aws.String(key), UploadId: start.UploadId, PartNumber: aws.Int32(number), ContentLength: aws.Int64(length), Body: io.NewSectionReader(f, offset, length)})
		if e != nil {
			return e
		}
		parts = append(parts, types.CompletedPart{ETag: part.ETag, PartNumber: aws.Int32(number)})
	}
	_, err = c.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(Bucket), Key: aws.String(key), UploadId: start.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	complete = err == nil
	return err
}
func (c *Client) Get(ctx context.Context, key string, w io.Writer) error {
	o, e := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(Bucket), Key: aws.String(key)})
	if e != nil {
		return e
	}
	defer o.Body.Close()
	_, e = io.Copy(w, o.Body)
	return e
}
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	o, e := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(Bucket), Key: aws.String(key)})
	if e != nil {
		var api smithy.APIError
		if errors.As(e, &api) && (api.ErrorCode() == "404" || api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			return Object{}, ErrNotFound
		}
		return Object{}, e
	}
	return Object{Size: aws.ToInt64(o.ContentLength), Metadata: o.Metadata}, nil
}
func (c *Client) Delete(ctx context.Context, key string) error {
	_, e := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(Bucket), Key: aws.String(key)})
	return e
}
func (c *Client) List(ctx context.Context, prefix string, limit int32) ([]string, error) {
	keys := []string{}
	in := &s3.ListObjectsV2Input{Bucket: aws.String(Bucket), Prefix: aws.String(prefix)}
	if limit > 0 {
		in.MaxKeys = aws.Int32(limit)
	}
	for {
		page, e := c.client.ListObjectsV2(ctx, in)
		if e != nil {
			return nil, e
		}
		for _, item := range page.Contents {
			keys = append(keys, aws.ToString(item.Key))
			if limit > 0 && len(keys) >= int(limit) {
				return keys, nil
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return keys, nil
		}
		if page.NextContinuationToken == nil {
			return nil, errors.New("invalid_s3_pagination")
		}
		in.ContinuationToken = page.NextContinuationToken
	}
}
func (c *Client) PrivateACL(ctx context.Context, key string) (bool, error) {
	var grants []types.Grant
	if key == "" {
		o, e := c.client.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: aws.String(Bucket)})
		if e != nil {
			return false, e
		}
		grants = o.Grants
	} else {
		o, e := c.client.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String(Bucket), Key: aws.String(key)})
		if e != nil {
			return false, e
		}
		grants = o.Grants
	}
	for _, g := range grants {
		if g.Grantee != nil && g.Grantee.Type == types.TypeGroup {
			return false, nil
		}
	}
	return true, nil
}
func (c *Client) PublicRead(ctx context.Context, key string) error {
	_, e := c.client.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: aws.String(Bucket), Key: aws.String(key), ACL: types.ObjectCannedACLPublicRead})
	return e
}
func (c *Client) Presign(ctx context.Context, key string, lifetime time.Duration) (string, error) {
	o, e := s3.NewPresignClient(c.client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(Bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = lifetime })
	if e != nil {
		return "", e
	}
	return o.URL, nil
}
