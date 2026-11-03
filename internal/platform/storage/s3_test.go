package storage

import (
	"errors"
	"fmt"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestMissingObjectClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no such key", minio.ErrorResponse{Code: "NoSuchKey"}, true},
		{"no such bucket", minio.ErrorResponse{Code: "NoSuchBucket"}, true},
		{"pointer no such key", &minio.ErrorResponse{Code: "NoSuchKey"}, true},
		{"wrapped no such key", fmt.Errorf("read object: %w", minio.ErrorResponse{Code: "NoSuchKey"}), true},
		{"access denied", minio.ErrorResponse{Code: "AccessDenied"}, false},
		{"generic", errors.New("connection refused"), false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := missingObject(testCase.err); got != testCase.want {
				t.Fatalf("missingObject(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}
