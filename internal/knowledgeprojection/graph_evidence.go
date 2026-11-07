package knowledgeprojection

import (
	"context"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
)

// graph_evidence.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

func sourceKey(relationID, chunkID string) string {
	return relationID + "\x00" + chunkID
}

// validateGraphSources returns the sources that still describe current evidence
// and, when filterSources is set, the subset the caller may read.
func (s *Service) validateGraphSources(ctx context.Context, scope authz.AuthorizationScope, sources []relationstore.Source, filterSources bool) (map[string]bool, map[string]bool, error) {
	readable := map[string]bool{}
	if len(sources) == 0 {
		return map[string]bool{}, readable, nil
	}
	refs := make([]relationstore.SourceRef, 0, len(sources))
	for _, source := range sources {
		refs = append(refs, relationstore.SourceRef{
			RelationID: source.RelationID, TenantID: source.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
			DocumentID: source.DocumentID, DocumentVersionID: source.DocumentVersionID, ChunkID: source.ChunkID,
		})
	}
	valid, err := s.currentEvidence(ctx, refs)
	if err != nil {
		return nil, nil, err
	}
	if !filterSources {
		return valid, readable, nil
	}
	authRefs := make([]authz.ResourceRef, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		if !valid[sourceKey(source.RelationID, source.ChunkID)] || seen[source.ChunkID] {
			continue
		}
		seen[source.ChunkID] = true
		authRefs = append(authRefs, authz.ResourceRef{
			TenantID: scope.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
			ResourceID: source.ChunkID, ResourceType: "chunk",
		})
	}
	allowed, err := s.authz.FilterReadable(ctx, scope, authRefs)
	if err != nil {
		return nil, nil, err
	}
	for _, ref := range allowed {
		readable[ref.ResourceID] = true
	}
	return valid, readable, nil
}

// currentEvidence verifies that every source reference still points at the
// current, evidence-ready chunk of its document version. The Relation Store
// only supplies references; this invariant belongs to the evidence domain.

// currentEvidence verifies that every source reference still points at the
// current, evidence-ready chunk of its document version. The Relation Store
// only supplies references; this invariant belongs to the evidence domain.
func (s *Service) currentEvidence(ctx context.Context, refs []relationstore.SourceRef) (map[string]bool, error) {
	valid := map[string]bool{}
	if len(refs) == 0 {
		return valid, nil
	}
	chunkIDs := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.ChunkID == "" || seen[ref.ChunkID] {
			continue
		}
		seen[ref.ChunkID] = true
		chunkIDs = append(chunkIDs, ref.ChunkID)
	}
	if len(chunkIDs) == 0 {
		return valid, nil
	}
	var rows []struct {
		ChunkID           string `gorm:"column:chunk_id"`
		KnowledgeBaseID   string `gorm:"column:knowledge_base_id"`
		DocumentID        string `gorm:"column:document_id"`
		DocumentVersionID string `gorm:"column:document_version_id"`
	}
	if err := s.db.WithContext(ctx).Table("chunks c").
		Select("c.id AS chunk_id, c.knowledge_base_id, dv.document_id, c.document_version_id").
		Joins("JOIN document_versions dv ON dv.id = c.document_version_id").
		Joins("JOIN documents d ON d.id = dv.document_id").
		Where("c.id IN ?", chunkIDs).
		Where("d.latest_version_id = c.document_version_id AND d.deleted_at IS NULL").
		Where("d.status = ? AND dv.status = ? AND c.status = ?",
			ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("validate graph sources: %w", err)
	}
	current := make(map[string]bool, len(rows))
	for _, row := range rows {
		current[evidenceKey(row.ChunkID, row.DocumentID, row.DocumentVersionID, row.KnowledgeBaseID)] = true
	}
	for _, ref := range refs {
		if current[evidenceKey(ref.ChunkID, ref.DocumentID, ref.DocumentVersionID, ref.KnowledgeBaseID)] {
			valid[sourceKey(ref.RelationID, ref.ChunkID)] = true
		}
	}
	return valid, nil
}

func evidenceKey(chunkID, documentID, documentVersionID, knowledgeBaseID string) string {
	return strings.Join([]string{chunkID, documentID, documentVersionID, knowledgeBaseID}, "\x00")
}
