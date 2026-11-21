package knowledgeprojection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/projectionintent"
	"gorm.io/gorm"
)

// InvalidateDocument is the immediate-delete entry point used by ingestion.
// It records a delete change (atomically with the row delete when the caller
// already did so), hides every wiki page and graph snapshot sourced from the
// document, and cancels active projection runs. Rebuild may lag; old content is
// not queryable in the meantime.
func (s *Service) InvalidateDocument(ctx context.Context, tenantID, knowledgeBaseID, documentID, documentVersionID, reason string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(knowledgeBaseID) == "" || strings.TrimSpace(documentID) == "" {
		return fmt.Errorf("document invalidation requires tenant, knowledge base and document")
	}
	now := time.Now()
	if s.store != nil {
		if err := s.store.MarkStaleByDocument(ctx, tenantID, knowledgeBaseID, documentID, now); err != nil {
			return fmt.Errorf("invalidate document graph snapshots: %w", err)
		}
	}
	message := strings.TrimSpace(reason)
	if message == "" {
		message = "document_deleted"
	}
	return tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if _, err := projectionintent.RecordTx(tx, projectionintent.Input{
			TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, Kind: projectionintent.KindDelete,
			DocumentID: documentID, DocumentVersionID: documentVersionID,
		}); err != nil {
			return err
		}
		pageSelect := `SELECT DISTINCT v.wiki_page_id
			FROM wiki_page_sources s JOIN wiki_page_versions v ON v.id = s.wiki_page_version_id
			WHERE s.tenant_id = ? AND s.knowledge_base_id = ? AND s.document_id = ?`
		args := []any{tenantID, knowledgeBaseID, documentID}
		if strings.TrimSpace(documentVersionID) != "" {
			pageSelect += " AND s.document_version_id = ?"
			args = append(args, documentVersionID)
		}
		updateArgs := append([]any{PageStale, now, tenantID, knowledgeBaseID}, args...)
		if err := tx.Exec(`UPDATE wiki_pages SET status = ?, updated_at = ?
			WHERE tenant_id = ? AND knowledge_base_id = ? AND id IN (`+pageSelect+`)`, updateArgs...).Error; err != nil {
			return fmt.Errorf("stale document wiki pages: %w", err)
		}
		deleteArgs := append([]any{tenantID, knowledgeBaseID}, args...)
		if err := tx.Exec(`DELETE FROM knowledge_index_records
			WHERE tenant_id = ? AND knowledge_base_id = ? AND wiki_page_id IN (`+pageSelect+`)`, deleteArgs...).Error; err != nil {
			return fmt.Errorf("drop document knowledge index: %w", err)
		}
		return tx.Model(&models.ProjectionRun{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND status IN ?", tenantID, knowledgeBaseID,
				append(activeStatusesFor(ProjectionTypeWiki), activeStatusesFor(ProjectionTypeRelation)...)).
			Updates(map[string]any{"status": ProjectionCancelled, "error_code": "invalidated", "error_message": message, "updated_at": now}).Error
	})
}
