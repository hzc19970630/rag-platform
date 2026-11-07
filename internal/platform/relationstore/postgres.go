// Package relationstore is the PostgreSQL implementation of the RelationStore
// port. It owns the graph_* tables; the knowledge projection domain keeps
// traversal, authorization and evidence validity checks.
package relationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

var _ relationstore.RelationStore = (*Store)(nil)

func (s *Store) ActiveSnapshot(ctx context.Context, tenantID, knowledgeBaseID string, aclVersion int64) (*relationstore.Snapshot, error) {
	var row models.GraphSnapshot
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND status = ? AND acl_version = ?", tenantID, knowledgeBaseID, models.GraphSnapshotActive, aclVersion).
		Order("version DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active graph snapshot: %w", err)
	}
	snapshot := toSnapshot(row)
	return &snapshot, nil
}

func (s *Store) SnapshotByID(ctx context.Context, tenantID, snapshotID string) (*relationstore.Snapshot, error) {
	var row models.GraphSnapshot
	err := s.db.WithContext(ctx).First(&row, "id = ? AND tenant_id = ?", snapshotID, tenantID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, relationstore.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load graph snapshot: %w", err)
	}
	snapshot := toSnapshot(row)
	return &snapshot, nil
}

func (s *Store) SnapshotByRun(ctx context.Context, runID string) (*relationstore.Snapshot, error) {
	var row models.GraphSnapshot
	err := s.db.WithContext(ctx).Where("projection_run_id = ?", runID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, relationstore.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load graph snapshot for run: %w", err)
	}
	snapshot := toSnapshot(row)
	return &snapshot, nil
}

func (s *Store) ListSnapshots(ctx context.Context, tenantID, knowledgeBaseID string, limit int) ([]relationstore.Snapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var rows []models.GraphSnapshot
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).
		Order("version DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list graph snapshots: %w", err)
	}
	snapshots := make([]relationstore.Snapshot, 0, len(rows))
	for _, row := range rows {
		snapshots = append(snapshots, toSnapshot(row))
	}
	return snapshots, nil
}

func (s *Store) Graph(ctx context.Context, snapshotID string) (*relationstore.Graph, error) {
	graph := &relationstore.Graph{Entities: []relationstore.Entity{}, Aliases: []relationstore.Alias{}, Relations: []relationstore.Relation{}, Sources: []relationstore.Source{}}

	var entities []models.GraphEntity
	if err := s.db.WithContext(ctx).Where("snapshot_id = ?", snapshotID).Order("id").Find(&entities).Error; err != nil {
		return nil, fmt.Errorf("load graph entities: %w", err)
	}
	entityIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		graph.Entities = append(graph.Entities, relationstore.Entity{
			ID: entity.ID, TenantID: entity.TenantID, KnowledgeBaseID: entity.KnowledgeBaseID,
			CanonicalKey: entity.CanonicalKey, Name: entity.Name, EntityType: entity.EntityType,
			Properties: decodeProperties(entity.Properties), ACLVersion: entity.ACLVersion,
		})
		entityIDs = append(entityIDs, entity.ID)
	}
	if len(entityIDs) > 0 {
		var aliases []models.GraphEntityAlias
		if err := s.db.WithContext(ctx).Where("entity_id IN ?", entityIDs).Order("entity_id, normalized").Find(&aliases).Error; err != nil {
			return nil, fmt.Errorf("load graph aliases: %w", err)
		}
		for _, alias := range aliases {
			graph.Aliases = append(graph.Aliases, relationstore.Alias{
				TenantID: alias.TenantID, KnowledgeBaseID: alias.KnowledgeBaseID,
				EntityID: alias.EntityID, Alias: alias.Alias, Normalized: alias.Normalized,
				Rule: alias.Rule, MergedKey: alias.MergedKey, CreatedAt: alias.CreatedAt,
			})
		}
	}

	var relations []models.GraphRelation
	if err := s.db.WithContext(ctx).Where("snapshot_id = ? AND status = ?", snapshotID, "ACTIVE").Order("id").Find(&relations).Error; err != nil {
		return nil, fmt.Errorf("load graph relations: %w", err)
	}
	relationIDs := make([]string, 0, len(relations))
	for _, relation := range relations {
		graph.Relations = append(graph.Relations, relationstore.Relation{
			ID: relation.ID, TenantID: relation.TenantID, KnowledgeBaseID: relation.KnowledgeBaseID,
			SubjectID: relation.SubjectID, Predicate: relation.Predicate, ObjectID: relation.ObjectID,
			Properties: decodeProperties(relation.Properties), Confidence: relation.Confidence, ACLVersion: relation.ACLVersion,
		})
		relationIDs = append(relationIDs, relation.ID)
	}
	if len(relationIDs) > 0 {
		var sources []models.GraphRelationSource
		if err := s.db.WithContext(ctx).Where("relation_id IN ?", relationIDs).Order("relation_id, chunk_id").Find(&sources).Error; err != nil {
			return nil, fmt.Errorf("load graph relation sources: %w", err)
		}
		for _, source := range sources {
			graph.Sources = append(graph.Sources, relationstore.Source{
				TenantID: source.TenantID, KnowledgeBaseID: source.KnowledgeBaseID, RelationID: source.RelationID,
				DocumentID: source.DocumentID, DocumentVersionID: source.DocumentVersionID, ChunkID: source.ChunkID,
				SourcePath: source.SourcePath, HeadingPath: source.HeadingPath, PageNumber: source.PageNumber,
			})
		}
	}
	return graph, nil
}

func (s *Store) SourceRefs(ctx context.Context, snapshotID string) (relationstore.SourceRefs, error) {
	result := relationstore.SourceRefs{Refs: []relationstore.SourceRef{}}
	var sourceless int64
	if err := s.db.WithContext(ctx).Table("graph_relations r").
		Where("r.snapshot_id = ? AND NOT EXISTS (SELECT 1 FROM graph_relation_sources s WHERE s.relation_id = r.id)", snapshotID).
		Count(&sourceless).Error; err != nil {
		return relationstore.SourceRefs{}, fmt.Errorf("count unsourced graph relations: %w", err)
	}
	result.SourcelessRelations = int(sourceless)
	var rows []struct {
		RelationID        string `gorm:"column:relation_id"`
		TenantID          string `gorm:"column:tenant_id"`
		KnowledgeBaseID   string `gorm:"column:knowledge_base_id"`
		DocumentID        string `gorm:"column:document_id"`
		DocumentVersionID string `gorm:"column:document_version_id"`
		ChunkID           string `gorm:"column:chunk_id"`
	}
	if err := s.db.WithContext(ctx).Table("graph_relation_sources s").
		Select("s.relation_id, s.tenant_id, s.knowledge_base_id, s.document_id, s.document_version_id, s.chunk_id").
		Joins("JOIN graph_relations r ON r.id = s.relation_id").
		Where("r.snapshot_id = ?", snapshotID).
		Order("s.relation_id, s.chunk_id").Scan(&rows).Error; err != nil {
		return relationstore.SourceRefs{}, fmt.Errorf("load graph source refs: %w", err)
	}
	for _, row := range rows {
		result.Refs = append(result.Refs, relationstore.SourceRef{
			RelationID: row.RelationID, TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID,
			DocumentID: row.DocumentID, DocumentVersionID: row.DocumentVersionID, ChunkID: row.ChunkID,
		})
	}
	return result, nil
}

func (s *Store) Counts(ctx context.Context, tenantID, knowledgeBaseID string) (relationstore.Counts, error) {
	var counts relationstore.Counts
	if err := s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).Count(&counts.Snapshots).Error; err != nil {
		return counts, err
	}
	var active models.GraphSnapshot
	activeErr := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, models.GraphSnapshotActive).
		Order("version DESC").First(&active).Error
	if activeErr == nil {
		counts.ActiveSnapshot = active.ID
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.GraphEntity{}).Where("snapshot_id = ?", active.ID).Count(&counts.Entities).Error; err != nil {
			return counts, err
		}
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.GraphRelation{}).Where("snapshot_id = ?", active.ID).Count(&counts.Relations).Error; err != nil {
			return counts, err
		}
	} else if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
		return counts, activeErr
	}
	if err := s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", tenantID, knowledgeBaseID, models.GraphSnapshotPending).
		Count(&counts.PendingReview).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

func (s *Store) ReplaceRunSnapshot(ctx context.Context, write relationstore.SnapshotWrite) error {
	now := write.Snapshot.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updatedAt := write.Snapshot.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	stats := strings.TrimSpace(write.Snapshot.Stats)
	if stats == "" {
		stats = "{}"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Retrying EXTRACT replaces the previous attempt's snapshot atomically.
		// Children are removed explicitly rather than relying on ON DELETE CASCADE
		// so the replacement also works on databases without FK enforcement.
		var previous []string
		if err := tenantdb.MustScoped(tx, write.Snapshot.TenantID).Model(&models.GraphSnapshot{}).Where("projection_run_id = ?", write.Snapshot.ProjectionRunID).Pluck("id", &previous).Error; err != nil {
			return err
		}
		if len(previous) > 0 {
			var entityIDs, relationIDs []string
			if err := tenantdb.MustScoped(tx, write.Snapshot.TenantID).Model(&models.GraphEntity{}).Where("snapshot_id IN ?", previous).Pluck("id", &entityIDs).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, write.Snapshot.TenantID).Model(&models.GraphRelation{}).Where("snapshot_id IN ?", previous).Pluck("id", &relationIDs).Error; err != nil {
				return err
			}
			if len(entityIDs) > 0 {
				if err := tx.Where("entity_id IN ?", entityIDs).Delete(&models.GraphEntityAlias{}).Error; err != nil {
					return err
				}
			}
			if len(relationIDs) > 0 {
				if err := tx.Where("relation_id IN ?", relationIDs).Delete(&models.GraphRelationSource{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("snapshot_id IN ?", previous).Delete(&models.GraphRelation{}).Error; err != nil {
				return err
			}
			if err := tx.Where("snapshot_id IN ?", previous).Delete(&models.GraphEntity{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", previous).Delete(&models.GraphSnapshot{}).Error; err != nil {
				return err
			}
		}

		var nextVersion int
		if err := tenantdb.MustScoped(tx, write.Snapshot.TenantID).Model(&models.GraphSnapshot{}).Where("knowledge_base_id = ?", write.Snapshot.KnowledgeBaseID).
			Select("COALESCE(MAX(version), 0)").Scan(&nextVersion).Error; err != nil {
			return err
		}
		status := write.Snapshot.Status
		if status == "" {
			status = models.GraphSnapshotBuilding
		}
		snapshot := models.GraphSnapshot{
			ID: write.Snapshot.ID, TenantID: write.Snapshot.TenantID, KnowledgeBaseID: write.Snapshot.KnowledgeBaseID,
			ProjectionRunID: write.Snapshot.ProjectionRunID, Version: nextVersion + 1, Status: status,
			SourceHash: write.Snapshot.SourceHash, ACLVersion: write.Snapshot.ACLVersion,
			Model: write.Snapshot.Model, PromptVersion: write.Snapshot.PromptVersion, Stats: stats,
			CreatedAt: now, UpdatedAt: updatedAt,
		}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}

		entityRows := make([]models.GraphEntity, 0, len(write.Entities))
		aliasRows := make([]models.GraphEntityAlias, 0, len(write.Aliases))
		for _, entity := range write.Entities {
			properties, err := encodeProperties(entity.Properties)
			if err != nil {
				return err
			}
			entityRows = append(entityRows, models.GraphEntity{
				ID: entity.ID, TenantID: firstNonEmpty(entity.TenantID, write.Snapshot.TenantID),
				KnowledgeBaseID: firstNonEmpty(entity.KnowledgeBaseID, write.Snapshot.KnowledgeBaseID),
				SnapshotID:      snapshot.ID, CanonicalKey: entity.CanonicalKey, Name: entity.Name, EntityType: entity.EntityType,
				Properties: properties, ACLVersion: write.Snapshot.ACLVersion, Status: "ACTIVE",
				CreatedAt: now, UpdatedAt: now,
			})
		}
		for _, alias := range write.Aliases {
			createdAt := alias.CreatedAt
			if createdAt.IsZero() {
				createdAt = now
			}
			aliasRows = append(aliasRows, models.GraphEntityAlias{
				TenantID:        firstNonEmpty(alias.TenantID, write.Snapshot.TenantID),
				KnowledgeBaseID: firstNonEmpty(alias.KnowledgeBaseID, write.Snapshot.KnowledgeBaseID),
				EntityID:        alias.EntityID, Alias: alias.Alias, Normalized: alias.Normalized,
				Rule: alias.Rule, MergedKey: alias.MergedKey, CreatedAt: createdAt,
			})
		}
		if len(entityRows) > 0 {
			if err := tx.Create(&entityRows).Error; err != nil {
				return err
			}
		}
		if len(aliasRows) > 0 {
			if err := tx.Create(&aliasRows).Error; err != nil {
				return err
			}
		}

		relationRows := make([]models.GraphRelation, 0, len(write.Relations))
		sourceRows := make([]models.GraphRelationSource, 0, len(write.Sources))
		for _, relation := range write.Relations {
			properties, err := encodeProperties(relation.Properties)
			if err != nil {
				return err
			}
			confidence := relation.Confidence
			if confidence <= 0 {
				confidence = 1
			}
			relationRows = append(relationRows, models.GraphRelation{
				ID: relation.ID, TenantID: firstNonEmpty(relation.TenantID, write.Snapshot.TenantID),
				KnowledgeBaseID: firstNonEmpty(relation.KnowledgeBaseID, write.Snapshot.KnowledgeBaseID),
				SnapshotID:      snapshot.ID, SubjectID: relation.SubjectID, Predicate: relation.Predicate,
				ObjectID: relation.ObjectID, Properties: properties, Confidence: confidence,
				ACLVersion: write.Snapshot.ACLVersion, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now,
			})
		}
		for _, source := range write.Sources {
			sourceRows = append(sourceRows, models.GraphRelationSource{
				TenantID:        firstNonEmpty(source.TenantID, write.Snapshot.TenantID),
				KnowledgeBaseID: firstNonEmpty(source.KnowledgeBaseID, write.Snapshot.KnowledgeBaseID),
				RelationID:      source.RelationID, DocumentID: source.DocumentID, DocumentVersionID: source.DocumentVersionID,
				ChunkID: source.ChunkID, SourcePath: source.SourcePath, HeadingPath: source.HeadingPath, PageNumber: source.PageNumber,
			})
		}
		if len(relationRows) > 0 {
			if err := tx.Create(&relationRows).Error; err != nil {
				return err
			}
		}
		if len(sourceRows) > 0 {
			if err := tx.Create(&sourceRows).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Update(ctx context.Context, snapshotID string, update relationstore.SnapshotUpdate) error {
	now := update.At
	if now.IsZero() {
		now = time.Now().UTC()
	}
	fields := map[string]any{"updated_at": now}
	if update.Status != "" {
		fields["status"] = update.Status
	}
	if update.SourceHash != nil {
		fields["source_hash"] = *update.SourceHash
	}
	if update.ACLVersion != nil {
		fields["acl_version"] = *update.ACLVersion
	}
	return s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).Where("id = ?", snapshotID).Updates(fields).Error
}

func (s *Store) UpdateReview(ctx context.Context, snapshotID string, update relationstore.ReviewUpdate) error {
	now := update.At
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).Where("id = ?", snapshotID).
		Updates(map[string]any{
			"status": update.Status, "review_note": update.Note, "reviewed_by": update.Reviewer,
			"reviewed_at": &now, "updated_at": now,
		}).Error
}

func (s *Store) Approve(ctx context.Context, tenantID, snapshotID, knowledgeBaseID string, update relationstore.ReviewUpdate) error {
	now := update.At
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if err := tenantdb.MustScoped(tx, tenantID).Model(&models.GraphSnapshot{}).
			Where("knowledge_base_id = ? AND status = ? AND id <> ?", knowledgeBaseID, models.GraphSnapshotActive, snapshotID).
			Updates(map[string]any{"status": models.GraphSnapshotSuperseded, "updated_at": now}).Error; err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, tenantID).Model(&models.GraphSnapshot{}).Where("id = ?", snapshotID).Updates(map[string]any{
			"status": update.Status, "review_note": update.Note, "reviewed_by": update.Reviewer,
			"reviewed_at": &now, "updated_at": now,
		}).Error
	})
}

func (s *Store) MarkStale(ctx context.Context, snapshotIDs []string, now time.Time) error {
	if len(snapshotIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).Where("id IN ?", snapshotIDs).
		Updates(map[string]any{"status": models.GraphSnapshotStale, "updated_at": now}).Error
}

func (s *Store) MarkStaleByKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&models.GraphSnapshot{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND status IN ?", tenantID, knowledgeBaseID,
			[]string{models.GraphSnapshotBuilding, models.GraphSnapshotPending, models.GraphSnapshotActive}).
		Updates(map[string]any{"status": models.GraphSnapshotStale, "updated_at": now}).Error
}

func (s *Store) StaleSnapshotIDs(ctx context.Context) ([]string, error) {
	var staleIDs []string
	if err := s.db.WithContext(ctx).Table("graph_snapshots gs").
		Select("gs.id").
		Where("gs.status IN ?", []string{models.GraphSnapshotActive, models.GraphSnapshotPending}).
		Where(`gs.acl_version <> (SELECT kb.acl_version FROM knowledge_bases kb WHERE kb.id = gs.knowledge_base_id)
		    OR EXISTS (
		        SELECT 1 FROM graph_relations r
		        LEFT JOIN graph_relation_sources rs ON rs.relation_id = r.id
		        LEFT JOIN documents d ON d.id = rs.document_id
		        LEFT JOIN document_versions dv ON dv.id = rs.document_version_id
		        WHERE r.snapshot_id = gs.id
		          AND (rs.id IS NULL OR d.id IS NULL OR d.deleted_at IS NOT NULL
		               OR d.latest_version_id <> rs.document_version_id OR dv.status <> ?)
		    )`, status.EvidenceReady).Scan(&staleIDs).Error; err != nil {
		return nil, fmt.Errorf("find stale graph snapshots: %w", err)
	}
	return staleIDs, nil
}

func toSnapshot(row models.GraphSnapshot) relationstore.Snapshot {
	return relationstore.Snapshot{
		ID: row.ID, TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID,
		ProjectionRunID: row.ProjectionRunID, Version: row.Version, Status: row.Status,
		SourceHash: row.SourceHash, ACLVersion: row.ACLVersion, Model: row.Model,
		PromptVersion: row.PromptVersion, Stats: row.Stats, ReviewNote: row.ReviewNote,
		ReviewedBy: row.ReviewedBy, ReviewedAt: row.ReviewedAt, ActivatedAt: row.ActivatedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func decodeProperties(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var properties map[string]string
	if err := json.Unmarshal([]byte(raw), &properties); err != nil || len(properties) == 0 {
		return nil
	}
	return properties
}

func encodeProperties(properties map[string]string) (string, error) {
	if len(properties) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(properties)
	if err != nil {
		return "", fmt.Errorf("encode graph properties: %w", err)
	}
	return string(raw), nil
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
