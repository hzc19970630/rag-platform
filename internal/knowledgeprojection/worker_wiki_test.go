package knowledgeprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func seedWikiRun(t *testing.T, db *gorm.DB, runID, status string) {
	t.Helper()
	if err := db.Create(&models.ProjectionRun{
		ID: runID, TenantID: "t1", KnowledgeBaseID: "kb1", EvidencePackageID: "pkg-1",
		ProjectionType: ProjectionTypeWiki, Status: status, ACLVersion: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func wikiTaskFor(t *testing.T, db *gorm.DB, runID, stage string) Task {
	t.Helper()
	key := runID + ":" + stage
	var stageRow models.ProjectionStageRun
	err := db.First(&stageRow, "idempotency_key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		stageRow = models.ProjectionStageRun{
			ProjectionRunID: runID, ProjectionType: ProjectionTypeWiki, Stage: stage,
			Status: StagePending, IdempotencyKey: key, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := db.Create(&stageRow).Error; err != nil {
			t.Fatalf("seed stage: %v", err)
		}
	} else if err != nil {
		t.Fatalf("load stage: %v", err)
	}
	var inboxes int64
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Count(&inboxes).Error; err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if inboxes == 0 {
		if err := db.Create(&models.TaskInbox{
			IdempotencyKey: key, TaskType: "projection.wiki." + stage, Status: "PROCESSING",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("seed inbox: %v", err)
		}
	}
	return Task{
		SchemaVersion: SchemaWikiTask, TenantID: "t1", KnowledgeBaseID: "kb1", EvidencePackageID: "pkg-1",
		ProjectionRunID: runID, ProjectionType: ProjectionTypeWiki, Stage: stage, IdempotencyKey: key,
	}
}

func TestWikiWorkerRunsEveryStage(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	seedWikiRun(t, db, "run-w", ProjectionPending)

	if err := service.Process(ctx, wikiTaskFor(t, db, "run-w", StageCollect)); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var run models.ProjectionRun
	if err := db.First(&run, "id = ?", "run-w").Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.SourceHash == "" || run.Status != ProjectionPending {
		t.Fatalf("run after collect = %+v", run)
	}
	if err := service.Process(ctx, wikiTaskFor(t, db, "run-w", StageGenerate)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := db.First(&run, "id = ?", "run-w").Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Model == "" || run.PromptVersion == "" {
		t.Fatalf("run after generate = %+v", run)
	}
	if err := service.Process(ctx, wikiTaskFor(t, db, "run-w", StageFinalize)); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := db.First(&run, "id = ?", "run-w").Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Status != ProjectionReady {
		t.Fatalf("run after finalize = %+v", run)
	}
	var pages int64
	if err := db.Model(&models.WikiPage{}).Where("knowledge_base_id = ?", "kb1").Count(&pages).Error; err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if pages == 0 {
		t.Fatal("generate must persist wiki pages")
	}
}

func TestWikiWorkerTerminalAndIdempotentPaths(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()

	if err := service.Process(ctx, Task{ProjectionType: ProjectionTypeWiki}); err == nil {
		t.Fatal("invalid envelope must fail")
	}
	if err := service.Process(ctx, Task{ProjectionType: "unsupported"}); err == nil {
		t.Fatal("unsupported projection type must fail")
	}

	seedWikiRun(t, db, "run-ready", ProjectionReady)
	if err := service.Process(ctx, wikiTaskFor(t, db, "run-ready", StageCollect)); err != nil {
		t.Fatalf("ready run must settle: %v", err)
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", "run-ready:"+StageCollect).Error; err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	if inbox.Status != inboxSucceeded {
		t.Fatalf("ready inbox status = %s", inbox.Status)
	}

	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "missing-run:" + StageCollect, TaskType: "projection.wiki.collect",
		Status: "PROCESSING", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed missing-run inbox: %v", err)
	}
	if err := service.Process(ctx, Task{
		SchemaVersion: SchemaWikiTask, TenantID: "t1", KnowledgeBaseID: "kb1",
		ProjectionRunID: "missing", ProjectionType: ProjectionTypeWiki, Stage: StageCollect,
		IdempotencyKey: "missing-run:" + StageCollect,
	}); err != nil {
		t.Fatalf("missing run must settle: %v", err)
	}

	seedWikiRun(t, db, "run-transition", ProjectionPending)
	if err := service.Process(ctx, wikiTaskFor(t, db, "run-transition", StageFinalize)); err != nil {
		t.Fatalf("invalid transition must settle: %v", err)
	}
	var transitionInbox models.TaskInbox
	if err := db.First(&transitionInbox, "idempotency_key = ?", "run-transition:"+StageFinalize).Error; err != nil {
		t.Fatalf("load transition inbox: %v", err)
	}
	if transitionInbox.Status != inboxFailed {
		t.Fatalf("transition inbox status = %s", transitionInbox.Status)
	}
}
