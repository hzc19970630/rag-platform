package knowledgeprojection

import (
	"context"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/projectionintent"
)

// Projection change kinds are owned by the shared intent package so ingestion
// and projection write the same values.
const (
	ChangeAdd    = projectionintent.KindAdd
	ChangeUpdate = projectionintent.KindUpdate
	ChangeDelete = projectionintent.KindDelete
	ChangeACL    = projectionintent.KindACL
)

// ChangeInput is re-exported for callers that live inside this package.
type ChangeInput = projectionintent.Input

// RecordChange appends one change and advances target_epoch atomically.
func (s *Service) RecordChange(ctx context.Context, input ChangeInput) (int64, error) {
	return projectionintent.Record(ctx, s.db, input)
}

// NoteEvidenceChange records one published package without creating a run. The
// batch scheduler turns the accumulated intents into one run after the quiet
// window.
func (s *Service) NoteEvidenceChange(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string) error {
	_, err := projectionintent.Record(ctx, s.db, projectionintent.Input{
		TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, Kind: projectionintent.KindAdd,
		EvidencePackageID: evidencePackageID, IdempotencyKey: "package:" + evidencePackageID,
	})
	return err
}

// PendingChanges returns unapplied changes in epoch order.
func (s *Service) PendingChanges(ctx context.Context, tenantID, knowledgeBaseID string, limit int) ([]models.ProjectionChange, error) {
	return projectionintent.Pending(ctx, s.db, tenantID, knowledgeBaseID, limit)
}

// MarkChangesApplied moves every change up to epoch into the applied set.
func (s *Service) MarkChangesApplied(ctx context.Context, tenantID, knowledgeBaseID string, epoch int64) error {
	return projectionintent.MarkApplied(ctx, s.db, tenantID, knowledgeBaseID, epoch)
}
