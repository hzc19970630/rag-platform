package relationstore

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	port "github.com/example/rag-platform/internal/relationstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.Document{}, &models.DocumentVersion{}, &models.Chunk{},
		&models.GraphSnapshot{}, &models.GraphEntity{}, &models.GraphEntityAlias{},
		&models.GraphRelation{}, &models.GraphRelationSource{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func testWrite(runID, snapshotID string) port.SnapshotWrite {
	now := time.Now().UTC()
	entityOne := snapshotID + "-e1"
	entityTwo := snapshotID + "-e2"
	relationID := snapshotID + "-r1"
	return port.SnapshotWrite{
		Snapshot: port.Snapshot{
			ID: snapshotID, TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: runID,
			Status: models.GraphSnapshotBuilding, SourceHash: "hash", ACLVersion: 1,
			Model: "m", PromptVersion: "p", Stats: `{"entities":2,"relations":1}`, CreatedAt: now, UpdatedAt: now,
		},
		Entities: []port.Entity{
			{ID: entityOne, TenantID: "t1", KnowledgeBaseID: "kb1", CanonicalKey: "alpha", Name: "Alpha"},
			{ID: entityTwo, TenantID: "t1", KnowledgeBaseID: "kb1", CanonicalKey: "beta", Name: "Beta"},
		},
		Aliases: []port.Alias{
			{TenantID: "t1", KnowledgeBaseID: "kb1", EntityID: entityOne, Alias: "A-One", Normalized: "a one", Rule: "declared_alias", CreatedAt: now},
		},
		Relations: []port.Relation{
			{ID: relationID, TenantID: "t1", KnowledgeBaseID: "kb1", SubjectID: entityOne, Predicate: "knows", ObjectID: entityTwo, Confidence: 1},
		},
		Sources: []port.Source{
			{TenantID: "t1", KnowledgeBaseID: "kb1", RelationID: relationID, DocumentID: "d1", DocumentVersionID: "dv1", ChunkID: "c1", SourcePath: "d1.md"},
		},
	}
}

func TestReplaceRunSnapshotAndActivate(t *testing.T) {
	db := openTestDB(t)
	store := New(db)
	ctx := context.Background()

	if err := store.ReplaceRunSnapshot(ctx, testWrite("run1", "gs1")); err != nil {
		t.Fatalf("replace run snapshot: %v", err)
	}
	snapshot, err := store.SnapshotByRun(ctx, "run1")
	if err != nil {
		t.Fatalf("snapshot by run: %v", err)
	}
	if snapshot.Version != 1 || snapshot.Status != models.GraphSnapshotBuilding {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	graph, err := store.Graph(ctx, snapshot.ID)
	if err != nil {
		t.Fatalf("load graph: %v", err)
	}
	if len(graph.Entities) != 2 || len(graph.Aliases) != 1 || len(graph.Relations) != 1 || len(graph.Sources) != 1 {
		t.Fatalf("unexpected graph: %+v", graph)
	}

	refs, err := store.SourceRefs(ctx, snapshot.ID)
	if err != nil {
		t.Fatalf("source refs: %v", err)
	}
	if refs.SourcelessRelations != 0 || len(refs.Refs) != 1 {
		t.Fatalf("unexpected source refs: %+v", refs)
	}

	now := time.Now().UTC()
	if err := store.Approve(ctx, "t1", snapshot.ID, "kb1", port.ReviewUpdate{Status: models.GraphSnapshotActive, Reviewer: "u1", At: now}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	active, err := store.ActiveSnapshot(ctx, "t1", "kb1", 1)
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	if active == nil || active.ID != snapshot.ID {
		t.Fatalf("approved snapshot must be active, got %+v", active)
	}
	counts, err := store.Counts(ctx, "t1", "kb1")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts.ActiveSnapshot != snapshot.ID || counts.Entities != 2 || counts.Relations != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestApproveSupersedesPreviousActive(t *testing.T) {
	db := openTestDB(t)
	store := New(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.ReplaceRunSnapshot(ctx, testWrite("run1", "gs1")); err != nil {
		t.Fatalf("write first snapshot: %v", err)
	}
	if err := store.Approve(ctx, "t1", "gs1", "kb1", port.ReviewUpdate{Status: models.GraphSnapshotActive, Reviewer: "u1", At: now}); err != nil {
		t.Fatalf("approve first snapshot: %v", err)
	}
	if err := store.ReplaceRunSnapshot(ctx, testWrite("run2", "gs2")); err != nil {
		t.Fatalf("write second snapshot: %v", err)
	}
	if err := store.Approve(ctx, "t1", "gs2", "kb1", port.ReviewUpdate{Status: models.GraphSnapshotActive, Reviewer: "u1", At: now}); err != nil {
		t.Fatalf("approve second snapshot: %v", err)
	}

	first, err := store.SnapshotByID(ctx, "t1", "gs1")
	if err != nil {
		t.Fatalf("load first snapshot: %v", err)
	}
	if first.Status != models.GraphSnapshotSuperseded {
		t.Fatalf("previous active snapshot must be superseded, got %s", first.Status)
	}
	active, err := store.ActiveSnapshot(ctx, "t1", "kb1", 1)
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	if active == nil || active.ID != "gs2" {
		t.Fatalf("second snapshot must be active, got %+v", active)
	}
}

func TestSourceRefsMarksUntraceableRelation(t *testing.T) {
	db := openTestDB(t)
	store := New(db)
	ctx := context.Background()
	write := testWrite("run1", "gs1")
	write.Sources = nil
	if err := store.ReplaceRunSnapshot(ctx, write); err != nil {
		t.Fatalf("replace run snapshot: %v", err)
	}
	refs, err := store.SourceRefs(ctx, "gs1")
	if err != nil {
		t.Fatalf("source refs: %v", err)
	}
	if refs.SourcelessRelations != 1 {
		t.Fatalf("expected one untraceable relation, got %+v", refs)
	}
}

func TestMarkStaleByKnowledgeBase(t *testing.T) {
	db := openTestDB(t)
	store := New(db)
	ctx := context.Background()
	if err := store.ReplaceRunSnapshot(ctx, testWrite("run1", "gs1")); err != nil {
		t.Fatalf("replace run snapshot: %v", err)
	}
	if err := store.MarkStaleByKnowledgeBase(ctx, "t1", "kb1", time.Now().UTC()); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	snapshot, err := store.SnapshotByID(ctx, "t1", "gs1")
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Status != models.GraphSnapshotStale {
		t.Fatalf("snapshot must be stale, got %s", snapshot.Status)
	}
}

func TestStaleSnapshotIDsDetectsACLAndSourceDrift(t *testing.T) {
	db := openTestDB(t)
	store := New(db)
	ctx := context.Background()
	now := time.Now().UTC()

	seedEvidence(t, db, now)
	if err := store.ReplaceRunSnapshot(ctx, testWrite("run1", "gs1")); err != nil {
		t.Fatalf("replace run snapshot: %v", err)
	}
	if err := store.Approve(ctx, "t1", "gs1", "kb1", port.ReviewUpdate{Status: models.GraphSnapshotActive, Reviewer: "u1", At: now}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	stale, err := store.StaleSnapshotIDs(ctx)
	if err != nil {
		t.Fatalf("find stale snapshots: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("current snapshot must not be stale, got %v", stale)
	}

	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").Update("acl_version", 2).Error; err != nil {
		t.Fatalf("bump acl: %v", err)
	}
	stale, err = store.StaleSnapshotIDs(ctx)
	if err != nil {
		t.Fatalf("find stale snapshots after acl change: %v", err)
	}
	if len(stale) != 1 || stale[0] != "gs1" {
		t.Fatalf("acl drift must stale the snapshot, got %v", stale)
	}
}

func seedEvidence(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	if err := db.Create(&models.KnowledgeBase{ID: "kb1", TenantID: "t1", OwnerID: "u1", Name: "KB", ACLVersion: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.Document{ID: "d1", TenantID: "t1", KnowledgeBaseID: "kb1", OwnerID: "u1", Filename: "d1.md", MimeType: "text/markdown", Status: "EVIDENCE_READY", LatestVersionID: "dv1", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{ID: "dv1", DocumentID: "d1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 1, ContentHash: "h", Status: "EVIDENCE_READY", ACLVersion: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed document version: %v", err)
	}
	if err := db.Create(&models.Chunk{ID: "c1", TenantID: "t1", KnowledgeBaseID: "kb1", DocumentVersionID: "dv1", Content: "alpha", ContentHash: "h", Status: "EVIDENCE_READY", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
}
