package datasource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Cancelling a sync stops ingestion for a whole connector or knowledge base, so
// it stays an operator-only session action.
func TestSyncCancelRejectsAPIKeyAndNonOperator(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Membership{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	now := time.Now()
	if err := db.Create(&models.Membership{TenantID: "t1", UserID: "editor", Role: "editor", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed editor: %v", err)
	}
	authorizer, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	service := &Service{db: db, authz: authorizer}
	cases := []struct {
		name      string
		principal authz.Principal
	}{
		{"api key", authz.Principal{
			SubjectID: "u1", TenantID: "t1", CredentialType: "api_key", CredentialID: "key-1",
			Capabilities: []string{"knowledge_base:write", "knowledge_base:manage_members"},
		}},
		{"editor session", authz.Principal{SubjectID: "editor", TenantID: "t1", CredentialType: "session"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := service.Runs().CancelRun(ctx, testCase.principal, "conn-1", "run-1"); !errors.Is(err, apperror.ErrForbidden) {
				t.Fatalf("CancelRun must be forbidden, got %v", err)
			}
			if _, err := service.Runs().CancelConnector(ctx, testCase.principal, "conn-1"); !errors.Is(err, apperror.ErrForbidden) {
				t.Fatalf("CancelConnector must be forbidden, got %v", err)
			}
			if _, err := service.Runs().CancelKnowledgeBase(ctx, testCase.principal, "kb-1"); !errors.Is(err, apperror.ErrForbidden) {
				t.Fatalf("CancelKnowledgeBase must be forbidden, got %v", err)
			}
		})
	}
}
