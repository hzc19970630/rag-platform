// Package evidencestore is the PostgreSQL implementation of the evidence.Reader
// port. It owns the evidence_packages query and returns the published contract
// type, so projections do not read the ingestion tables directly.
package evidencestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

var _ evidence.Reader = (*Store)(nil)

// currentPackages narrows evidence_packages to the current, evidence-ready,
// non-deleted document version. Both the batch and single-package readers must
// apply exactly the same predicate.
func (s *Store) currentPackages(ctx context.Context, tenantID, knowledgeBaseID string) *gorm.DB {
	return s.db.WithContext(ctx).Model(&models.EvidencePackage{}).
		Joins("JOIN document_versions dv ON dv.id = evidence_packages.document_version_id").
		Joins("JOIN documents d ON d.id = dv.document_id").
		Where("evidence_packages.tenant_id = ? AND evidence_packages.knowledge_base_id = ? AND evidence_packages.status = ?", tenantID, knowledgeBaseID, status.EvidenceReady).
		Where("d.latest_version_id = evidence_packages.document_version_id AND d.status = ? AND d.deleted_at IS NULL", status.EvidenceReady)
}

// CurrentPackages returns only packages that still describe the current,
// evidence-ready, non-deleted document version.
func (s *Store) CurrentPackages(ctx context.Context, tenantID, knowledgeBaseID string) ([]evidence.Package, error) {
	var rows []models.EvidencePackage
	if err := s.currentPackages(ctx, tenantID, knowledgeBaseID).
		Order("evidence_packages.document_version_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load evidence packages: %w", err)
	}
	packages := make([]evidence.Package, 0, len(rows))
	for _, row := range rows {
		pkg, err := decodePackage(row)
		if err != nil {
			return nil, err
		}
		if len(pkg.Chunks) == 0 {
			continue
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

// CurrentPackage returns one package under the same currency predicate. A
// missing row means the evidence changed, not that the caller made a mistake.
func (s *Store) CurrentPackage(ctx context.Context, tenantID, knowledgeBaseID, packageID string) (evidence.Package, bool, error) {
	var row models.EvidencePackage
	err := s.currentPackages(ctx, tenantID, knowledgeBaseID).
		Where("evidence_packages.id = ?", packageID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return evidence.Package{}, false, nil
	}
	if err != nil {
		return evidence.Package{}, false, fmt.Errorf("load evidence package: %w", err)
	}
	pkg, decodeErr := decodePackage(row)
	if decodeErr != nil {
		return evidence.Package{}, false, decodeErr
	}
	return pkg, len(pkg.Chunks) > 0, nil
}

func decodePackage(row models.EvidencePackage) (evidence.Package, error) {
	var pkg evidence.Package
	if err := json.Unmarshal([]byte(row.PackageJSON), &pkg); err != nil {
		return evidence.Package{}, apperror.Terminalf("schema_incompatible", "evidence package %s is unreadable", row.ID)
	}
	pkg.ID = row.ID
	if pkg.SchemaVersion != evidence.Schema {
		return evidence.Package{}, apperror.Terminalf("schema_incompatible", "evidence package %s uses %s", row.ID, pkg.SchemaVersion)
	}
	return pkg, nil
}
