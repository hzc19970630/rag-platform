package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixedQuotaResolver struct {
	limits tenantquota.Limits
	err    error
}

func (r fixedQuotaResolver) Limits(context.Context, string) (tenantquota.Limits, error) {
	return r.limits, r.err
}

func TestCheckDailyQuotaUsesResolvedTenantLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Message{}); err != nil {
		t.Fatalf("migrate messages: %v", err)
	}
	now := time.Now()
	if err := db.Create(&[]models.Message{
		{ID: "msg-1", ConversationID: "conv-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "user-a", RequestID: "r1", Role: MessageRoleUser, Status: MessageStatusCompleted, CreatedAt: now},
		{ID: "msg-2", ConversationID: "conv-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "user-a", RequestID: "r2", Role: MessageRoleUser, Status: MessageStatusCompleted, CreatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	newService := func(limits tenantquota.Limits) *Service {
		return NewService(Options{
			Stores:        Stores{Messages: NewPostgresMessageStore(db)},
			QuotaResolver: fixedQuotaResolver{limits: limits},
		})
	}
	if err := newService(tenantquota.Limits{ChatRequestsPerDay: 3}).checkDailyQuota(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("quota with headroom = %v", err)
	}
	if err := newService(tenantquota.Limits{ChatRequestsPerDay: 2}).checkDailyQuota(context.Background(), "tenant-a"); !errors.Is(err, apperror.ErrQuotaExceeded) {
		t.Fatalf("exhausted quota error = %v, want quota exceeded", err)
	}
	// Zero at the tenant tier is an explicit unlimited, not a fallback to the
	// deployment default.
	if err := newService(tenantquota.Limits{ChatRequestsPerDay: 0}).checkDailyQuota(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("explicit unlimited quota = %v", err)
	}

	failing := NewService(Options{
		Stores:        Stores{Messages: NewPostgresMessageStore(db)},
		QuotaResolver: fixedQuotaResolver{err: errors.New("quota store down")},
	})
	if err := failing.checkDailyQuota(context.Background(), "tenant-a"); err == nil {
		t.Fatal("a quota resolver failure must not silently allow generation")
	}
}
