package quality

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func seedFeedback(t *testing.T, service *Service, rows []models.RetrievalFeedback) {
	t.Helper()
	for _, row := range rows {
		if err := service.db.Create(&row).Error; err != nil {
			t.Fatalf("seed feedback %s: %v", row.ID, err)
		}
	}
}

func TestSummaryAppliesWindowAndScope(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	now := time.Now().UTC()
	seedFeedback(t, service, []models.RetrievalFeedback{
		{ID: "fb-old", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", ConversationID: "conv-1", MessageID: "msg-old", Kind: KindHelpful, ActorID: "user-1", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		{ID: "fb-new", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", ConversationID: "conv-1", MessageID: "msg-2", Kind: KindHelpful, ActorID: "user-1", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now},
		{ID: "fb-neg", TenantID: "tenant-1", KnowledgeBaseID: "kb-2", ConversationID: "conv-2", MessageID: "msg-3", ChunkID: "chunk-9", Kind: KindUnhelpful, ActorID: "user-1", CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now},
		{ID: "fb-other", TenantID: "tenant-2", KnowledgeBaseID: "kb-1", ConversationID: "conv-9", MessageID: "msg-9", Kind: KindHelpful, ActorID: "user-9", CreatedAt: now.Add(-time.Minute), UpdatedAt: now},
	})

	summary, err := service.Summary(context.Background(), "tenant-1", "", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Helpful != 1 || summary.Unhelpful != 1 || summary.Messages != 2 {
		t.Fatalf("window/scope leak: %+v", summary)
	}
	if summary.HelpfulRate != 0.5 {
		t.Fatalf("helpful rate = %v, want 0.5", summary.HelpfulRate)
	}
	if len(summary.TopUnhelpful) != 1 || summary.TopUnhelpful[0].ChunkID != "chunk-9" {
		t.Fatalf("top unhelpful = %+v", summary.TopUnhelpful)
	}

	scoped, err := service.Summary(context.Background(), "tenant-1", "kb-1", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("scoped summary: %v", err)
	}
	if scoped.Helpful != 1 || scoped.Unhelpful != 0 || scoped.Messages != 1 {
		t.Fatalf("knowledge-base scope leak: %+v", scoped)
	}
}

func TestRegressionThresholdBoundaryAndFailedStatus(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	ctx := context.Background()
	record := func(status string, hit float64) models.RetrievalEvalRun {
		t.Helper()
		run, err := service.RecordEvalRun(ctx, "tenant-1", EvalRunInput{
			Kind: "m3", KnowledgeBaseID: "kb-1", Status: status,
			Metrics: map[string]float64{"documentHitRate": hit}, Report: []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("record %s %v: %v", status, hit, err)
		}
		return run
	}

	record(EvalStatusPassed, 0.90)
	// Exactly the threshold is still allowed; only a larger drop regresses.
	if run := record(EvalStatusPassed, 0.88); run.Status != EvalStatusPassed {
		t.Fatalf("threshold boundary status = %s, want passed", run.Status)
	}
	if run := record(EvalStatusPassed, 0.859); run.Status != EvalStatusRegressed {
		t.Fatalf("drop beyond threshold status = %s, want regressed", run.Status)
	}
	// An explicitly failed run keeps its status even when metrics also dropped.
	if run := record(EvalStatusFailed, 0.10); run.Status != EvalStatusFailed {
		t.Fatalf("failed run status = %s, want failed", run.Status)
	}
}

func TestRequireRecentEvalChecksAgeAndTenant(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := models.RetrievalEvalRun{
		ID: "eval-stale", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", Kind: "m3", Status: EvalStatusPassed,
		Metrics: `{}`, Report: `{}`, CreatedAt: now.Add(-8 * 24 * time.Hour),
	}
	otherTenant := models.RetrievalEvalRun{
		ID: "eval-other", TenantID: "tenant-2", KnowledgeBaseID: "kb-1", Kind: "m3", Status: EvalStatusPassed,
		Metrics: `{}`, Report: `{}`, CreatedAt: now.Add(-time.Hour),
	}
	for _, row := range []models.RetrievalEvalRun{stale, otherTenant} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed eval %s: %v", row.ID, err)
		}
	}
	if err := service.RequireRecentEval(ctx, "tenant-1", "kb-1"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("stale run must not satisfy the gate, got %v", err)
	}

	if _, err := service.RecordEvalRun(ctx, "tenant-1", EvalRunInput{
		Kind: "m3", KnowledgeBaseID: "", Status: EvalStatusPassed,
		Metrics: map[string]float64{"documentHitRate": 0.9}, Report: []byte(`{}`),
	}); err != nil {
		t.Fatalf("record global run: %v", err)
	}
	// A tenant-wide run covers any knowledge base in that tenant.
	if err := service.RequireRecentEval(ctx, "tenant-1", "kb-2"); err != nil {
		t.Fatalf("tenant-wide run must cover kb-2: %v", err)
	}
	if err := service.RequireRecentEval(ctx, "tenant-2", "kb-2"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("tenant-wide run must not leak to tenant-2, got %v", err)
	}
}
