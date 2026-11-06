package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAuthenticateRejectsRevokedSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.AuthSession{}); err != nil {
		t.Fatalf("migrate auth sessions: %v", err)
	}
	service, err := NewService(db, AuthOptions{
		SigningKeys:       []string{"test:0123456789abcdef"},
		ActiveKeyID:       "test",
		SessionTTLSeconds: 3600,
		JWTIssuer:         "rag-platform",
		JWTAudience:       "rag-platform-api",
	})
	if err != nil {
		t.Fatalf("new identity service: %v", err)
	}
	now := time.Now().UTC()
	session := models.AuthSession{
		ID: "session-1", UserID: "user-1", TenantID: "tenant-1", TokenID: "token-1",
		ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	raw, err := service.signer.issue(sessionClaims{
		Subject: "user-1", TenantID: "tenant-1", SessionID: session.ID, TokenID: session.TokenID,
		ExpiresAt: session.ExpiresAt, IssuedAt: now,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	ctx := context.Background()
	if _, err := service.Authenticate(ctx, raw); err != nil {
		t.Fatalf("authenticate live session: %v", err)
	}

	revoked := time.Now().UTC()
	if err := db.Model(&models.AuthSession{}).Where("id = ?", session.ID).
		Updates(map[string]any{"revoked_at": &revoked, "updated_at": revoked}).Error; err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := service.Authenticate(ctx, raw); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("revoked session must be unauthenticated, got %v", err)
	}
}
