// Package s3 implements objectstore.Store on S3 or an S3-compatible service
// (MinIO, Ceph RGW). Only object semantics are used (P5), and signed URLs are
// native presigned GETs, so no API-side verification is needed.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/objectstore"
)

// Config configures a Store.
type Config struct {
	Bucket string
	Region string
	// Endpoint overrides the S3 endpoint, e.g. http://minio:9000.
	Endpoint string
	// UsePathStyle addresses buckets as endpoint/bucket/key; MinIO needs it.
	UsePathStyle bool
	// AccessKey/SecretKey are optional static credentials; when empty the
	// default AWS chain (env, shared config, IRSA, instance role) is used.
	AccessKey string
	SecretKey string
	// HTTPClient is optional (tests).
	HTTPClient *http.Client
}

// Store is an S3-backed object store.
type Store struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

var _ objectstore.Store = (*Store)(nil)

// New builds a Store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errs.Invalid("s3: bucket is required")
	}
	var loadOpts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	if cfg.HTTPClient != nil {
		loadOpts = append(loadOpts, awsconfig.WithHTTPClient(cfg.HTTPClient))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}
	if awsCfg.Region == "" {
		// Signing needs a region even for MinIO, which ignores it.
		awsCfg.Region = "us-east-1"
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Many S3-compatible stores reject the CRC trailers the SDK adds
			// by default; only send checksums the operation requires.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	return &Store{bucket: cfg.Bucket, client: client, presign: s3.NewPresignClient(client)}, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	// The SDK needs a seekable body to sign it (and to send over plain HTTP);
	// spool other readers to a temp file rather than holding them in memory.
	body, ok := r.(io.ReadSeeker)
	if !ok {
		f, n, err := spool(r)
		if err != nil {
			return err
		}
		defer func() { f.Close(); os.Remove(f.Name()) }()
		body, size = f, n
	}
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		in.ContentType = &contentType
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return wrap(err, key)
	}
	return nil
}

func spool(r io.Reader) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "lagent-s3-*")
	if err != nil {
		return nil, 0, err
	}
	n, err := io.Copy(f, r)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, 0, err
	}
	return f, n, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	if err := checkKey(key); err != nil {
		return nil, objectstore.ObjectInfo{}, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, objectstore.ObjectInfo{}, wrap(err, key)
	}
	info := objectstore.ObjectInfo{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		ModTime:     aws.ToTime(out.LastModified),
	}
	return out.Body, info, nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]objectstore.ObjectInfo, error) {
	var out []objectstore.ObjectInfo
	pg := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix)})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, wrap(err, prefix)
		}
		for _, o := range page.Contents {
			out = append(out, objectstore.ObjectInfo{
				Key:     aws.ToString(o.Key),
				Size:    aws.ToInt64(o.Size),
				ModTime: aws.ToTime(o.LastModified),
			})
		}
	}
	return out, nil
}

// Delete is idempotent: S3 reports success for missing keys, matching local.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}); err != nil {
		if errs.Is(wrap(err, key), errs.CodeNotFound) {
			return nil
		}
		return wrap(err, key)
	}
	return nil
}

func (s *Store) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", wrap(err, key)
	}
	return req.URL, nil
}

// checkKey rejects keys that the local store would also reject, so behaviour
// does not depend on the driver.
func checkKey(key string) error {
	if key == "" || strings.Contains(key, "..") {
		return errs.Invalid("bad object key %q", key)
	}
	return nil
}

func wrap(err error, key string) error {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return errs.Wrap(errs.CodeNotFound, err, "object %q not found", key)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return errs.Wrap(errs.CodeNotFound, err, "object %q not found", key)
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return errs.Wrap(errs.CodeForbidden, err, "object store denied access")
		case "SlowDown", "ServiceUnavailable", "InternalError":
			return errs.Wrap(errs.CodeUnavailable, err, "object store unavailable")
		}
	}
	return errs.Wrap(errs.CodeUnavailable, err, "object store")
}
