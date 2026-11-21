package projectionintent

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newIntentFixture(t *testing.T) *gorm.DB {
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
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestRecordAdvancesEpochAndDedupes(t *testing.T) {
	db := newIntentFixture(t)
	ctx := context.Background()
	input := Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindAdd,
		DocumentID: "d1", DocumentVersionID: "v1", ContentHash: "h1"}
	first, err := Record(ctx, db, input)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	second, err := Record(ctx, db, input)
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("epochs = %d,%d, want 1,1", first, second)
	}
	third, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindUpdate,
		DocumentID: "d1", DocumentVersionID: "v2", ContentHash: "h2"})
	if err != nil || third != 2 {
		t.Fatalf("second change = %d, %v", third, err)
	}
	pending, err := Pending(ctx, db, "t1", "kb1", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if err := MarkApplied(ctx, db, "t1", "kb1", 1); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	pending, err = Pending(ctx, db, "t1", "kb1", 10)
	if err != nil || len(pending) != 1 || pending[0].Epoch != 2 {
		t.Fatalf("pending after apply = %+v, %v", pending, err)
	}
}

func TestRecordDeleteInvalidatesAndACLNeedsKey(t *testing.T) {
	db := newIntentFixture(t)
	ctx := context.Background()
	if _, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindAdd,
		DocumentID: "d1", DocumentVersionID: "v1", ContentHash: "h1"}); err != nil {
		t.Fatalf("record add: %v", err)
	}
	if _, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindDelete,
		DocumentID: "d1", DocumentVersionID: "v1"}); err != nil {
		t.Fatalf("record delete: %v", err)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.InvalidatedAt == nil || state.TargetEpoch != 2 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindACL}); err == nil {
		t.Fatalf("acl without key must fail")
	}
	if _, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindACL, IdempotencyKey: "acl:2"}); err != nil {
		t.Fatalf("acl with key: %v", err)
	}
	if _, err := Record(ctx, db, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: "bogus", IdempotencyKey: "x"}); err == nil {
		t.Fatalf("unknown kind must fail")
	}
}

func TestRecordTxRollsBackWithTransaction(t *testing.T) {
	db := newIntentFixture(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := RecordTx(tx, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindAdd,
			DocumentVersionID: "v1", ContentHash: "h1"}); err != nil {
			return err
		}
		return context.Canceled
	})
	if err == nil {
		t.Fatalf("transaction should roll back")
	}
	var count int64
	if err := db.Model(&models.ProjectionChange{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("changes after rollback = %d", count)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := RecordTx(tx, Input{TenantID: "t1", KnowledgeBaseID: "kb1", Kind: KindAdd,
			DocumentVersionID: "v1", ContentHash: "h1"})
		return err
	}); err != nil {
		t.Fatalf("record tx: %v", err)
	}
	if err := db.Model(&models.ProjectionChange{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("changes = %d, %v", count, err)
	}
}
