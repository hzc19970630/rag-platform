package knowledgeprojection

import (
	"context"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type triggerEvidence struct {
	packages []ingestion.EvidencePackage
}

func (f *triggerEvidence) CurrentPackages(context.Context, string, string) ([]ingestion.EvidencePackage, error) {
	return f.packages, nil
}

func newTriggerFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	_, db := newProjectionViewsFixture(t)
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").
		Update("indexing_strategy", `{"wiki_enabled":true,"graph_enabled":true,"required_capabilities":["evidence"]}`).Error; err != nil {
		t.Fatalf("enable projections: %v", err)
	}
	pkg := ingestion.EvidencePackage{
		ID: "pkg-1",
		Envelope: ingestion.Envelope{
			TenantID: "t1", KnowledgeBaseID: "kb1", DocumentID: "d1", DocumentVersionID: "dv1",
		},
		ContentHash: "hash", ACLVersion: 1, Status: "EVIDENCE_READY",
		Chunks: []ingestion.Chunk{{ID: "c1", ParentID: "p1", Ordinal: 1, Content: "text", SourcePath: "d.md"}},
	}
	service := NewService(Options{
		DB: db, Authz: fakeAuthorizer{}, Evidence: &triggerEvidence{packages: []ingestion.EvidencePackage{pkg}},
		Generator: testGenerator(t, "model"), Extractor: testExtractor(t),
	})
	return service, db
}

func countRuns(t *testing.T, db *gorm.DB, projectionType string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.ProjectionRun{}).Where("knowledge_base_id = ? AND projection_type = ?", "kb1", projectionType).Count(&count).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return count
}

func TestTriggerAndRebuildProjections(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()

	if err := service.TriggerWiki(ctx, "t1", "kb1", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerWiki: %v", err)
	}
	if countRuns(t, db, ProjectionTypeWiki) != 1 {
		t.Fatalf("wiki runs = %d, want 1", countRuns(t, db, ProjectionTypeWiki))
	}
	if err := service.TriggerWiki(ctx, "t1", "kb1", "pkg-1", 1); err != nil {
		t.Fatalf("second TriggerWiki: %v", err)
	}
	if countRuns(t, db, ProjectionTypeWiki) != 1 {
		t.Fatal("an active run must coalesce triggers")
	}
	if err := service.TriggerWiki(ctx, "t1", "missing", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerWiki missing kb = %v", err)
	}

	if err := service.TriggerRelation(ctx, "t1", "kb1", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerRelation: %v", err)
	}
	if countRuns(t, db, ProjectionTypeRelation) != 1 {
		t.Fatalf("relation runs = %d, want 1", countRuns(t, db, ProjectionTypeRelation))
	}
	if err := service.TriggerRelation(ctx, "t1", "missing", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerRelation missing kb = %v", err)
	}

	created, err := service.RebuildKnowledgeBase(ctx, "t1", "kb1")
	if err != nil || created != 1 || countRuns(t, db, ProjectionTypeWiki) != 1 {
		t.Fatalf("RebuildKnowledgeBase active = %d, %v", created, err)
	}
	if err := db.Model(&models.ProjectionRun{}).Where("knowledge_base_id = ? AND projection_type = ?", "kb1", ProjectionTypeWiki).
		Update("status", ProjectionFailed).Error; err != nil {
		t.Fatalf("fail wiki run: %v", err)
	}
	created, err = service.RebuildKnowledgeBase(ctx, "t1", "kb1")
	if err != nil || created != 1 || countRuns(t, db, ProjectionTypeWiki) != 2 {
		t.Fatalf("RebuildKnowledgeBase retry = %d, %v, runs = %d", created, err, countRuns(t, db, ProjectionTypeWiki))
	}

	created, err = service.RebuildRelations(ctx, "t1", "kb1")
	if err != nil || created != 1 || countRuns(t, db, ProjectionTypeRelation) != 1 {
		t.Fatalf("RebuildRelations active = %d, %v", created, err)
	}
	if err := db.Model(&models.ProjectionRun{}).Where("knowledge_base_id = ? AND projection_type = ?", "kb1", ProjectionTypeRelation).
		Update("status", RelationFailed).Error; err != nil {
		t.Fatalf("fail relation run: %v", err)
	}
	created, err = service.RebuildRelations(ctx, "t1", "kb1")
	if err != nil || created != 1 || countRuns(t, db, ProjectionTypeRelation) != 2 {
		t.Fatalf("RebuildRelations retry = %d, %v", created, err)
	}

	withoutRelation := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Evidence: &triggerEvidence{}})
	if _, err := withoutRelation.RebuildRelations(ctx, "t1", "kb1"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("RebuildRelations without extractor = %v", err)
	}
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").Update("indexing_strategy", "{}").Error; err != nil {
		t.Fatalf("disable projections: %v", err)
	}
	if _, err := withoutRelation.RebuildKnowledgeBase(ctx, "t1", "kb1"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("RebuildKnowledgeBase disabled = %v", err)
	}
	if err := withoutRelation.TriggerWiki(ctx, "t1", "kb1", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerWiki disabled = %v", err)
	}
	if err := withoutRelation.TriggerRelation(ctx, "t1", "kb1", "pkg-1", 1); err != nil {
		t.Fatalf("TriggerRelation disabled = %v", err)
	}
}

func TestInvalidateAndScheduleMissing(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()

	if err := service.InvalidateKnowledgeBase(ctx, "t1", "kb1", "acl_changed"); err != nil {
		t.Fatalf("InvalidateKnowledgeBase: %v", err)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.Status != PageStale {
		t.Fatalf("page status = %s, want stale", page.Status)
	}

	started, cursor, err := service.ScheduleMissing(ctx, 10, "")
	if err != nil {
		t.Fatalf("ScheduleMissing: %v", err)
	}
	if started < 1 || cursor != "" {
		t.Fatalf("ScheduleMissing = %d, %q", started, cursor)
	}
	if _, _, err := service.ScheduleMissing(ctx, 10, "bad-cursor"); err == nil {
		t.Fatal("malformed cursor must fail")
	}
	started, _, err = service.ScheduleMissing(ctx, 10, "")
	if err != nil || started != 0 {
		t.Fatalf("second ScheduleMissing = %d, %v", started, err)
	}
	if err := db.Model(&models.ProjectionRun{}).Where("knowledge_base_id = ? AND projection_type = ?", "kb1", ProjectionTypeWiki).
		Update("status", ProjectionFailed).Error; err != nil {
		t.Fatalf("fail wiki runs: %v", err)
	}
	if err := db.Model(&models.ProjectionRun{}).Where("knowledge_base_id = ? AND projection_type = ?", "kb1", ProjectionTypeRelation).
		Update("status", RelationFailed).Error; err != nil {
		t.Fatalf("fail relation runs: %v", err)
	}
	started, _, err = service.ScheduleMissing(ctx, 10, "")
	if err != nil || started != 0 {
		t.Fatalf("failed-run ScheduleMissing = %d, %v", started, err)
	}
}
