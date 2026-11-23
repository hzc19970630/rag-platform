package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Store struct {
	client *minio.Client
	// core owns multipart calls; the high-level Client does not expose them.
	core *minio.Core
	// signer uses the browser-reachable endpoint. Nil means direct uploads are
	// disabled and the API proxies parts instead.
	signer         *minio.Client
	bucket         string
	maxObjectBytes int64
}

func NewS3(endpoint, publicEndpoint, accessKey, secretKey, region, bucket string, useTLS, forcePathStyle bool, maxObjectBytes int64) (*S3Store, error) {
	options := &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: useTLS, Region: region}
	if forcePathStyle {
		// S3-compatible endpoints such as MinIO cannot be addressed as
		// bucket.host, so path-style has to be requested explicitly. Leaving the
		// option at its zero value keeps the client's own endpoint detection.
		options.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("create object storage client: %w", err)
	}
	core, err := minio.NewCore(endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("create object storage core client: %w", err)
	}
	var signer *minio.Client
	if public := strings.TrimSpace(publicEndpoint); public != "" {
		parsed, parseErr := url.Parse(public)
		if parseErr != nil || parsed.Host == "" {
			return nil, fmt.Errorf("S3_PUBLIC_ENDPOINT must be an absolute URL")
		}
		publicOptions := &minio.Options{
			Creds:        options.Creds,
			Secure:       parsed.Scheme == "https",
			Region:       region,
			BucketLookup: options.BucketLookup,
		}
		signer, err = minio.New(parsed.Host, publicOptions)
		if err != nil {
			return nil, fmt.Errorf("create public object storage client: %w", err)
		}
	}
	return &S3Store{client: client, core: core, signer: signer, bucket: bucket, maxObjectBytes: maxObjectBytes}, nil
}

// Ready reports whether the configured bucket is reachable. A TCP check on the
// endpoint passes even when credentials are wrong or the bucket was removed.
func (s *S3Store) Ready(ctx context.Context) error {
	if _, err := s.client.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("check object bucket: %w", err)
	}
	return nil
}

func (s *S3Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check object bucket: %w", err)
	}
	if exists {
		return nil
	}
	// The existence check and the create are not atomic. Every component calls
	// this while bootstrapping, so on a fresh bucket the losers of that race get
	// BucketAlreadyOwnedByYou; that is the state this call is asking for, and
	// failing here would only depend on start-up ordering.
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		switch minio.ToErrorResponse(err).Code {
		case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
			return nil
		}
		return fmt.Errorf("create object bucket: %w", err)
	}
	return nil
}

func (s *S3Store) Put(ctx context.Context, key string, content []byte, contentType string) error {
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(content), int64(len(content)), minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}

// NewMultipart starts a resumable upload. The content type is fixed on the
// upload so the completed object carries it without a second metadata write.
func (s *S3Store) NewMultipart(ctx context.Context, key, contentType string) (string, error) {
	uploadID, err := s.core.NewMultipartUpload(ctx, s.bucket, key, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("start multipart upload %s: %w", key, err)
	}
	return uploadID, nil
}

func (s *S3Store) PutPart(ctx context.Context, key, uploadID string, part int, body io.Reader, size int64) (ports.MultipartPart, error) {
	stored, err := s.core.PutObjectPart(ctx, s.bucket, key, uploadID, part, body, size, minio.PutObjectPartOptions{})
	if err != nil {
		return ports.MultipartPart{}, fmt.Errorf("put part %d of %s: %w", part, key, err)
	}
	return ports.MultipartPart{Number: stored.PartNumber, Size: stored.Size, ETag: stored.ETag}, nil
}

func (s *S3Store) PresignPart(ctx context.Context, key, uploadID string, part int, ttl time.Duration) (string, error) {
	if s.signer == nil {
		return "", fmt.Errorf("direct uploads are not configured")
	}
	params := url.Values{"partNumber": {strconv.Itoa(part)}, "uploadId": {uploadID}}
	signed, err := s.signer.PresignHeader(ctx, http.MethodPut, s.bucket, key, ttl, params, nil)
	if err != nil {
		return "", fmt.Errorf("presign part %d of %s: %w", part, key, err)
	}
	return signed.String(), nil
}

func (s *S3Store) ListParts(ctx context.Context, key, uploadID string) ([]ports.MultipartPart, error) {
	var parts []ports.MultipartPart
	marker := 0
	for {
		result, err := s.core.ListObjectParts(ctx, s.bucket, key, uploadID, marker, 1000)
		if err != nil {
			if missingMultipart(err) {
				return nil, apperror.ErrNotFound
			}
			return nil, fmt.Errorf("list parts of %s: %w", key, err)
		}
		for _, part := range result.ObjectParts {
			parts = append(parts, ports.MultipartPart{Number: part.PartNumber, Size: part.Size, ETag: part.ETag})
		}
		if !result.IsTruncated || result.NextPartNumberMarker <= marker {
			return parts, nil
		}
		marker = result.NextPartNumberMarker
	}
}

func (s *S3Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []ports.MultipartPart) (int64, error) {
	completed := make([]minio.CompletePart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, minio.CompletePart{PartNumber: part.Number, ETag: part.ETag})
	}
	info, err := s.core.CompleteMultipartUpload(ctx, s.bucket, key, uploadID, completed, minio.PutObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("complete multipart upload %s: %w", key, err)
	}
	return info.Size, nil
}

// AbortMultipart is idempotent: a missing upload is already in the state the
// caller wants, which is what an expiry sweeper needs on a second pass.
func (s *S3Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if strings.TrimSpace(uploadID) == "" {
		return nil
	}
	if err := s.core.AbortMultipartUpload(ctx, s.bucket, key, uploadID); err != nil {
		if missingMultipart(err) {
			return nil
		}
		return fmt.Errorf("abort multipart upload %s: %w", key, err)
	}
	return nil
}

func missingMultipart(err error) bool {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchUpload", "NoSuchKey", "NotFound":
		return true
	}
	return false
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	if s.maxObjectBytes > 0 {
		stat, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
		if err != nil {
			if missingObject(err) {
				return nil, apperror.ErrContentUnavailable
			}
			return nil, fmt.Errorf("stat object %s: %w", key, err)
		}
		if stat.Size > s.maxObjectBytes {
			return nil, fmt.Errorf("object %s is %d bytes, above the %d byte read limit", key, stat.Size, s.maxObjectBytes)
		}
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if missingObject(err) {
			return nil, apperror.ErrContentUnavailable
		}
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer object.Close()
	content, err := readLimited(object, s.maxObjectBytes)
	if err != nil {
		if missingObject(err) {
			return nil, apperror.ErrContentUnavailable
		}
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}
	return content, nil
}

// Stat reports the stored object's size. Missing objects map to
// ErrContentUnavailable so callers can fail before writing headers.
func (s *S3Store) Stat(ctx context.Context, key string) (int64, error) {
	stat, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if missingObject(err) {
			return 0, apperror.ErrContentUnavailable
		}
		return 0, fmt.Errorf("stat object %s: %w", key, err)
	}
	if s.maxObjectBytes > 0 && stat.Size > s.maxObjectBytes {
		return 0, fmt.Errorf("object %s is %d bytes, above the %d byte read limit", key, stat.Size, s.maxObjectBytes)
	}
	return stat.Size, nil
}

// Open returns a lazy stream for callers that decode incrementally. It is the
// only object read that is not bounded by maxObjectBytes because no bytes are
// buffered; the caller controls how much it consumes.
func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if missingObject(err) {
			return nil, apperror.ErrContentUnavailable
		}
		return nil, fmt.Errorf("open object %s: %w", key, err)
	}
	return object, nil
}

// readLimited bounds how many bytes an object read may buffer. The +1 byte
// distinguishes "exactly at the limit" from "over the limit" without trusting
// a prior metadata call.
func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(reader)
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("object exceeds the %d byte read limit", maxBytes)
	}
	return content, nil
}

// missingObject recognizes the S3 codes for an absent key or bucket. The
// object key itself stays out of the returned error so callers can surface a
// stable error to clients.
func missingObject(err error) bool {
	if err == nil {
		return false
	}
	var value minio.ErrorResponse
	if errors.As(err, &value) {
		return value.Code == "NoSuchKey" || value.Code == "NoSuchBucket"
	}
	var pointer *minio.ErrorResponse
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.Code == "NoSuchKey" || pointer.Code == "NoSuchBucket"
	}
	return false
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete object %s: %w", key, err)
	}
	return nil
}

// DeletePrefix removes every object under a prefix. The prefix is matched after
// trimming a leading slash; an empty prefix would address the whole bucket and
// is rejected.
func (s *S3Store) DeletePrefix(ctx context.Context, prefix string) error {
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return fmt.Errorf("delete prefix: empty prefix")
	}
	keys := make([]string, 0, 64)
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return fmt.Errorf("list prefix %s: %w", prefix, object.Err)
		}
		keys = append(keys, object.Key)
	}
	for start := 0; start < len(keys); start += 500 {
		end := start + 500
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]
		objects := make(chan minio.ObjectInfo, len(batch))
		for _, key := range batch {
			objects <- minio.ObjectInfo{Key: key}
		}
		close(objects)
		for removal := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
			if removal.Err != nil {
				return fmt.Errorf("delete object %s: %w", removal.ObjectName, removal.Err)
			}
		}
	}
	return nil
}
