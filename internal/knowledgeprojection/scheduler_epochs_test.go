package knowledgeprojection

import (
	"context"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/projectionintent"
)

func TestBatchSchedulerRespectsQuietWindow(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record change: %v", err)
	}
	if started, err := service.SchedulePendingChanges(ctx, 10); err != nil {
		t.Fatalf("schedule inside window: %v", err)
	} else if started != 0 {
		t.Fatalf("scheduled %d runs inside quiet window, want 0", started)
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := db.Model(&models.KnowledgeProjectionState{}).Where("knowledge_base_id = ?", "kb1").
		Update("last_change_at", past).Error; err != nil {
		t.Fatalf("age change: %v", err)
	}
	started, err := service.SchedulePendingChanges(ctx, 10)
	if err != nil {
		t.Fatalf("schedule after window: %v", err)
	}
	if started != 2 {
		t.Fatalf("scheduled %d runs, want 2 (wiki+relation)", started)
	}
	var runs []models.ProjectionRun
	if err := db.Order("projection_type").Find(&runs).Error; err != nil {
		t.Fatalf("load runs: %v", err)
	}
	for _, run := range runs {
		if run.TargetEpoch != 1 || run.BaseEpoch != 0 {
			t.Fatalf("run epochs = base %d target %d, want 0/1", run.BaseEpoch, run.TargetEpoch)
		}
	}
}

func TestStaleRunIsSuperseded(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record first change: %v", err)
	}
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: "run-1", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeWiki,
		Status: ProjectionRunning, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-2", IdempotencyKey: "package:pkg-2",
	}); err != nil {
		t.Fatalf("record second change: %v", err)
	}
	superseded, err := service.supersedeStaleRun(ctx, &run)
	if err != nil {
		t.Fatalf("supersede stale run: %v", err)
	}
	if !superseded {
		t.Fatalf("run target 1 with state target 2 must be superseded")
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if stored.Status != ProjectionSuperseded {
		t.Fatalf("run status = %s, want %s", stored.Status, ProjectionSuperseded)
	}
}

func TestMarkProjectionAppliedClearsInvalidation(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: projectionintent.KindDelete,
		DocumentID: "d1", DocumentVersionID: "dv1",
	}); err != nil {
		t.Fatalf("record delete: %v", err)
	}
	var before models.KnowledgeProjectionState
	if err := db.First(&before, "knowledge_base_id = ?", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if before.InvalidatedAt == nil {
		t.Fatalf("delete must invalidate the state")
	}
	if err := service.markProjectionApplied(ctx, "t1", "kb1", ProjectionTypeWiki, before.TargetEpoch); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	var after models.KnowledgeProjectionState
	if err := db.First(&after, "knowledge_base_id = ?", "kb1").Error; err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if after.InvalidatedAt != nil {
		t.Fatalf("applied target must clear invalidation")
	}
	if after.WikiAppliedEpoch != before.TargetEpoch {
		t.Fatalf("wiki applied = %d, want %d", after.WikiAppliedEpoch, before.TargetEpoch)
	}
	var pending int64
	if err := db.Model(&models.ProjectionChange{}).Where("applied_at IS NULL").Count(&pending).Error; err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending changes = %d, want 0", pending)
	}
}
