package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3(endpoint, accessKey, secretKey, region, bucket string, useTLS, forcePathStyle bool) (*S3Store, error) {
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
	return &S3Store{client: client, bucket: bucket}, nil
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

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if missingObject(err) {
			return nil, apperror.ErrContentUnavailable
		}
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	defer object.Close()
	content, err := io.ReadAll(object)
	if err != nil {
		if missingObject(err) {
			return nil, apperror.ErrContentUnavailable
		}
		return nil, fmt.Errorf("read object %s: %w", key, err)
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
