// Package projectioncache is the PostgreSQL adapter for the per-package
// projection generation cache. It stores one row per package and projection
// type; a changed prompt/model/ACL overwrites the previous variant.
package projectioncache

import (
	"context"
	"errors"
	"fmt"
	"time"

	cachecontract "github.com/example/rag-platform/internal/contracts/projectioncache"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, key cachecontract.Key) ([]byte, bool, error) {
	var row models.ProjectionGenerationCache
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND projection_type = ? AND artifact = ? AND package_id = ? AND input_hash = ?",
			key.TenantID, key.KnowledgeBaseID, key.ProjectionType, key.Artifact, key.PackageID, key.InputHash).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load projection generation cache: %w", err)
	}
	return []byte(row.Payload), true, nil
}

func (s *Store) Put(ctx context.Context, key cachecontract.Key, payload []byte) error {
	now := time.Now()
	row := models.ProjectionGenerationCache{
		TenantID: key.TenantID, KnowledgeBaseID: key.KnowledgeBaseID,
		ProjectionType: key.ProjectionType, Artifact: key.Artifact, PackageID: key.PackageID,
		InputHash: key.InputHash, Payload: string(payload),
		PromptVersion: key.PromptVersion, Model: key.Model, ACLVersion: key.ACLVersion,
		UpdatedAt: now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"}, {Name: "knowledge_base_id"},
			{Name: "projection_type"}, {Name: "artifact"}, {Name: "package_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"input_hash", "payload", "prompt_version", "model", "acl_version", "updated_at",
		}),
	}).Create(&row).Error
}

// DeleteScope clears a knowledge base's cache for one projection type. It is
// called before a manual rebuild so the operator's explicit rebuild is a full
// regeneration, not a cache replay.
func (s *Store) DeleteScope(ctx context.Context, tenantID, knowledgeBaseID, projectionType string) error {
	return s.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND projection_type = ?", tenantID, knowledgeBaseID, projectionType).
		Delete(&models.ProjectionGenerationCache{}).Error
}

// PurgeOrphans removes rows whose EvidencePackage no longer exists. It is the
// maintenance sweep that bounds the table after document purges; current
// packages stay cached indefinitely so an old but unchanged document does not
// turn the next run into a full rebuild.
func (s *Store) PurgeOrphans(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	result := s.db.WithContext(ctx).Exec(`
		DELETE FROM projection_generation_cache
		WHERE package_id IN (
			SELECT c.package_id
			FROM projection_generation_cache c
			WHERE NOT EXISTS (
				SELECT 1 FROM evidence_packages p WHERE p.id = c.package_id
			)
			LIMIT ?
		)`, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge projection generation cache: %w", result.Error)
	}
	return result.RowsAffected, nil
}
