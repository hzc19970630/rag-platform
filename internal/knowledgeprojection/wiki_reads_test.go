package knowledgeprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestWikiReadsAndVersions(t *testing.T) {
	service, db := newWikiSecurityFixture(t)
	ctx := context.Background()
	principal := projectPrincipal()
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Update("blocks", "[]").Error; err != nil {
		t.Fatalf("seed blocks: %v", err)
	}
	current := "wv1"
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"current_version_id": &current, "published_version_id": &current,
	}).Error; err != nil {
		t.Fatalf("bind page version: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "idx-1", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1",
		Status: IndexActive, EmbeddingModel: "model", EmbeddingDimension: 8, ACLVersion: 1,
	}).Error; err != nil {
		t.Fatalf("seed index: %v", err)
	}

	pages, err := service.ListPages(ctx, principal, "kb1")
	if err != nil || len(pages) != 1 || pages[0].Page.ID != "wp1" {
		t.Fatalf("ListPages = %+v, %v", pages, err)
	}
	original := service.authz
	service.authz = fakeAuthorizer{deniedActions: map[string]bool{"read": true}}
	if _, err := service.ListPages(ctx, principal, "kb1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("denied ListPages = %v", err)
	}
	service.authz = original

	detail, err := service.GetPage(ctx, principal, "wp1")
	if err != nil || detail.Page.ID != "wp1" || detail.Version == nil {
		t.Fatalf("GetPage = %+v, %v", detail, err)
	}
	if _, err := service.GetPage(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing GetPage = %v", err)
	}
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Update("blocks", "{bad").Error; err != nil {
		t.Fatalf("corrupt blocks: %v", err)
	}
	if _, err := service.GetPage(ctx, principal, "wp1"); err == nil {
		t.Fatal("invalid blocks must fail")
	}
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Update("blocks", "[]").Error; err != nil {
		t.Fatalf("restore blocks: %v", err)
	}

	versions, err := service.ListVersions(ctx, principal, "wp1")
	if err != nil || len(versions) != 1 {
		t.Fatalf("ListVersions = %+v, %v", versions, err)
	}
	if _, err := service.ListVersions(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing ListVersions = %v", err)
	}

	service.invalidatePage(ctx, "t1", "wp1")
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.Status != PageStale {
		t.Fatalf("page status = %s, want stale", page.Status)
	}
	var indexes int64
	if err := db.Model(&models.KnowledgeIndexRecord{}).Where("wiki_page_id = ?", "wp1").Count(&indexes).Error; err != nil {
		t.Fatalf("count index: %v", err)
	}
	if indexes != 0 {
		t.Fatalf("index rows = %d, want 0", indexes)
	}
}

func TestReviewPublishRollback(t *testing.T) {
	service, db := newWikiSecurityFixture(t)
	ctx := context.Background()
	principal := projectPrincipal()
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").
		Updates(map[string]any{"review_status": ReviewPending, "blocks": "[]"}).Error; err != nil {
		t.Fatalf("reset version: %v", err)
	}

	if _, err := service.Review(ctx, principal, "wp1", "wv1", ReviewInput{Action: "approve"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := service.Review(ctx, principal, "wp1", "wv1", ReviewInput{Action: "approve"}); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("second approve = %v, want conflict", err)
	}
	if _, err := service.Review(ctx, principal, "wp1", "wv1", ReviewInput{Action: "invalid"}); err == nil {
		t.Fatal("invalid review action must fail")
	}
	if _, err := service.Review(ctx, principal, "wp1", "missing", ReviewInput{Action: "approve"}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing version = %v", err)
	}

	published, err := service.Publish(ctx, principal, "wp1", "wv1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != PagePublished || published.PublishedVersionID == nil {
		t.Fatalf("published page = %+v", published)
	}
	if _, err := service.Rollback(ctx, principal, "wp1", RollbackInput{VersionID: "missing"}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("rollback missing = %v", err)
	}
	rolled, err := service.Rollback(ctx, principal, "wp1", RollbackInput{VersionID: "wv1"})
	if err != nil || rolled.Status != PagePublished {
		t.Fatalf("rollback = %+v, %v", rolled, err)
	}
}

func TestGraphRunReads(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	principal := projectPrincipal()
	relation := seedProjectionRun(t, db, "run-relation", ProjectionTypeRelation, RelationReady)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: relation.ID, Stage: StageExtract, Status: StageSucceeded,
		IdempotencyKey: relation.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed relation stage: %v", err)
	}

	runs, err := service.ListRelationRuns(ctx, principal, "kb1", 0)
	if err != nil || len(runs) != 1 || runs[0].ID != relation.ID {
		t.Fatalf("ListRelationRuns = %+v, %v", runs, err)
	}
	detail, err := service.GetGraphRun(ctx, principal, relation.ID)
	if err != nil || detail.Run.ID != relation.ID || len(detail.Stages) != 1 {
		t.Fatalf("GetGraphRun = %+v, %v", detail, err)
	}
	if _, err := service.GetGraphRun(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing GetGraphRun = %v", err)
	}

	if _, err := service.RebuildGraph(ctx, authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "api_key"}, "kb1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key RebuildGraph = %v, want forbidden", err)
	}
	if _, err := service.Rebuild(ctx, authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "api_key"}, "kb1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key Rebuild = %v, want forbidden", err)
	}
}

func TestJoinStatusAndExpectedHashes(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()

	status, err := service.JoinStatus(ctx, "t1", "kb1")
	if err != nil || status != JoinEvidenceReady {
		t.Fatalf("JoinStatus disabled = %q, %v", status, err)
	}
	capabilities, err := service.CapabilityStatus(ctx, "t1", "kb1")
	if err != nil || !capabilities.WikiReady || !capabilities.RelationReady {
		t.Fatalf("CapabilityStatus = %+v, %v", capabilities, err)
	}
	if _, err := service.JoinStatus(ctx, "t1", "missing"); err == nil {
		t.Fatal("missing JoinStatus must fail")
	}

	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").
		Update("indexing_strategy", `{"wiki_enabled":true,"required_capabilities":["evidence"]}`).Error; err != nil {
		t.Fatalf("enable wiki: %v", err)
	}
	var knowledgeBase models.KnowledgeBase
	if err := db.First(&knowledgeBase, "id = ?", "kb1").Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	service.upsertWikiExpectedHash(ctx, knowledgeBase, "hash-current")
	seedProjectionRun(t, db, "run-ready", ProjectionTypeWiki, ProjectionReady)
	if err := db.Model(&models.ProjectionRun{}).Where("id = ?", "run-ready").Update("source_hash", "hash-current").Error; err != nil {
		t.Fatalf("set run hash: %v", err)
	}
	status, err = service.JoinStatus(ctx, "t1", "kb1")
	if err != nil || status != JoinKnowledgeReady {
		t.Fatalf("JoinStatus ready = %q, %v", status, err)
	}
	if err := db.Model(&models.ProjectionRun{}).Where("id = ?", "run-ready").Update("source_hash", "stale").Error; err != nil {
		t.Fatalf("stale run: %v", err)
	}
	status, err = service.JoinStatus(ctx, "t1", "kb1")
	if err != nil || status != JoinKnowledgePartial {
		t.Fatalf("JoinStatus stale = %q, %v", status, err)
	}
	service.upsertRelationExpectedHash(ctx, knowledgeBase, "relation-hash")
}
