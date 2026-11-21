// Package projectionintent owns the durable projection change log. Both
// ingestion (evidence publish, delete, version replacement) and the projection
// domain append changes; keeping the writer here avoids an import cycle while
// still sharing one epoch counter and one idempotency rule.
package projectionintent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KindAdd    = "add"
	KindUpdate = "update"
	KindDelete = "delete"
	KindACL    = "acl"
)

// Input describes one durable projection intent. IdempotencyKey should be
// stable across retries; evidence changes derive one from the version and
// content hash, while ACL and bulk operations must supply their own.
type Input struct {
	TenantID          string
	KnowledgeBaseID   string
	Kind              string
	DocumentID        string
	DocumentVersionID string
	EvidencePackageID string
	ContentHash       string
	IdempotencyKey    string
}

func (input Input) invalidates() bool {
	return input.Kind == KindDelete || input.Kind == KindACL
}

func (input Input) validate() error {
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.KnowledgeBaseID) == "" {
		return fmt.Errorf("projection change requires tenant and knowledge base")
	}
	switch input.Kind {
	case KindAdd, KindUpdate, KindDelete, KindACL:
	default:
		return fmt.Errorf("unsupported projection change kind %q", input.Kind)
	}
	_, err := input.key()
	return err
}

func (input Input) key() (string, error) {
	if value := strings.TrimSpace(input.IdempotencyKey); value != "" {
		return value, nil
	}
	switch input.Kind {
	case KindAdd, KindUpdate, KindDelete:
		key := strings.TrimSpace(input.DocumentVersionID)
		if key == "" {
			key = strings.TrimSpace(input.DocumentID)
		}
		if key == "" {
			return "", fmt.Errorf("projection change %q needs an idempotency key", input.Kind)
		}
		return input.Kind + ":" + key + ":" + strings.TrimSpace(input.ContentHash), nil
	default:
		return "", fmt.Errorf("projection change %q needs an explicit idempotency key", input.Kind)
	}
}

// Record appends one change in its own tenant transaction.
func Record(ctx context.Context, db *gorm.DB, input Input) (int64, error) {
	if err := input.validate(); err != nil {
		return 0, err
	}
	var epoch int64
	err := tenantdb.InTx(ctx, db, input.TenantID, func(tx *gorm.DB) error {
		var err error
		epoch, err = RecordTx(tx, input)
		return err
	})
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// RecordTx appends one change on an existing transaction. Callers that mutate
// evidence in a transaction use this to keep the delete/replace and its
// projection intent atomic.
func RecordTx(tx *gorm.DB, input Input) (int64, error) {
	if err := input.validate(); err != nil {
		return 0, err
	}
	key, _ := input.key()
	now := time.Now().UTC()
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.KnowledgeProjectionState{
		TenantID: input.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, UpdatedAt: now,
	}).Error; err != nil {
		return 0, fmt.Errorf("ensure projection state: %w", err)
	}
	var existing models.ProjectionChange
	lookup := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND idempotency_key = ?",
		input.TenantID, input.KnowledgeBaseID, key).First(&existing)
	if lookup.Error == nil {
		return existing.Epoch, nil
	}
	if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return 0, lookup.Error
	}
	invalidating := input.invalidates()
	update := `UPDATE knowledge_projection_states
		SET target_epoch = target_epoch + 1,
		    last_change_at = ?,
		    invalidated_at = CASE WHEN ? THEN ? ELSE invalidated_at END,
		    updated_at = ?
		WHERE tenant_id = ? AND knowledge_base_id = ?
		RETURNING target_epoch`
	var epoch int64
	if err := tx.Raw(update, now, invalidating, now, now, input.TenantID, input.KnowledgeBaseID).Scan(&epoch).Error; err != nil {
		return 0, fmt.Errorf("advance projection epoch: %w", err)
	}
	change := models.ProjectionChange{
		ID:       stableID("pchg", input.KnowledgeBaseID, key, fmt.Sprint(epoch)),
		TenantID: input.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, Kind: input.Kind,
		DocumentID: input.DocumentID, DocumentVersionID: input.DocumentVersionID,
		EvidencePackageID: input.EvidencePackageID, ContentHash: input.ContentHash,
		Epoch: epoch, IdempotencyKey: key, CreatedAt: now,
	}
	if err := tx.Create(&change).Error; err != nil {
		return 0, fmt.Errorf("append projection change: %w", err)
	}
	return epoch, nil
}

// Pending returns unapplied changes in epoch order.
func Pending(ctx context.Context, db *gorm.DB, tenantID, knowledgeBaseID string, limit int) ([]models.ProjectionChange, error) {
	if limit <= 0 {
		limit = 100
	}
	var changes []models.ProjectionChange
	err := tenantdb.MustScoped(db.WithContext(ctx), tenantID).
		Where("knowledge_base_id = ? AND applied_at IS NULL", knowledgeBaseID).
		Order("epoch").Limit(limit).Find(&changes).Error
	return changes, err
}

// HasBlockingChanges reports delete/ACL intents that are not applied yet. They
// gate wiki and graph reads regardless of which generation is ACTIVE.
func HasBlockingChanges(ctx context.Context, db *gorm.DB, tenantID, knowledgeBaseID string) (bool, error) {
	var count int64
	err := tenantdb.MustScoped(db.WithContext(ctx), tenantID).Model(&models.ProjectionChange{}).
		Where("knowledge_base_id = ? AND applied_at IS NULL AND kind IN ?", knowledgeBaseID, []string{KindDelete, KindACL}).
		Count(&count).Error
	return count > 0, err
}

// MarkApplied moves every change up to epoch into the applied set.
func MarkApplied(ctx context.Context, db *gorm.DB, tenantID, knowledgeBaseID string, epoch int64) error {
	if epoch <= 0 {
		return nil
	}
	return tenantdb.MustScoped(db.WithContext(ctx), tenantID).
		Model(&models.ProjectionChange{}).
		Where("knowledge_base_id = ? AND applied_at IS NULL AND epoch <= ?", knowledgeBaseID, epoch).
		Update("applied_at", time.Now().UTC()).Error
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:])[:48]
}
