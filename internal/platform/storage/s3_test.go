package storage

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestReadLimitedEnforcesObjectCap(t *testing.T) {
	tests := []struct {
		name    string
		content string
		limit   int64
		wantErr bool
	}{
		{"below limit", "abc", 4, false},
		{"exactly at limit", "abcd", 4, false},
		{"one byte over", "abcde", 4, true},
		{"zero disables limit", strings.Repeat("x", 1024), 0, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			content, err := readLimited(bytes.NewReader([]byte(testCase.content)), testCase.limit)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected an over-limit error, read %d bytes", len(content))
				}
				return
			}
			if err != nil {
				t.Fatalf("readLimited: %v", err)
			}
			if string(content) != testCase.content {
				t.Fatalf("content = %q, want %q", content, testCase.content)
			}
		})
	}
}

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
