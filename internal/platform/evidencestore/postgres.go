// Package evidencestore is the PostgreSQL implementation of the evidence.Reader
// port. It owns the evidence_packages query and returns the published contract
// type, so projections do not read the ingestion tables directly.
package evidencestore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/contracts/evidence"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

var _ evidence.Reader = (*Store)(nil)

// CurrentPackages returns only packages that still describe the current,
// evidence-ready, non-deleted document version.
func (s *Store) CurrentPackages(ctx context.Context, tenantID, knowledgeBaseID string) ([]evidence.Package, error) {
	var rows []models.EvidencePackage
	if err := s.db.WithContext(ctx).Model(&models.EvidencePackage{}).
		Joins("JOIN document_versions dv ON dv.id = evidence_packages.document_version_id").
		Joins("JOIN documents d ON d.id = dv.document_id").
		Where("evidence_packages.tenant_id = ? AND evidence_packages.knowledge_base_id = ? AND evidence_packages.status = ?", tenantID, knowledgeBaseID, status.EvidenceReady).
		Where("d.latest_version_id = evidence_packages.document_version_id AND d.status = ? AND d.deleted_at IS NULL", status.EvidenceReady).
		Order("evidence_packages.document_version_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load evidence packages: %w", err)
	}
	packages := make([]evidence.Package, 0, len(rows))
	for _, row := range rows {
		var pkg evidence.Package
		if err := json.Unmarshal([]byte(row.PackageJSON), &pkg); err != nil {
			return nil, apperror.Terminalf("schema_incompatible", "evidence package %s is unreadable", row.ID)
		}
		pkg.ID = row.ID
		if pkg.SchemaVersion != evidence.Schema {
			return nil, apperror.Terminalf("schema_incompatible", "evidence package %s uses %s", row.ID, pkg.SchemaVersion)
		}
		if len(pkg.Chunks) == 0 {
			continue
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}
