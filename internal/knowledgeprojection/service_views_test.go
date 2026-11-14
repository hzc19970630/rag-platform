package knowledgeprojection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func seedProjectionRun(t *testing.T, db *gorm.DB, id, projectionType, status string) models.ProjectionRun {
	t.Helper()
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: id, TenantID: "t1", KnowledgeBaseID: "kb1", EvidencePackageID: "pkg-1",
		ProjectionType: projectionType, Status: status, SourceHash: "hash",
		PromptVersion: "prompt", Model: "model", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("seed projection run: %v", err)
	}
	return run
}

func newProjectionViewsFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	service, db := newWikiSecurityFixture(t)
	if err := db.AutoMigrate(&models.ProjectionRun{}, &models.ProjectionStageRun{}, &models.KnowledgeProjectionState{}, &models.EvidencePackage{}, &models.TaskInbox{}, &models.OutboxEvent{}); err != nil {
		t.Fatalf("migrate projection tables: %v", err)
	}
	return service, db
}

func projectPrincipal() authz.Principal {
	return authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
}

func TestProjectionStatusAndRuns(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	wikiRun := seedProjectionRun(t, db, "run-wiki", ProjectionTypeWiki, ProjectionReady)
	seedProjectionRun(t, db, "run-relation", ProjectionTypeRelation, RelationReady)

	status, err := service.Status(ctx, projectPrincipal(), "kb1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.KnowledgeBaseID != "kb1" || len(status.Runs) != 1 || len(status.RelationRuns) != 1 {
		t.Fatalf("status = %+v", status)
	}
	if status.Pages.Total != 1 || status.Pages.Published != 0 {
		t.Fatalf("page counts = %+v", status.Pages)
	}
	if _, err := service.Status(ctx, projectPrincipal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing Status = %v, want not found", err)
	}

	runs, err := service.ListRuns(ctx, projectPrincipal(), "kb1", 0)
	if err != nil || len(runs) != 1 || runs[0].ID != wikiRun.ID {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	original := service.authz
	service.authz = fakeAuthorizer{deniedActions: map[string]bool{"read": true}}
	_, deniedErr := service.ListRuns(ctx, authz.Principal{SubjectID: "u2", TenantID: "t1"}, "kb1", 1000)
	service.authz = original
	if !errors.Is(deniedErr, apperror.ErrForbidden) {
		t.Fatalf("unauthorized ListRuns = %v", deniedErr)
	}

	detail, err := service.GetRun(ctx, projectPrincipal(), wikiRun.ID)
	if err != nil || detail.Run.ID != wikiRun.ID {
		t.Fatalf("GetRun = %+v, %v", detail, err)
	}
	if _, err := service.GetRun(ctx, projectPrincipal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing GetRun = %v, want not found", err)
	}
}

func TestRetryProjectionRun(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	failed := seedProjectionRun(t, db, "run-failed", ProjectionTypeWiki, ProjectionFailed)
	ready := seedProjectionRun(t, db, "run-ready", ProjectionTypeWiki, ProjectionReady)
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: failed.ID, Stage: StageCollect, Status: StageFailed, IdempotencyKey: failed.ID + ":" + StageCollect,
		Attempts: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed failed stage: %v", err)
	}

	if _, err := service.Retry(ctx, authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "api_key"}, failed.ID); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key retry = %v, want forbidden", err)
	}
	if _, err := service.Retry(ctx, projectPrincipal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing retry = %v, want not found", err)
	}
	if _, err := service.Retry(ctx, projectPrincipal(), ready.ID); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("ready retry = %v, want conflict", err)
	}
	retried, err := service.Retry(ctx, projectPrincipal(), failed.ID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.Status != ProjectionRetrying {
		t.Fatalf("retried status = %s, want %s", retried.Status, ProjectionRetrying)
	}
	var audits int64
	if err := db.Model(&models.AuditLog{}).Where("resource_id = ? AND action = ? AND reason LIKE ?", failed.ID, "write", "wiki_retry:%").Count(&audits).Error; err != nil {
		t.Fatalf("count retry audits: %v", err)
	}
	if audits != 1 {
		t.Fatalf("retry audits = %d, want 1", audits)
	}
}

func TestReconcileStalesWikiPages(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").Update("acl_version", 2).Error; err != nil {
		t.Fatalf("bump acl version: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "idx-1", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1",
		Status: IndexActive, EmbeddingModel: "model", EmbeddingDimension: 8, ACLVersion: 1,
	}).Error; err != nil {
		t.Fatalf("seed index record: %v", err)
	}
	staled, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if staled < 1 {
		t.Fatalf("staled = %d, want at least 1", staled)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.Status != PageStale {
		t.Fatalf("page status = %s, want stale", page.Status)
	}
	var indexes int64
	if err := db.Model(&models.KnowledgeIndexRecord{}).Where("wiki_page_id = ?", "wp1").Count(&indexes).Error; err != nil {
		t.Fatalf("count index records: %v", err)
	}
	if indexes != 0 {
		t.Fatalf("stale page kept %d index records", indexes)
	}
}

func TestSearchValidationAndSourceCurrency(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	principal := projectPrincipal()

	if _, err := service.Search(ctx, principal, SearchOptions{KnowledgeBaseID: "kb1"}); err == nil {
		t.Fatal("empty query must be rejected")
	}
	if _, err := service.Search(ctx, principal, SearchOptions{Query: "x"}); err == nil {
		t.Fatal("empty knowledge base must be rejected")
	}
	result, err := service.Search(ctx, principal, SearchOptions{KnowledgeBaseID: "kb1", Query: "x"})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("disabled wiki search = %+v, %v", result, err)
	}

	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").
		Update("indexing_strategy", `{"wiki_enabled":true}`).Error; err != nil {
		t.Fatalf("enable wiki: %v", err)
	}
	if _, err := service.Search(ctx, principal, SearchOptions{KnowledgeBaseID: "kb1", Query: "x"}); err == nil || !strings.Contains(err.Error(), "knowledge recall failed") {
		t.Fatalf("recall failure = %v", err)
	}

	valid, err := service.validSourceChunks(ctx, []models.WikiPageSource{{
		TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageVersionID: "wv1",
		DocumentID: "d1", DocumentVersionID: "dv1", ChunkID: "c1",
	}})
	if err != nil {
		t.Fatalf("validSourceChunks: %v", err)
	}
	if !valid["c1"] {
		t.Fatalf("current source not valid: %+v", valid)
	}
	if err := db.Model(&models.Chunk{}).Where("id = ?", "c1").Update("status", ingestion.StatusStale).Error; err != nil {
		t.Fatalf("stale chunk: %v", err)
	}
	valid, err = service.validSourceChunks(ctx, []models.WikiPageSource{{
		TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageVersionID: "wv1",
		DocumentID: "d1", DocumentVersionID: "dv1", ChunkID: "c1",
	}})
	if err != nil {
		t.Fatalf("validSourceChunks stale: %v", err)
	}
	if len(valid) != 0 {
		t.Fatalf("stale source still valid: %+v", valid)
	}

	if _, err := service.searchKnowledgeKeyword(ctx, "t1", "kb1", "query", 5); err == nil {
		t.Fatal("keyword recall must fail without PostgreSQL BM25 tables")
	}
	if _, err := service.searchKnowledgeVector(ctx, "t1", "kb1", "query", 5); err == nil {
		t.Fatal("vector recall must fail without pgvector")
	}
}

func TestFuseKnowledgeMergesOverlappingPages(t *testing.T) {
	keyword := []KnowledgeHit{{PageID: "p1", VersionID: "v1"}, {PageID: "p2", VersionID: "v2"}}
	vector := []KnowledgeHit{{PageID: "p1", VersionID: "v1"}, {PageID: "p3", VersionID: "v3"}}
	candidates := fuseKnowledge(keyword, vector, 60)
	if len(candidates) != 3 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].hit.PageID != "p1" {
		t.Fatalf("top candidate = %+v", candidates[0])
	}
	if candidates[0].score <= candidates[1].score {
		t.Fatalf("overlapping page must rank higher: %+v", candidates)
	}
	if len(fuseKnowledge(nil, nil, 60)) != 0 {
		t.Fatal("empty recalls must fuse to no candidates")
	}
}
