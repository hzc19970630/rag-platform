package knowledgeprojection

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newChangeFixture(t *testing.T) (*Service, *gorm.DB) {
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
	if err := db.AutoMigrate(&models.KnowledgeProjectionState{}, &models.ProjectionChange{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return NewService(Options{DB: db}), db
}

func TestRecordChangeAdvancesEpoch(t *testing.T) {
	service, db := newChangeFixture(t)
	ctx := context.Background()

	first, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeAdd,
		DocumentID: "doc-1", DocumentVersionID: "ver-1", ContentHash: "h1",
	})
	if err != nil {
		t.Fatalf("record add: %v", err)
	}
	second, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeUpdate,
		DocumentID: "doc-1", DocumentVersionID: "ver-2", ContentHash: "h2",
	})
	if err != nil {
		t.Fatalf("record update: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("epochs = %d,%d, want 1,2", first, second)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "tenant_id = ? AND knowledge_base_id = ?", "t1", "kb-1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.TargetEpoch != 2 || state.LastChangeAt == nil {
		t.Fatalf("state target=%d last=%v", state.TargetEpoch, state.LastChangeAt)
	}
}

func TestRecordChangeDedupesRetry(t *testing.T) {
	service, db := newChangeFixture(t)
	ctx := context.Background()
	input := ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeAdd,
		DocumentID: "doc-1", DocumentVersionID: "ver-1", ContentHash: "h1",
	}
	first, err := service.RecordChange(ctx, input)
	if err != nil {
		t.Fatalf("first change: %v", err)
	}
	second, err := service.RecordChange(ctx, input)
	if err != nil {
		t.Fatalf("duplicate change: %v", err)
	}
	if first != second {
		t.Fatalf("duplicate returned epoch %d, want %d", second, first)
	}
	var count int64
	if err := db.Model(&models.ProjectionChange{}).Count(&count).Error; err != nil {
		t.Fatalf("count changes: %v", err)
	}
	if count != 1 {
		t.Fatalf("changes = %d, want 1", count)
	}
}

func TestRecordChangeDeleteInvalidates(t *testing.T) {
	service, db := newChangeFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeAdd,
		DocumentID: "doc-1", DocumentVersionID: "ver-1", ContentHash: "h1",
	}); err != nil {
		t.Fatalf("record add: %v", err)
	}
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeDelete,
		DocumentID: "doc-1", DocumentVersionID: "ver-1",
	}); err != nil {
		t.Fatalf("record delete: %v", err)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "tenant_id = ? AND knowledge_base_id = ?", "t1", "kb-1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.InvalidatedAt == nil {
		t.Fatalf("delete must set invalidated_at")
	}
	if state.TargetEpoch != 2 {
		t.Fatalf("target epoch = %d, want 2", state.TargetEpoch)
	}
}

func TestPendingChangesAndApply(t *testing.T) {
	service, db := newChangeFixture(t)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		if _, err := service.RecordChange(ctx, ChangeInput{
			TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeUpdate,
			DocumentID: "doc-1", DocumentVersionID: "ver-1", ContentHash: string(rune('a' + index)),
		}); err != nil {
			t.Fatalf("record change %d: %v", index, err)
		}
	}
	pending, err := service.PendingChanges(ctx, "t1", "kb-1", 10)
	if err != nil {
		t.Fatalf("pending changes: %v", err)
	}
	if len(pending) != 3 || pending[0].Epoch != 1 || pending[2].Epoch != 3 {
		t.Fatalf("pending = %+v", pending)
	}
	if err := service.MarkChangesApplied(ctx, "t1", "kb-1", 2); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	var applied int64
	if err := db.Model(&models.ProjectionChange{}).Where("applied_at IS NOT NULL").Count(&applied).Error; err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}
}

func TestRecordChangeACLNeedsKey(t *testing.T) {
	service, _ := newChangeFixture(t)
	if _, err := service.RecordChange(context.Background(), ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeACL,
	}); err == nil {
		t.Fatalf("acl change without idempotency key must fail")
	}
	if _, err := service.RecordChange(context.Background(), ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb-1", Kind: ChangeACL, IdempotencyKey: "acl:7",
	}); err != nil {
		t.Fatalf("acl change with key: %v", err)
	}
}
