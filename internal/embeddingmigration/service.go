// Package embeddingmigration owns the managed vector-space move for one
// knowledge base: build the target index, re-embed chunks in batches, verify,
// cut over and roll back. It never changes the knowledge-base binding while
// chunks are still being written, so retrieval keeps using the source space
// until an explicit cutover.
package embeddingmigration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StatusPending     = "pending"
	StatusBuilding    = "building_index"
	StatusReembedding = "reembedding"
	StatusVerifying   = "verifying"
	StatusReady       = "ready"
	StatusCutover     = "cutover"
	StatusFinalized   = "finalized"
	StatusRolledBack  = "rolled_back"
	StatusCancelled   = "cancelled"
	StatusFailed      = "failed"
	defaultBatchSize  = 256
)

var activeStatuses = []string{StatusPending, StatusBuilding, StatusReembedding, StatusVerifying, StatusReady}

var chunkStatuses = []string{status.Embedded, status.EvidenceReady}

// Authorizer is the slice of authorization the migration needs: knowledge-base
// read for status and write for every state change.
type Authorizer interface {
	AuthorizeKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID, action string) (bool, error)
}

// ModelCatalog resolves the target model to the vector space it serves.
type ModelCatalog interface {
	ResolveEmbeddingModel(ctx context.Context, tenantID, requested string) (embedding.Binding, error)
}

// EmbedderFactory builds an adapter for a model that is not yet the knowledge
// base binding; the migration embeds into that model.
type EmbedderFactory interface {
	EmbedderForModel(ctx context.Context, tenantID, modelName string) (embedding.Binding, embedding.Embedder, error)
}

// Writer persists target-space vectors without touching the source space.
type Writer interface {
	WriteVariants(ctx context.Context, model string, records []VariantRecord) error
}

// VariantRecord is one target-space vector.
type VariantRecord struct {
	ChunkID         string
	TenantID        string
	KnowledgeBaseID string
	Dimension       int
	Vector          []float32
}

// IndexEnsurer builds the HNSW index for a target dimension.
type IndexEnsurer interface {
	EnsureIndex(ctx context.Context, dimension int) error
}

// Publisher enqueues the next batch step.
type Publisher interface {
	EnqueueEmbeddingMigration(ctx context.Context, tenantID, migrationID string, attempt int) error
}

type Options struct {
	DB              *gorm.DB
	Authz           Authorizer
	Models          ModelCatalog
	EmbedderFactory EmbedderFactory
	Writer          Writer
	Indexer         IndexEnsurer
	Publisher       Publisher
	BatchSize       int
}

type Service struct {
	db              *gorm.DB
	authz           Authorizer
	models          ModelCatalog
	embedderFactory EmbedderFactory
	writer          Writer
	indexer         IndexEnsurer
	publisher       Publisher
	batchSize       int
}

func NewService(options Options) (*Service, error) {
	if options.DB == nil {
		return nil, fmt.Errorf("embedding migration requires a database")
	}
	if options.Authz == nil {
		return nil, fmt.Errorf("embedding migration requires authorization")
	}
	if options.Models == nil || options.EmbedderFactory == nil || options.Writer == nil || options.Indexer == nil {
		return nil, fmt.Errorf("embedding migration dependencies are incomplete")
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &Service{
		db: options.DB, authz: options.Authz, models: options.Models,
		embedderFactory: options.EmbedderFactory, writer: options.Writer,
		indexer: options.Indexer, publisher: options.Publisher, batchSize: batchSize,
	}, nil
}

// Start validates the target and records one migration. It does not touch the
// binding; a worker builds the target space in batches.
func (s *Service) Start(ctx context.Context, principal authz.Principal, knowledgeBaseID, targetModel string) (models.EmbeddingMigration, error) {
	if err := s.authorizeWrite(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	var knowledgeBase models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", knowledgeBaseID).Take(&knowledgeBase).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.EmbeddingMigration{}, apperror.ErrNotFound
		}
		return models.EmbeddingMigration{}, fmt.Errorf("load knowledge base: %w", err)
	}
	source := embedding.Binding{Model: knowledgeBase.EmbeddingModel, Dimension: knowledgeBase.EmbeddingDimension}
	if source.Model == "" || source.Dimension <= 0 {
		return models.EmbeddingMigration{}, apperror.Invalidf("knowledge base has no embedding binding; run the binding backfill first")
	}
	target, err := s.models.ResolveEmbeddingModel(ctx, principal.TenantID, targetModel)
	if err != nil {
		return models.EmbeddingMigration{}, err
	}
	if target == source {
		return models.EmbeddingMigration{}, apperror.Invalidf("knowledge base is already bound to %s/%d", target.Model, target.Dimension)
	}
	var active int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.EmbeddingMigration{}).
		Where("knowledge_base_id = ? AND status IN ?", knowledgeBaseID, activeStatuses).Count(&active).Error; err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("check active migration: %w", err)
	}
	if active > 0 {
		return models.EmbeddingMigration{}, fmt.Errorf("%w: a migration is already active for this knowledge base", apperror.ErrConflict)
	}
	var total int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Table("chunks").
		Where("knowledge_base_id = ? AND parent_id IS NOT NULL AND status IN ? AND embedding IS NOT NULL", knowledgeBaseID, chunkStatuses).
		Count(&total).Error; err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("count chunks: %w", err)
	}
	id, err := newID("emig")
	if err != nil {
		return models.EmbeddingMigration{}, err
	}
	now := time.Now()
	job := models.EmbeddingMigration{
		ID: id, TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID, Status: StatusPending,
		SourceModel: source.Model, SourceDimension: source.Dimension,
		TargetModel: target.Model, TargetDimension: target.Dimension,
		TotalChunks: total, CreatedBy: principal.SubjectID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		return s.recordAuditTx(tx, principal.TenantID, principal.SubjectID, "embedding_migration_started", id,
			fmt.Sprintf("%s/%d -> %s/%d", source.Model, source.Dimension, target.Model, target.Dimension), now)
	})
	if err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("create migration: %w", err)
	}
	if s.publisher != nil {
		if err := s.publisher.EnqueueEmbeddingMigration(ctx, job.TenantID, job.ID, 0); err != nil {
			return job, fmt.Errorf("enqueue migration: %w", err)
		}
	}
	return job, nil
}

// Step advances the job by one unit of work. It is idempotent at batch
// boundaries: a repeated task re-reads the cursor and upserts the same rows.
func (s *Service) Step(ctx context.Context, migrationID string) error {
	var job models.EmbeddingMigration
	if err := s.db.WithContext(ctx).Where("id = ?", migrationID).Take(&job).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load migration: %w", err)
	}
	switch job.Status {
	case StatusPending:
		if err := s.indexer.EnsureIndex(ctx, job.TargetDimension); err != nil {
			return fmt.Errorf("build target index: %w", err)
		}
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusReembedding, "started_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.enqueueNext(ctx, job, 0)
	case StatusReembedding:
		more, err := s.reembedBatch(ctx, job)
		if err != nil {
			return err
		}
		if more {
			return s.enqueueNext(ctx, job, 0)
		}
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusVerifying, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.enqueueNext(ctx, job, 0)
	case StatusVerifying:
		missing, err := s.missingChunks(ctx, job)
		if err != nil {
			return err
		}
		if missing > 0 {
			if err := s.db.WithContext(ctx).Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
				Updates(map[string]any{"status": StatusReembedding, "cursor_chunk_id": "", "updated_at": time.Now()}).Error; err != nil {
				return err
			}
			return s.enqueueNext(ctx, job, 0)
		}
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusReady, "ready_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return nil
	default:
		// Terminal or operator-controlled state: a duplicate delivery is a no-op.
		return nil
	}
}

func (s *Service) reembedBatch(ctx context.Context, job models.EmbeddingMigration) (bool, error) {
	var chunks []struct {
		ID      string
		Content string
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), job.TenantID).Table("chunks").
		Select("id, content").
		Where("knowledge_base_id = ? AND parent_id IS NOT NULL AND status IN ? AND id > ?", job.KnowledgeBaseID, chunkStatuses, job.CursorChunkID).
		Order("id").Limit(s.batchSize).Scan(&chunks).Error; err != nil {
		return false, fmt.Errorf("load migration batch: %w", err)
	}
	if len(chunks) == 0 {
		return false, nil
	}
	binding, adapter, err := s.embedderFactory.EmbedderForModel(ctx, job.TenantID, job.TargetModel)
	if err != nil {
		return false, err
	}
	if binding.Model != job.TargetModel || binding.Dimension != job.TargetDimension {
		return false, fmt.Errorf("target model %q resolved to %s/%d, migration expects %d dimensions", job.TargetModel, binding.Model, binding.Dimension, job.TargetDimension)
	}
	texts := make([]string, len(chunks))
	for i := range chunks {
		texts[i] = chunks[i].Content
	}
	vectors, err := adapter.Embed(ctx, texts)
	if err != nil {
		return false, fmt.Errorf("embed migration batch: %w", err)
	}
	if len(vectors) != len(chunks) {
		return false, fmt.Errorf("embed migration batch returned %d vectors for %d chunks", len(vectors), len(chunks))
	}
	records := make([]VariantRecord, len(chunks))
	for i := range chunks {
		if len(vectors[i]) != job.TargetDimension {
			return false, fmt.Errorf("chunk %s has dimension %d, migration expects %d", chunks[i].ID, len(vectors[i]), job.TargetDimension)
		}
		records[i] = VariantRecord{
			ChunkID: chunks[i].ID, TenantID: job.TenantID, KnowledgeBaseID: job.KnowledgeBaseID,
			Dimension: job.TargetDimension, Vector: vectors[i],
		}
	}
	if err := s.writer.WriteVariants(ctx, job.TargetModel, records); err != nil {
		return false, err
	}
	last := chunks[len(chunks)-1].ID
	if err := s.db.WithContext(ctx).Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"cursor_chunk_id": last,
			"done_chunks":     gorm.Expr("done_chunks + ?", len(chunks)),
			"updated_at":      time.Now(),
		}).Error; err != nil {
		return false, err
	}
	return len(chunks) == s.batchSize, nil
}

func (s *Service) missingChunks(ctx context.Context, job models.EmbeddingMigration) (int64, error) {
	var missing int64
	err := tenantdb.MustScoped(s.db.WithContext(ctx), job.TenantID).Table("chunks AS c").
		Where("c.knowledge_base_id = ? AND c.parent_id IS NOT NULL AND c.status IN ?", job.KnowledgeBaseID, chunkStatuses).
		Where("NOT EXISTS (SELECT 1 FROM chunk_embeddings e WHERE e.chunk_id = c.id AND e.model = ?)", job.TargetModel).
		Count(&missing).Error
	if err != nil {
		return 0, fmt.Errorf("verify migration: %w", err)
	}
	return missing, nil
}

func (s *Service) enqueueNext(ctx context.Context, job models.EmbeddingMigration, attempt int) error {
	if s.publisher == nil {
		return nil
	}
	return s.publisher.EnqueueEmbeddingMigration(ctx, job.TenantID, job.ID, attempt+1)
}

// Cutover flips the binding after verification. The target rows already exist,
// so retrieval switches spaces with one row update.
func (s *Service) Cutover(ctx context.Context, principal authz.Principal, knowledgeBaseID, migrationID string) (models.EmbeddingMigration, error) {
	if err := s.authorizeWrite(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	var job models.EmbeddingMigration
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ? AND tenant_id = ?", migrationID, principal.TenantID).Take(&job).Error; err != nil {
			return err
		}
		if job.KnowledgeBaseID != knowledgeBaseID {
			return apperror.ErrNotFound
		}
		if job.Status != StatusReady {
			return apperror.Invalidf("migration is %s, only a ready migration can be cut over", job.Status)
		}
		now := time.Now()
		if err := tx.Model(&models.KnowledgeBase{}).Where("id = ? AND tenant_id = ?", knowledgeBaseID, principal.TenantID).
			Updates(map[string]any{"embedding_model": job.TargetModel, "embedding_dimension": job.TargetDimension, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusCutover, "cutover_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.recordAuditTx(tx, job.TenantID, principal.SubjectID, "embedding_migration_cutover", job.ID,
			fmt.Sprintf("%s/%d -> %s/%d", job.SourceModel, job.SourceDimension, job.TargetModel, job.TargetDimension), now)
	})
	if err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("cut over migration: %w", err)
	}
	return s.load(ctx, job.ID)
}

// Rollback returns the binding to the source space while the source variants
// still exist. It is only available before finalize.
func (s *Service) Rollback(ctx context.Context, principal authz.Principal, knowledgeBaseID, migrationID string) (models.EmbeddingMigration, error) {
	if err := s.authorizeWrite(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.EmbeddingMigration
		if err := lockForUpdate(tx).Where("id = ? AND tenant_id = ?", migrationID, principal.TenantID).Take(&job).Error; err != nil {
			return err
		}
		if job.KnowledgeBaseID != knowledgeBaseID {
			return apperror.ErrNotFound
		}
		if job.Status != StatusCutover {
			return apperror.Invalidf("migration is %s, only a cut-over migration can be rolled back", job.Status)
		}
		now := time.Now()
		if err := tx.Model(&models.KnowledgeBase{}).Where("id = ? AND tenant_id = ?", knowledgeBaseID, principal.TenantID).
			Updates(map[string]any{"embedding_model": job.SourceModel, "embedding_dimension": job.SourceDimension, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusRolledBack, "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.recordAuditTx(tx, job.TenantID, principal.SubjectID, "embedding_migration_rolled_back", job.ID, "restored source embedding binding", now)
	})
	if err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("roll back migration: %w", err)
	}
	return s.load(ctx, migrationID)
}

// Cancel stops an active migration and removes target rows. The source space is
// untouched, so retrieval is unaffected.
func (s *Service) Cancel(ctx context.Context, principal authz.Principal, knowledgeBaseID, migrationID string) (models.EmbeddingMigration, error) {
	if err := s.authorizeWrite(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.EmbeddingMigration
		if err := lockForUpdate(tx).Where("id = ? AND tenant_id = ?", migrationID, principal.TenantID).Take(&job).Error; err != nil {
			return err
		}
		if job.KnowledgeBaseID != knowledgeBaseID {
			return apperror.ErrNotFound
		}
		if job.Status == StatusCutover || job.Status == StatusFinalized {
			return apperror.Invalidf("a cut-over migration must be rolled back, not cancelled")
		}
		if job.Status == StatusCancelled || job.Status == StatusRolledBack || job.Status == StatusFailed {
			return nil
		}
		if err := tx.Where("knowledge_base_id = ? AND model = ?", job.KnowledgeBaseID, job.TargetModel).
			Delete(&models.ChunkEmbedding{}).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusCancelled, "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.recordAuditTx(tx, job.TenantID, principal.SubjectID, "embedding_migration_cancelled", job.ID, "target space removed", now)
	})
	if err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("cancel migration: %w", err)
	}
	return s.load(ctx, migrationID)
}

// Finalize ends the rollback window by deleting the source variants. The
// knowledge base stays on the target space.
func (s *Service) Finalize(ctx context.Context, principal authz.Principal, knowledgeBaseID, migrationID string) (models.EmbeddingMigration, error) {
	if err := s.authorizeWrite(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.EmbeddingMigration
		if err := lockForUpdate(tx).Where("id = ? AND tenant_id = ?", migrationID, principal.TenantID).Take(&job).Error; err != nil {
			return err
		}
		if job.KnowledgeBaseID != knowledgeBaseID {
			return apperror.ErrNotFound
		}
		if job.Status != StatusCutover {
			return apperror.Invalidf("migration is %s, only a cut-over migration can be finalized", job.Status)
		}
		if err := tx.Where("knowledge_base_id = ? AND model = ?", job.KnowledgeBaseID, job.SourceModel).
			Delete(&models.ChunkEmbedding{}).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).
			Updates(map[string]any{"status": StatusFinalized, "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return s.recordAuditTx(tx, job.TenantID, principal.SubjectID, "embedding_migration_finalized", job.ID, "source space removed", now)
	})
	if err != nil {
		return models.EmbeddingMigration{}, fmt.Errorf("finalize migration: %w", err)
	}
	return s.load(ctx, migrationID)
}

// Latest returns the most recent migration for a knowledge base. A missing row
// is reported as not found so the UI can offer the start action.
func (s *Service) Latest(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (models.EmbeddingMigration, error) {
	if err := s.authorizeRead(ctx, principal, knowledgeBaseID); err != nil {
		return models.EmbeddingMigration{}, err
	}
	var job models.EmbeddingMigration
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("knowledge_base_id = ?", knowledgeBaseID).
		Order("created_at DESC").Take(&job).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.EmbeddingMigration{}, apperror.ErrNotFound
		}
		return models.EmbeddingMigration{}, fmt.Errorf("load migration: %w", err)
	}
	return job, nil
}

// lockForUpdate serializes cutover/rollback/cancel on PostgreSQL. SQLite has no
// row locking and is only used by unit tests, where the tests are sequential.
func lockForUpdate(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "postgres" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

func (s *Service) load(ctx context.Context, id string) (models.EmbeddingMigration, error) {
	var job models.EmbeddingMigration
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&job).Error; err != nil {
		return models.EmbeddingMigration{}, err
	}
	return job, nil
}

func (s *Service) authorizeWrite(ctx context.Context, principal authz.Principal, knowledgeBaseID string) error {
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, knowledgeBaseID, "write")
	if err != nil {
		return err
	}
	if !allowed {
		return apperror.ErrForbidden
	}
	return nil
}

func (s *Service) authorizeRead(ctx context.Context, principal authz.Principal, knowledgeBaseID string) error {
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, knowledgeBaseID, "read")
	if err != nil {
		return err
	}
	if !allowed {
		return apperror.ErrForbidden
	}
	return nil
}

func (s *Service) recordAuditTx(tx *gorm.DB, tenantID, actorID, action, resourceID, reason string, now time.Time) error {
	return tx.Create(&models.AuditLog{
		TenantID: tenantID, ActorID: actorID, Action: action,
		ResourceType: "embedding_migration", ResourceID: resourceID, Allowed: true, Reason: reason,
	}).Error
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}
