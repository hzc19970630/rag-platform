package projectioncache

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	cachecontract "github.com/hzc19970630/rag-platform/internal/contracts/projectioncache"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&models.ProjectionGenerationCache{}, &models.EvidencePackage{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return New(db)
}

func testKey(packageID, inputHash string) cachecontract.Key {
	return cachecontract.Key{
		TenantID: "t1", KnowledgeBaseID: "kb-1", ProjectionType: "wiki",
		Artifact: "document", PackageID: packageID, InputHash: inputHash,
		PromptVersion: "prompt-v1", Model: "model-v1", ACLVersion: 1,
	}
}

func TestStoreGetPutAndHashMismatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	key := testKey("pkg-1", "hash-1")

	if _, ok, err := store.Get(ctx, key); err != nil || ok {
		t.Fatalf("empty cache must miss: ok=%v err=%v", ok, err)
	}
	if err := store.Put(ctx, key, []byte(`{"value":1}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	payload, ok, err := store.Get(ctx, key)
	if err != nil || !ok || string(payload) != `{"value":1}` {
		t.Fatalf("get after put: ok=%v payload=%s err=%v", ok, payload, err)
	}

	// A changed input hash is a miss; putting the new variant replaces the row.
	next := testKey("pkg-1", "hash-2")
	if _, ok, err := store.Get(ctx, next); err != nil || ok {
		t.Fatalf("new hash must miss: ok=%v err=%v", ok, err)
	}
	if err := store.Put(ctx, next, []byte(`{"value":2}`)); err != nil {
		t.Fatalf("put next: %v", err)
	}
	if _, ok, _ := store.Get(ctx, key); ok {
		t.Fatalf("the previous hash must not be served after overwrite")
	}
	if payload, ok, _ := store.Get(ctx, next); !ok || string(payload) != `{"value":2}` {
		t.Fatalf("the current hash must be served")
	}

	if err := store.DeleteScope(ctx, "t1", "kb-1", "wiki"); err != nil {
		t.Fatalf("delete scope: %v", err)
	}
	if _, ok, _ := store.Get(ctx, next); ok {
		t.Fatalf("delete scope must remove the row")
	}
}

func TestStoreDeleteScopeIsProjectionTypeScoped(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	wiki := testKey("pkg-1", "hash-1")
	relation := wiki
	relation.ProjectionType = "relation"
	relation.Artifact = "extraction"
	if err := store.Put(ctx, wiki, []byte(`{}`)); err != nil {
		t.Fatalf("put wiki: %v", err)
	}
	if err := store.Put(ctx, relation, []byte(`{}`)); err != nil {
		t.Fatalf("put relation: %v", err)
	}
	if err := store.DeleteScope(ctx, "t1", "kb-1", "wiki"); err != nil {
		t.Fatalf("delete wiki scope: %v", err)
	}
	if _, ok, _ := store.Get(ctx, wiki); ok {
		t.Fatalf("wiki cache must be cleared")
	}
	if _, ok, _ := store.Get(ctx, relation); !ok {
		t.Fatalf("relation cache must survive a wiki rebuild")
	}
}

func TestPurgeOrphansKeepsLivePackages(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now()
	live := models.EvidencePackage{
		ID: "pkg-live", TenantID: "t1", KnowledgeBaseID: "kb-1", DocumentVersionID: "dv-1",
		ContentHash: "hash", ACLVersion: 1, Status: "EVIDENCE_READY", PackageJSON: "{}",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.db.Create(&live).Error; err != nil {
		t.Fatalf("create live package: %v", err)
	}
	if err := store.Put(ctx, testKey("pkg-live", "hash-1"), []byte(`{}`)); err != nil {
		t.Fatalf("put live: %v", err)
	}
	if err := store.Put(ctx, testKey("pkg-orphan", "hash-1"), []byte(`{}`)); err != nil {
		t.Fatalf("put orphan: %v", err)
	}
	purged, err := store.PurgeOrphans(ctx, 10)
	if err != nil {
		t.Fatalf("purge orphans: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	if _, ok, _ := store.Get(ctx, testKey("pkg-live", "hash-1")); !ok {
		t.Fatalf("live package cache must remain")
	}
	if _, ok, _ := store.Get(ctx, testKey("pkg-orphan", "hash-1")); ok {
		t.Fatalf("orphan package cache must be removed")
	}
}
