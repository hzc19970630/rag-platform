package knowledgeprojection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/platform/vecutil"
	"gorm.io/gorm"
)

func (s *Service) pageCounts(ctx context.Context, tenantID, knowledgeBaseID string) (PageCounts, error) {
	var counts PageCounts
	if err := s.db.WithContext(ctx).Model(&models.WikiPage{}).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).Count(&counts.Total).Error; err != nil {
		return counts, err
	}
	if err := s.db.WithContext(ctx).Model(&models.WikiPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, PagePublished).Count(&counts.Published).Error; err != nil {
		return counts, err
	}
	if err := s.db.WithContext(ctx).Model(&models.WikiPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, PageReviewRequired).Count(&counts.PendingReview).Error; err != nil {
		return counts, err
	}
	if err := s.db.WithContext(ctx).Model(&models.WikiPage{}).Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, PageStale).Count(&counts.Stale).Error; err != nil {
		return counts, err
	}
	if err := s.db.WithContext(ctx).Model(&models.KnowledgeIndexRecord{}).Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, IndexActive).Count(&counts.Indexed).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

func (s *Service) ListPages(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]PageSummary, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var pages []models.WikiPage
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, knowledgeBaseID).
		Order("page_type, title").Find(&pages).Error; err != nil {
		return nil, err
	}
	versions := map[string]models.WikiPageVersion{}
	ids := make([]string, 0, len(pages)*2)
	for _, page := range pages {
		if page.CurrentVersionID != nil {
			ids = append(ids, *page.CurrentVersionID)
		}
		if page.PublishedVersionID != nil {
			ids = append(ids, *page.PublishedVersionID)
		}
	}
	if len(ids) > 0 {
		var rows []models.WikiPageVersion
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			versions[row.ID] = row
		}
	}
	result := make([]PageSummary, 0, len(pages))
	for _, page := range pages {
		summary := PageSummary{Page: page}
		if page.CurrentVersionID != nil {
			if version, ok := versions[*page.CurrentVersionID]; ok {
				summary.CurrentVersion = &version
			}
		}
		if page.PublishedVersionID != nil {
			if version, ok := versions[*page.PublishedVersionID]; ok {
				summary.PublishedVersion = &version
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func (s *Service) GetPage(ctx context.Context, principal authz.Principal, pageID string) (PageDetail, error) {
	var page models.WikiPage
	if err := s.db.WithContext(ctx).First(&page, "id = ? AND tenant_id = ?", pageID, principal.TenantID).Error; err != nil {
		return PageDetail{}, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, page.KnowledgeBaseID, "read"); err != nil {
		return PageDetail{}, err
	}
	versionID := page.CurrentVersionID
	if page.PublishedVersionID != nil {
		versionID = page.PublishedVersionID
	}
	detail := PageDetail{Page: page}
	if versionID == nil {
		return detail, nil
	}
	var version models.WikiPageVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&version, "id = ?", *versionID).Error; err != nil {
		return PageDetail{}, err
	}
	detail.Version = &version
	if err := json.Unmarshal([]byte(version.Blocks), &detail.Blocks); err != nil {
		return PageDetail{}, fmt.Errorf("decode wiki blocks: %w", err)
	}
	if err := s.db.WithContext(ctx).Where("wiki_page_version_id = ?", version.ID).Order("id").Find(&detail.Sources).Error; err != nil {
		return PageDetail{}, err
	}
	return detail, nil
}

func (s *Service) ListVersions(ctx context.Context, principal authz.Principal, pageID string) ([]models.WikiPageVersion, error) {
	var page models.WikiPage
	if err := s.db.WithContext(ctx).First(&page, "id = ? AND tenant_id = ?", pageID, principal.TenantID).Error; err != nil {
		return nil, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, page.KnowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var versions []models.WikiPageVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("wiki_page_id = ?", pageID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

func (s *Service) Review(ctx context.Context, principal authz.Principal, pageID, versionID string, input ReviewInput) (models.WikiPageVersion, error) {
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "approve" && action != "reject" {
		return models.WikiPageVersion{}, apperror.Invalidf("unsupported review action %q", input.Action)
	}
	page, version, err := s.loadPageVersion(ctx, principal.TenantID, pageID, versionID)
	if err != nil {
		return models.WikiPageVersion{}, err
	}
	if _, err := s.requireReviewer(ctx, principal, page.KnowledgeBaseID); err != nil {
		return models.WikiPageVersion{}, err
	}
	if version.ReviewStatus != ReviewPending {
		return models.WikiPageVersion{}, fmt.Errorf("%w: only pending versions can be reviewed", apperror.ErrConflict)
	}
	status := ReviewApproved
	if action == "reject" {
		status = ReviewRejected
	}
	now := time.Now()
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.WikiPageVersion{}).Where("id = ?", version.ID).Updates(map[string]any{
			"review_status": status, "review_note": input.Note, "reviewed_by": principal.SubjectID,
			"reviewed_at": &now,
		}).Error; err != nil {
			return err
		}
		pageStatus := PageDraft
		if page.PublishedVersionID != nil {
			pageStatus = PagePublished
		}
		return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.WikiPage{}).Where("id = ?", page.ID).Updates(map[string]any{"status": pageStatus, "updated_at": now}).Error
	})
	if err != nil {
		return models.WikiPageVersion{}, err
	}
	version.ReviewStatus = status
	version.ReviewNote = input.Note
	version.ReviewedBy = principal.SubjectID
	version.ReviewedAt = &now
	s.recordAudit(ctx, principal, page.KnowledgeBaseID, page.ID, "wiki_review_"+status)
	return version, nil
}

func (s *Service) Publish(ctx context.Context, principal authz.Principal, pageID, versionID string) (models.WikiPage, error) {
	page, version, err := s.loadPageVersion(ctx, principal.TenantID, pageID, versionID)
	if err != nil {
		return models.WikiPage{}, err
	}
	if _, err := s.requireReviewer(ctx, principal, page.KnowledgeBaseID); err != nil {
		return models.WikiPage{}, err
	}
	if version.ReviewStatus != ReviewApproved {
		return models.WikiPage{}, fmt.Errorf("%w: only approved versions can be published", apperror.ErrConflict)
	}
	return s.publishVersion(ctx, principal, page, version, "wiki_publish")
}

func (s *Service) Rollback(ctx context.Context, principal authz.Principal, pageID string, input RollbackInput) (models.WikiPage, error) {
	page, version, err := s.loadPageVersion(ctx, principal.TenantID, pageID, input.VersionID)
	if err != nil {
		return models.WikiPage{}, err
	}
	if _, err := s.requireReviewer(ctx, principal, page.KnowledgeBaseID); err != nil {
		return models.WikiPage{}, err
	}
	if version.ReviewStatus != ReviewApproved {
		return models.WikiPage{}, fmt.Errorf("%w: only approved versions can be restored", apperror.ErrConflict)
	}
	return s.publishVersion(ctx, principal, page, version, "wiki_rollback")
}

func (s *Service) publishVersion(ctx context.Context, principal authz.Principal, page models.WikiPage, version models.WikiPageVersion, auditAction string) (models.WikiPage, error) {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, page.TenantID, page.KnowledgeBaseID)
	if err != nil {
		return models.WikiPage{}, apperror.ErrNotFound
	}
	if version.ACLVersion != knowledgeBase.ACLVersion {
		s.invalidatePage(ctx, page.TenantID, page.ID)
		return models.WikiPage{}, fmt.Errorf("%w: acl_changed: rebuild and review the page under the current ACL", apperror.ErrConflict)
	}
	if err := s.validateSources(ctx, version.ID); err != nil {
		s.invalidatePage(ctx, page.TenantID, page.ID)
		return models.WikiPage{}, err
	}
	embedding, err := s.embedPage(ctx, page.Title, version.Content)
	if err != nil {
		return models.WikiPage{}, err
	}
	now := time.Now()
	err = tenantdb.InTx(ctx, s.db, page.TenantID, func(tx *gorm.DB) error {
		if err := tenantdb.MustScoped(tx, principal.TenantID).Where("wiki_page_id = ?", page.ID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
			return err
		}
		record := models.KnowledgeIndexRecord{
			ID: stableID("kir", page.ID, version.ID), TenantID: page.TenantID, KnowledgeBaseID: page.KnowledgeBaseID,
			WikiPageID: page.ID, WikiPageVersionID: version.ID, Title: page.Title, Content: version.Content,
			ContentHash: sha256Hex(page.Title + "\n" + version.Content), Embedding: vecutil.Literal(embedding),
			EmbeddingModel: s.embedder.Name(), EmbeddingDimension: len(embedding),
			ACLVersion: knowledgeBase.ACLVersion, Status: IndexActive, IndexedAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.WikiPage{}).Where("id = ?", page.ID).Updates(map[string]any{
			"published_version_id": version.ID, "status": PagePublished,
			"acl_version": knowledgeBase.ACLVersion, "updated_at": now,
		}).Error
	})
	if err != nil {
		return models.WikiPage{}, err
	}
	page.PublishedVersionID = &version.ID
	page.Status = PagePublished
	page.ACLVersion = knowledgeBase.ACLVersion
	s.recordAudit(ctx, principal, page.KnowledgeBaseID, page.ID, auditAction)
	return page, nil
}

func (s *Service) Rebuild(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (int, error) {
	if principal.CredentialType == "api_key" {
		return 0, apperror.ErrForbidden
	}
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return 0, err
	}
	count, err := s.RebuildKnowledgeBase(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	s.recordAudit(ctx, principal, knowledgeBaseID, knowledgeBaseID, "wiki_rebuild")
	return count, nil
}

func (s *Service) loadPageVersion(ctx context.Context, tenantID, pageID, versionID string) (models.WikiPage, models.WikiPageVersion, error) {
	var page models.WikiPage
	if err := s.db.WithContext(ctx).First(&page, "id = ? AND tenant_id = ?", pageID, tenantID).Error; err != nil {
		return models.WikiPage{}, models.WikiPageVersion{}, apperror.ErrNotFound
	}
	var version models.WikiPageVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).First(&version, "id = ? AND wiki_page_id = ?", versionID, pageID).Error; err != nil {
		return models.WikiPage{}, models.WikiPageVersion{}, apperror.ErrNotFound
	}
	return page, version, nil
}

func (s *Service) validateSources(ctx context.Context, versionID string) error {
	var total, invalid int64
	if err := s.db.WithContext(ctx).Table("wiki_page_sources s").
		Where("s.wiki_page_version_id = ?", versionID).Count(&total).Error; err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("%w: page version has no source chunks", apperror.ErrConflict)
	}
	if err := s.db.WithContext(ctx).Table("wiki_page_sources s").
		Joins("JOIN document_versions dv ON dv.id = s.document_version_id").
		Joins("JOIN documents d ON d.id = s.document_id").
		Joins("JOIN chunks c ON c.id = s.chunk_id").
		Where("s.wiki_page_version_id = ? AND (d.latest_version_id <> s.document_version_id OR d.deleted_at IS NOT NULL OR d.status <> ? OR dv.status <> ? OR c.status <> ?)",
			versionID, ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady).
		Count(&invalid).Error; err != nil {
		return err
	}
	if invalid > 0 {
		return fmt.Errorf("%w: a source chunk is no longer evidence-ready; rebuild is required", apperror.ErrConflict)
	}
	return nil
}

func (s *Service) embedPage(ctx context.Context, title, content string) ([]float32, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("knowledge index requires an embedding adapter")
	}
	text := strings.TrimSpace(title + "\n" + content)
	vectors, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed wiki page: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.embedder.Dimension() {
		return nil, fmt.Errorf("embed wiki page returned an unexpected vector")
	}
	return vectors[0], nil
}

func (s *Service) invalidatePage(ctx context.Context, tenantID, pageID string) {
	now := time.Now()
	tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.WikiPage{}).Where("id = ?", pageID).Updates(map[string]any{"status": PageStale, "updated_at": now})
	tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("wiki_page_id = ?", pageID).Delete(&models.KnowledgeIndexRecord{})
}
