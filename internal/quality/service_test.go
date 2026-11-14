package quality

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openQualityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "quality.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Conversation{}, &models.Message{}, &models.RetrievalFeedback{}, &models.RetrievalEvalRun{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC()
	conversation := models.Conversation{ID: "conv-1", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", UserID: "user-1", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	message := models.Message{
		ID: "msg-1", ConversationID: "conv-1", TenantID: "tenant-1", KnowledgeBaseID: "kb-1",
		UserID: "user-1", RequestID: "req-1", Role: "assistant", Status: "DELIVERING",
		Content: "answer", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	return db
}

func TestRatingUpdatesOneRowAndClickIsIdempotent(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	principal := authz.Principal{SubjectID: "user-1", TenantID: "tenant-1"}
	ctx := context.Background()

	if _, err := service.Submit(ctx, principal, "conv-1", "msg-1", SubmitInput{Kind: KindHelpful}); err != nil {
		t.Fatalf("submit helpful: %v", err)
	}
	if _, err := service.Submit(ctx, principal, "conv-1", "msg-1", SubmitInput{Kind: KindUnhelpful}); err != nil {
		t.Fatalf("submit unhelpful: %v", err)
	}
	var ratings int64
	if err := db.Model(&models.RetrievalFeedback{}).Where("kind IN ?", []string{KindHelpful, KindUnhelpful}).Count(&ratings).Error; err != nil {
		t.Fatalf("count ratings: %v", err)
	}
	if ratings != 1 {
		t.Fatalf("expected one rating row, got %d", ratings)
	}

	for i := 0; i < 2; i++ {
		if _, err := service.Submit(ctx, principal, "conv-1", "msg-1", SubmitInput{Kind: KindCitationClick, ChunkID: "chunk-1"}); err != nil {
			t.Fatalf("submit click %d: %v", i, err)
		}
	}
	var clicks int64
	if err := db.Model(&models.RetrievalFeedback{}).Where("kind = ?", KindCitationClick).Count(&clicks).Error; err != nil {
		t.Fatalf("count clicks: %v", err)
	}
	if clicks != 1 {
		t.Fatalf("expected one click row, got %d", clicks)
	}

	summary, err := service.Summary(ctx, "tenant-1", "kb-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Helpful != 0 || summary.Unhelpful != 1 || summary.CitationClicks != 1 || summary.Messages != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.HelpfulRate != 0 {
		t.Fatalf("helpful rate = %v, want 0", summary.HelpfulRate)
	}
}

func TestFeedbackRejectsAnotherTenant(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	_, err := service.Submit(context.Background(), authz.Principal{SubjectID: "user-1", TenantID: "tenant-2"}, "conv-1", "msg-1", SubmitInput{Kind: KindHelpful})
	if !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestFeedbackRejectsUserMessageAndUnknownKind(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	principal := authz.Principal{SubjectID: "user-1", TenantID: "tenant-1"}
	if _, err := service.Submit(context.Background(), principal, "conv-1", "msg-1", SubmitInput{Kind: "thumbs"}); err == nil {
		t.Fatal("expected invalid kind")
	}
	if _, err := service.Submit(context.Background(), principal, "conv-1", "missing", SubmitInput{Kind: KindHelpful}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected not found for missing message, got %v", err)
	}
}

func TestEvalRunMarksRegressionAgainstPreviousRun(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	ctx := context.Background()
	first, err := service.RecordEvalRun(ctx, "tenant-1", EvalRunInput{
		Kind: "m3", KnowledgeBaseID: "kb-1", Status: EvalStatusPassed,
		Metrics: map[string]float64{"documentHitRate": 0.92, "citationAccuracy": 1},
		Report:  []byte(`{"schemaVersion":"test"}`),
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != EvalStatusPassed {
		t.Fatalf("first status = %s", first.Status)
	}
	second, err := service.RecordEvalRun(ctx, "tenant-1", EvalRunInput{
		Kind: "m3", KnowledgeBaseID: "kb-1", Status: EvalStatusPassed,
		Metrics: map[string]float64{"documentHitRate": 0.80, "citationAccuracy": 1},
		Report:  []byte(`{"schemaVersion":"test"}`),
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != EvalStatusRegressed {
		t.Fatalf("second status = %s, want regressed", second.Status)
	}
	summary, err := service.Summary(ctx, "tenant-1", "kb-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.QualityStatus != EvalStatusRegressed || summary.LatestEval == nil {
		t.Fatalf("unexpected quality summary: %+v", summary)
	}
}

func TestRequireRecentEvalBlocksUntilAReportExists(t *testing.T) {
	db := openQualityDB(t)
	service := NewService(db)
	ctx := context.Background()
	if err := service.RequireRecentEval(ctx, "tenant-1", "kb-1"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("expected conflict without an eval run, got %v", err)
	}
	if _, err := service.RecordEvalRun(ctx, "tenant-1", EvalRunInput{
		Kind: "m3", KnowledgeBaseID: "kb-1", Status: EvalStatusPassed,
		Metrics: map[string]float64{"documentHitRate": 0.9}, Report: []byte(`{}`),
	}); err != nil {
		t.Fatalf("record eval run: %v", err)
	}
	if err := service.RequireRecentEval(ctx, "tenant-1", "kb-1"); err != nil {
		t.Fatalf("recent run must satisfy the gate: %v", err)
	}
}
