package knowledgeprojection

import (
	"context"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/platform/vecutil"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// buildGeneration materializes the wiki run into a generation, carries forward
// unchanged pages, and activates it when every proposed version is already
// reviewed. Runs with pending review stay in REVIEW; the old ACTIVE generation
// keeps serving readers.
func (s *Service) buildGeneration(ctx context.Context, run *models.ProjectionRun, knowledgeBase models.KnowledgeBase, sourceHash string) (bool, error) {
	var state models.KnowledgeProjectionState
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", run.TenantID, knowledgeBase.ID).
		First(&state).Error; err != nil && err != gorm.ErrRecordNotFound {
		return false, err
	}
	baseVersions := map[string]string{}
	if state.ActiveWikiGenerationID != nil {
		var basePages []models.WikiGenerationPage
		if err := s.db.WithContext(ctx).Where("generation_id = ?", *state.ActiveWikiGenerationID).Find(&basePages).Error; err != nil {
			return false, err
		}
		for _, page := range basePages {
			if page.WikiPageVersionID != nil {
				baseVersions[page.WikiPageID] = *page.WikiPageVersionID
			}
		}
	}
	var pages []models.WikiPage
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", run.TenantID, knowledgeBase.ID).
		Order("id").Find(&pages).Error; err != nil {
		return false, err
	}
	if len(pages) == 0 {
		return false, nil
	}
	pageIDs := make([]string, 0, len(pages))
	for _, page := range pages {
		pageIDs = append(pageIDs, page.ID)
	}
	var versions []models.WikiPageVersion
	if err := s.db.WithContext(ctx).Where("wiki_page_id IN ?", pageIDs).Find(&versions).Error; err != nil {
		return false, err
	}
	versionByID := map[string]models.WikiPageVersion{}
	for _, version := range versions {
		versionByID[version.ID] = version
	}
	var generationNo int
	if err := s.db.WithContext(ctx).Model(&models.WikiGeneration{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", run.TenantID, knowledgeBase.ID).
		Select("COALESCE(MAX(generation_no), 0)").Scan(&generationNo).Error; err != nil {
		return false, err
	}
	generationID := stableID("gen", knowledgeBase.ID, run.ID)
	now := time.Now().UTC()
	generation := models.WikiGeneration{
		ID: generationID, TenantID: run.TenantID, KnowledgeBaseID: knowledgeBase.ID,
		GenerationNo: generationNo + 1, Status: status.Building, BaseGenerationID: state.ActiveWikiGenerationID,
		ProjectionRunID: run.ID, TargetEpoch: run.TargetEpoch, SourceHash: sourceHash,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&generation).Error; err != nil {
		return false, err
	}
	generatedPending := false
	rows := make([]models.WikiGenerationPage, 0, len(pages))
	for _, page := range pages {
		versionID := baseVersions[page.ID]
		mappingStatus := status.GenerationPageCarried
		if page.CurrentVersionID != nil {
			current := versionByID[*page.CurrentVersionID]
			if versionID == "" || *page.CurrentVersionID != versionID {
				switch current.ReviewStatus {
				case ReviewPending:
					versionID = *page.CurrentVersionID
					mappingStatus = status.GenerationPageGenerated
					generatedPending = true
				case ReviewApproved:
					versionID = *page.CurrentVersionID
					mappingStatus = status.GenerationPageGenerated
				case ReviewRejected:
					mappingStatus = status.GenerationPageException
				}
			}
		}
		if versionID == "" && mappingStatus != status.GenerationPageGenerated {
			continue
		}
		var versionPtr *string
		if versionID != "" {
			value := versionID
			versionPtr = &value
		}
		rows = append(rows, models.WikiGenerationPage{
			TenantID: run.TenantID, KnowledgeBaseID: knowledgeBase.ID, GenerationID: generationID,
			WikiPageID: page.ID, WikiPageVersionID: versionPtr, Status: mappingStatus,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(rows) > 0 {
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "generation_id"}, {Name: "wiki_page_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"wiki_page_version_id", "status", "updated_at"}),
		}).Create(&rows).Error; err != nil {
			return false, err
		}
	}
	generation.Status = status.GenerationReady
	if generatedPending {
		generation.Status = status.GenerationReview
	}
	if err := s.db.WithContext(ctx).Model(&models.WikiGeneration{}).Where("id = ?", generationID).
		Updates(map[string]any{"status": generation.Status, "updated_at": now}).Error; err != nil {
		return false, err
	}
	if generation.Status != status.GenerationReady {
		return false, nil
	}
	return s.activateGeneration(ctx, run.TenantID, knowledgeBase.ID, generationID)
}

// completeGenerationReview is called after a page version is approved or
// rejected. Rejections fall back to the previous ACTIVE version as an explicit
// exception; once every proposed version is resolved the generation becomes
// READY and activates if its epoch is still current.
func (s *Service) completeGenerationReview(ctx context.Context, tenantID, knowledgeBaseID, versionID string) error {
	var mapping models.WikiGenerationPage
	if err := s.db.WithContext(ctx).Where("wiki_page_version_id = ? AND tenant_id = ?", versionID, tenantID).
		First(&mapping).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	var generation models.WikiGeneration
	if err := s.db.WithContext(ctx).First(&generation, "id = ?", mapping.GenerationID).Error; err != nil {
		return err
	}
	if generation.Status != status.GenerationReview && generation.Status != status.GenerationReady {
		return nil
	}
	var version models.WikiPageVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", versionID).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	if version.ReviewStatus == ReviewRejected {
		carried := ""
		if generation.BaseGenerationID != nil {
			var base models.WikiGenerationPage
			if err := s.db.WithContext(ctx).Where("generation_id = ? AND wiki_page_id = ?", *generation.BaseGenerationID, mapping.WikiPageID).
				First(&base).Error; err == nil && base.WikiPageVersionID != nil {
				carried = *base.WikiPageVersionID
			}
		}
		var versionPtr *string
		if carried != "" {
			value := carried
			versionPtr = &value
		}
		if err := s.db.WithContext(ctx).Model(&models.WikiGenerationPage{}).
			Where("generation_id = ? AND wiki_page_id = ?", generation.ID, mapping.WikiPageID).
			Updates(map[string]any{"status": status.GenerationPageException, "wiki_page_version_id": versionPtr, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	var pending []models.WikiGenerationPage
	if err := s.db.WithContext(ctx).Where("generation_id = ? AND status = ?", generation.ID, status.GenerationPageGenerated).
		Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) > 0 {
		versionIDs := make([]string, 0, len(pending))
		for _, page := range pending {
			if page.WikiPageVersionID != nil {
				versionIDs = append(versionIDs, *page.WikiPageVersionID)
			}
		}
		var unresolved int64
		if err := s.db.WithContext(ctx).Model(&models.WikiPageVersion{}).
			Where("id IN ? AND review_status = ?", versionIDs, ReviewPending).Count(&unresolved).Error; err != nil {
			return err
		}
		if unresolved > 0 {
			return nil
		}
	}
	if err := s.db.WithContext(ctx).Model(&models.WikiGeneration{}).Where("id = ?", generation.ID).
		Updates(map[string]any{"status": status.GenerationReady, "updated_at": now}).Error; err != nil {
		return err
	}
	_, err := s.activateGeneration(ctx, tenantID, knowledgeBaseID, generation.ID)
	return err
}

// RollbackGeneration re-activates an earlier generation. It republishes that
// generation's page versions in one transaction and leaves newer pending
// changes to the batch scheduler.
func (s *Service) RollbackGeneration(ctx context.Context, principal authz.Principal, knowledgeBaseID, generationID string) (models.WikiGeneration, error) {
	if principal.CredentialType == "api_key" {
		return models.WikiGeneration{}, apperror.ErrForbidden
	}
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return models.WikiGeneration{}, err
	}
	if _, err := s.requireReviewer(ctx, principal, knowledgeBaseID); err != nil {
		return models.WikiGeneration{}, err
	}
	var generation models.WikiGeneration
	if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", generationID, principal.TenantID, knowledgeBaseID).
		First(&generation).Error; err != nil {
		return models.WikiGeneration{}, apperror.ErrNotFound
	}
	var state models.KnowledgeProjectionState
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, knowledgeBaseID).
		First(&state).Error; err != nil {
		return models.WikiGeneration{}, err
	}
	if state.ActiveWikiGenerationID != nil && *state.ActiveWikiGenerationID == generation.ID {
		return generation, nil
	}
	switch generation.Status {
	case status.Active, status.Superseded, status.GenerationReady:
	default:
		return models.WikiGeneration{}, fmt.Errorf("%w: only an ACTIVE, READY or SUPERSEDED generation can be restored", apperror.ErrConflict)
	}
	var mappings []models.WikiGenerationPage
	if err := s.db.WithContext(ctx).Where("generation_id = ?", generation.ID).Find(&mappings).Error; err != nil {
		return models.WikiGeneration{}, err
	}
	if len(mappings) == 0 {
		return models.WikiGeneration{}, fmt.Errorf("%w: generation has no pages", apperror.ErrConflict)
	}
	now := time.Now().UTC()
	type rollbackPage struct {
		page    models.WikiPage
		version models.WikiPageVersion
		record  models.KnowledgeIndexRecord
	}
	pages := make([]rollbackPage, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.WikiPageVersionID == nil {
			continue
		}
		var page models.WikiPage
		if err := s.db.WithContext(ctx).First(&page, "id = ? AND tenant_id = ?", mapping.WikiPageID, principal.TenantID).Error; err != nil {
			return models.WikiGeneration{}, err
		}
		var version models.WikiPageVersion
		if err := s.db.WithContext(ctx).First(&version, "id = ?", *mapping.WikiPageVersionID).Error; err != nil {
			return models.WikiGeneration{}, err
		}
		if version.ReviewStatus != ReviewApproved {
			return models.WikiGeneration{}, fmt.Errorf("%w: generation contains an unapproved page version", apperror.ErrConflict)
		}
		var record models.KnowledgeIndexRecord
		lookup := s.db.WithContext(ctx).Where("wiki_page_version_id = ?", version.ID).First(&record)
		if lookup.Error != nil {
			binding, vector, err := s.embedPage(ctx, principal.TenantID, knowledgeBaseID, page.Title, version.Content)
			if err != nil {
				return models.WikiGeneration{}, err
			}
			record = models.KnowledgeIndexRecord{
				ID: stableID("kir", page.ID, version.ID), TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID,
				WikiPageID: page.ID, WikiPageVersionID: version.ID, Title: page.Title, Content: version.Content,
				ContentHash: sha256Hex(page.Title + "\n" + version.Content), Embedding: vecutil.Literal(vector),
				EmbeddingModel: binding.Model, EmbeddingDimension: binding.Dimension,
				ACLVersion: version.ACLVersion, Status: IndexActive, IndexedAt: &now, CreatedAt: now, UpdatedAt: now,
			}
		}
		pages = append(pages, rollbackPage{page: page, version: version, record: record})
	}
	err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if state.ActiveWikiGenerationID != nil {
			if err := tx.Model(&models.WikiGeneration{}).Where("id = ?", *state.ActiveWikiGenerationID).
				Updates(map[string]any{"status": status.Superseded, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		claim := tx.Model(&models.WikiGeneration{}).Where("id = ?", generation.ID).
			Updates(map[string]any{"status": status.Active, "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return fmt.Errorf("%w: generation is no longer restorable", apperror.ErrConflict)
		}
		for _, page := range pages {
			if err := tx.Where("wiki_page_id = ?", page.page.ID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
				return err
			}
			record := page.record
			record.CreatedAt = now
			record.UpdatedAt = now
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.WikiPage{}).Where("id = ?", page.page.ID).
				Updates(map[string]any{
					"published_version_id": page.version.ID, "status": PagePublished,
					"acl_version": page.version.ACLVersion, "updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		present := map[string]bool{}
		for _, page := range pages {
			present[page.page.ID] = true
		}
		var currentPages []models.WikiPage
		if err := tenantdb.MustScoped(tx, principal.TenantID).Where("knowledge_base_id = ?", knowledgeBaseID).Find(&currentPages).Error; err != nil {
			return err
		}
		for _, page := range currentPages {
			if present[page.ID] {
				continue
			}
			if err := tx.Where("wiki_page_id = ?", page.ID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.WikiPage{}).Where("id = ?", page.ID).
				Updates(map[string]any{"status": PageArchived, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		invalidated := state.InvalidatedAt
		if generation.TargetEpoch >= state.TargetEpoch {
			invalidated = nil
		}
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.KnowledgeProjectionState{}).
			Where("knowledge_base_id = ?", knowledgeBaseID).
			Updates(map[string]any{
				"active_wiki_generation_id": generation.ID, "wiki_applied_epoch": generation.TargetEpoch,
				"invalidated_at": invalidated, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return models.WikiGeneration{}, err
	}
	s.recordAuditResource(ctx, principal, knowledgeBaseID, "wiki_generation", generation.ID, "wiki_generation_rollback")
	generation.Status = status.Active
	return generation, nil
}

// activateGeneration flips the whole knowledge base at once. The CAS on
// target_epoch is the only writer of active_wiki_generation_id, so a generation
// whose epoch moved on is superseded instead of published.
func (s *Service) activateGeneration(ctx context.Context, tenantID, knowledgeBaseID, generationID string) (bool, error) {
	var generation models.WikiGeneration
	if err := s.db.WithContext(ctx).First(&generation, "id = ?", generationID).Error; err != nil {
		return false, err
	}
	var state models.KnowledgeProjectionState
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).
		First(&state).Error; err != nil {
		return false, err
	}
	if generation.TargetEpoch != state.TargetEpoch {
		if err := s.db.WithContext(ctx).Model(&models.WikiGeneration{}).Where("id = ?", generation.ID).
			Updates(map[string]any{"status": status.Superseded, "updated_at": time.Now().UTC()}).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	var mappings []models.WikiGenerationPage
	if err := s.db.WithContext(ctx).Where("generation_id = ?", generation.ID).Order("wiki_page_id").Find(&mappings).Error; err != nil {
		return false, err
	}
	type activationPage struct {
		page    models.WikiPage
		version models.WikiPageVersion
		record  models.KnowledgeIndexRecord
	}
	pages := make([]activationPage, 0, len(mappings))
	versionIDs := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.WikiPageVersionID == nil {
			continue
		}
		versionIDs = append(versionIDs, *mapping.WikiPageVersionID)
	}
	var pageRows []models.WikiPage
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).Find(&pageRows).Error; err != nil {
		return false, err
	}
	pageByID := map[string]models.WikiPage{}
	for _, page := range pageRows {
		pageByID[page.ID] = page
	}
	var versionRows []models.WikiPageVersion
	if len(versionIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", versionIDs).Find(&versionRows).Error; err != nil {
			return false, err
		}
	}
	versionByID := map[string]models.WikiPageVersion{}
	for _, version := range versionRows {
		versionByID[version.ID] = version
	}
	var existingRecords []models.KnowledgeIndexRecord
	if len(versionIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("wiki_page_version_id IN ?", versionIDs).Find(&existingRecords).Error; err != nil {
			return false, err
		}
	}
	recordByVersion := map[string]models.KnowledgeIndexRecord{}
	for _, record := range existingRecords {
		recordByVersion[record.WikiPageVersionID] = record
	}
	now := time.Now().UTC()
	for _, mapping := range mappings {
		if mapping.WikiPageVersionID == nil {
			continue
		}
		page, ok := pageByID[mapping.WikiPageID]
		if !ok {
			continue
		}
		version, ok := versionByID[*mapping.WikiPageVersionID]
		if !ok {
			continue
		}
		if version.ReviewStatus != ReviewApproved && mapping.Status == status.GenerationPageGenerated {
			// Still unreviewed; the caller should not have marked READY.
			return false, fmt.Errorf("generation %s has an unreviewed page %s", generation.ID, page.ID)
		}
		record, exists := recordByVersion[version.ID]
		if !exists {
			binding, vector, err := s.embedPage(ctx, tenantID, knowledgeBaseID, page.Title, version.Content)
			if err != nil {
				return false, err
			}
			record = models.KnowledgeIndexRecord{
				ID: stableID("kir", page.ID, version.ID), TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID,
				WikiPageID: page.ID, WikiPageVersionID: version.ID, Title: page.Title, Content: version.Content,
				ContentHash: sha256Hex(page.Title + "\n" + version.Content), Embedding: vecutil.Literal(vector),
				EmbeddingModel: binding.Model, EmbeddingDimension: binding.Dimension,
				ACLVersion: version.ACLVersion, Status: IndexActive, IndexedAt: &now, CreatedAt: now, UpdatedAt: now,
			}
		}
		pages = append(pages, activationPage{page: page, version: version, record: record})
	}
	err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		var previousActive string
		var active models.WikiGeneration
		if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, status.Active).
			First(&active).Error; err == nil {
			previousActive = active.ID
		}
		// Supersede the current generation before claiming the target: the
		// partial unique index allows only one ACTIVE row, and the transaction
		// rolls this back if the target can no longer activate.
		if previousActive != "" && previousActive != generation.ID {
			if err := tx.Model(&models.WikiGeneration{}).Where("id = ?", previousActive).
				Updates(map[string]any{"status": status.Superseded, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		claim := tx.Model(&models.WikiGeneration{}).
			Where("id = ? AND status = ? AND target_epoch = ?", generation.ID, status.GenerationReady, state.TargetEpoch).
			Updates(map[string]any{"status": status.Active, "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			s.metrics.Inc("projection_activation_conflict_total", metrics.Labels("reason", "epoch_or_status"))
			return fmt.Errorf("generation %s is no longer activatable", generation.ID)
		}
		for _, page := range pages {
			if err := tx.Where("wiki_page_id = ?", page.page.ID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
				return err
			}
			record := page.record
			record.CreatedAt = now
			record.UpdatedAt = now
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, tenantID).Model(&models.WikiPage{}).Where("id = ?", page.page.ID).
				Updates(map[string]any{
					"published_version_id": page.version.ID, "status": PagePublished,
					"acl_version": page.version.ACLVersion, "updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		// Pages from the base generation that are absent from this one are
		// archived together with the switch.
		present := map[string]bool{}
		for _, page := range pages {
			present[page.page.ID] = true
		}
		if previousActive != "" {
			var basePages []models.WikiGenerationPage
			if err := tx.Where("generation_id = ?", previousActive).Find(&basePages).Error; err != nil {
				return err
			}
			for _, base := range basePages {
				if present[base.WikiPageID] {
					continue
				}
				if err := tx.Where("wiki_page_id = ?", base.WikiPageID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
					return err
				}
				if err := tenantdb.MustScoped(tx, tenantID).Model(&models.WikiPage{}).Where("id = ?", base.WikiPageID).
					Updates(map[string]any{"status": PageArchived, "updated_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if err := tenantdb.MustScoped(tx, tenantID).Model(&models.KnowledgeProjectionState{}).
			Where("knowledge_base_id = ?", knowledgeBaseID).
			Updates(map[string]any{
				"active_wiki_generation_id": generation.ID, "wiki_applied_epoch": generation.TargetEpoch,
				"invalidated_at": nil, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, tenantID).Model(&models.ProjectionChange{}).
			Where("knowledge_base_id = ? AND applied_at IS NULL AND epoch <= ?", knowledgeBaseID, generation.TargetEpoch).
			Update("applied_at", now).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
