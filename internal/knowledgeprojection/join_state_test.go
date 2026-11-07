package knowledgeprojection

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type countingEvidenceReader struct {
	packages []evidence.Package
	calls    int
}

func (r *countingEvidenceReader) CurrentPackages(context.Context, string, string) ([]evidence.Package, error) {
	r.calls++
	return r.packages, nil
}

func TestEnsureProjectionStateCachesExpectedHash(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.KnowledgeProjectionState{}, &models.EvidencePackage{}); err != nil {
		t.Fatalf("migrate projection state: %v", err)
	}
	reader := &countingEvidenceReader{packages: []evidence.Package{{ID: "pkg-1", ContentHash: "hash-1"}}}
	service := NewService(Options{DB: db, Evidence: reader, Generator: testGenerator(t, "local-extractive")})
	knowledgeBase := models.KnowledgeBase{
		ID: "kb-1", TenantID: "tenant-1", ACLVersion: 1,
		IndexingStrategy: `{"wiki_enabled":true,"graph_enabled":false,"required_capabilities":["evidence"]}`,
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		t.Fatalf("parse strategy: %v", err)
	}

	first, err := service.ensureProjectionState(context.Background(), knowledgeBase, strategy)
	if err != nil {
		t.Fatalf("ensure state: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("expected one package load, got %d", reader.calls)
	}
	if first.WikiExpectedHash == "" {
		t.Fatal("expected a materialized wiki hash")
	}

	second, err := service.ensureProjectionState(context.Background(), knowledgeBase, strategy)
	if err != nil {
		t.Fatalf("ensure cached state: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("cached state must not reload packages, calls=%d", reader.calls)
	}
	if second.WikiExpectedHash != first.WikiExpectedHash {
		t.Fatalf("cached hash changed: %q -> %q", first.WikiExpectedHash, second.WikiExpectedHash)
	}

	knowledgeBase.ACLVersion = 2
	third, err := service.ensureProjectionState(context.Background(), knowledgeBase, strategy)
	if err != nil {
		t.Fatalf("ensure state after ACL change: %v", err)
	}
	if reader.calls != 2 {
		t.Fatalf("ACL change must reload packages once, calls=%d", reader.calls)
	}
	if third.WikiExpectedHash == first.WikiExpectedHash {
		t.Fatal("ACL change must produce a different expected hash")
	}

	if err := service.InvalidateProjectionState(context.Background(), knowledgeBase.TenantID, knowledgeBase.ID); err != nil {
		t.Fatalf("invalidate state: %v", err)
	}
	if _, err := service.ensureProjectionState(context.Background(), knowledgeBase, strategy); err != nil {
		t.Fatalf("ensure state after invalidation: %v", err)
	}
	if reader.calls != 3 {
		t.Fatalf("invalidation must force one reload, calls=%d", reader.calls)
	}
}
