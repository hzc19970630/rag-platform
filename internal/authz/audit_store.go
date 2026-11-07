package authz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type PostgresAuditStore struct {
	db *gorm.DB
}

func NewPostgresAuditStore(db *gorm.DB) *PostgresAuditStore {
	return &PostgresAuditStore{db: db}
}

func (s *PostgresAuditStore) MemberRole(ctx context.Context, tenantID, userID string) (string, bool, error) {
	var membership models.Membership
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		First(&membership, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("load tenant membership: %w", err)
	}
	return membership.Role, true, nil
}

func (s *PostgresAuditStore) List(ctx context.Context, tenantID string, filter AuditFilter) ([]models.AuditLog, error) {
	dbQuery := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID)
	if filter.ActorID != "" {
		dbQuery = dbQuery.Where("actor_id = ?", filter.ActorID)
	}
	if filter.Action != "" {
		dbQuery = dbQuery.Where("action = ?", filter.Action)
	}
	if filter.ResourceType != "" {
		dbQuery = dbQuery.Where("resource_type = ?", filter.ResourceType)
	}
	if filter.ResourceID != "" {
		dbQuery = dbQuery.Where("resource_id = ?", filter.ResourceID)
	}
	if filter.Allowed != nil {
		dbQuery = dbQuery.Where("allowed = ?", *filter.Allowed)
	}
	var rows []models.AuditLog
	if err := dbQuery.Order("id DESC").Limit(filter.Limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	return rows, nil
}

func (s *PostgresAuditStore) Purge(ctx context.Context, before time.Time, limit int) (int64, error) {
	result := tenantdb.Unscoped(s.db.WithContext(ctx), "retention: purge expired audit logs across tenants").Exec(`
		DELETE FROM audit_logs
		WHERE id IN (
			SELECT id FROM audit_logs WHERE created_at < ? ORDER BY created_at, id LIMIT ?
		)`, before, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge audit logs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *PostgresAuditStore) Create(ctx context.Context, entry models.AuditLog) error {
	return s.db.WithContext(ctx).Create(&entry).Error
}
