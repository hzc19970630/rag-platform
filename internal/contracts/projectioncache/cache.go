// Package projectioncache owns the cache key and port for per-package
// projection generation results. The cache is a derived optimization: a miss
// must always be able to regenerate the value, and a cache failure must never
// fail the projection.
package projectioncache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
)

// Key identifies one generated package result. InputHash covers the package
// content, ACL, model and prompt version, so a change in any of them is a miss.
type Key struct {
	TenantID        string
	KnowledgeBaseID string
	ProjectionType  string
	// Artifact distinguishes multiple cached products for the same package, for
	// example a Wiki document page and its overview block.
	Artifact  string
	PackageID string
	InputHash string
	// PromptVersion, Model and ACLVersion are stored for diagnostics. They are
	// also part of InputHash; the cache never needs to compare them separately.
	PromptVersion string
	Model         string
	ACLVersion    int64
}

// Cache stores one payload per (tenant, knowledge base, projection type,
// artifact, package). Implementations may replace an older InputHash in place;
// callers must treat a hash mismatch as a miss.
type Cache interface {
	Get(ctx context.Context, key Key) ([]byte, bool, error)
	Put(ctx context.Context, key Key, payload []byte) error
	DeleteScope(ctx context.Context, tenantID, knowledgeBaseID, projectionType string) error
}

// digestPackage deliberately excludes trace/request/idempotency/created-at
// fields: replaying the same package must not invalidate the generation cache.
type digestPackage struct {
	ID                string        `json:"id"`
	DocumentID        string        `json:"documentId"`
	DocumentVersionID string        `json:"documentVersionId"`
	ContentHash       string        `json:"contentHash"`
	ACLVersion        int64         `json:"aclVersion"`
	Chunks            []digestChunk `json:"chunks"`
}

type digestChunk struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentId"`
	Ordinal     int    `json:"ordinal"`
	Content     string `json:"content"`
	ContentHash string `json:"contentHash"`
	HeadingPath string `json:"headingPath"`
	PageNumber  int    `json:"pageNumber"`
	SourcePath  string `json:"sourcePath"`
	StartOffset int    `json:"startOffset"`
	EndOffset   int    `json:"endOffset"`
}

// PackageDigest hashes every input that affects generated page or relation
// content. It is stable across pipeline replays with the same evidence.
func PackageDigest(pkg evidence.Package) string {
	chunks := make([]digestChunk, len(pkg.Chunks))
	for index, chunk := range pkg.Chunks {
		chunks[index] = digestChunk{
			ID: chunk.ID, ParentID: chunk.ParentID, Ordinal: chunk.Ordinal,
			Content: chunk.Content, ContentHash: chunk.ContentHash,
			HeadingPath: chunk.HeadingPath, PageNumber: chunk.PageNumber,
			SourcePath: chunk.SourcePath, StartOffset: chunk.StartOffset, EndOffset: chunk.EndOffset,
		}
	}
	payload, _ := json.Marshal(digestPackage{
		ID: pkg.ID, DocumentID: pkg.DocumentID, DocumentVersionID: pkg.DocumentVersionID,
		ContentHash: pkg.ContentHash, ACLVersion: pkg.ACLVersion, Chunks: chunks,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
