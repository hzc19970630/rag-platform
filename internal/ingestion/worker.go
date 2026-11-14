package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/contracts/formats"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	inboxProcessing = pipeline.InboxProcessing
	inboxSucceeded  = pipeline.InboxSucceeded
	inboxFailed     = pipeline.InboxFailed
)

// DefaultChunking is used only when a caller constructs a worker without an
// explicit chunker. Production wiring resolves the policy from configuration.
var DefaultChunking = NewRuneChunker(RuneChunkerOptions{Size: 900, Overlap: 120})

type PipelineWorker struct {
	db                *gorm.DB
	objects           ObjectStore
	reader            DocumentReader
	embedder          Embedder
	embeddingResolver EmbeddingResolver
	indexWriter       EvidenceIndexWriter
	chunker           Chunker
	chunkingResolver  ChunkingResolver
	timeout           time.Duration
	projection        ProjectionTrigger
	leaseOwner        inboxlease.Owner
	leaseTTL          time.Duration
	formats           formats.Registry
}

// ChunkingResolver resolves the chunking policy for one tenant. The worker
// calls it per document, so a policy change applies to the next ingestion
// without rewriting existing chunks.
type ChunkingResolver interface {
	ChunkerForTenant(ctx context.Context, tenantID string) (Chunker, error)
}

// SetChunkingResolver wires the tenant-aware chunking policy. A nil resolver
// keeps the single policy passed to NewPipelineWorker.
func (w *PipelineWorker) SetChunkingResolver(resolver ChunkingResolver) {
	w.chunkingResolver = resolver
}

// EmbeddingResolver resolves the exact vector space for one knowledge base.
// The worker calls it per document, so a knowledge base pinned to a tenant
// embedding provider is written in that provider's space.
type EmbeddingResolver interface {
	ResolveEmbedding(ctx context.Context, tenantID, knowledgeBaseID string) (embedding.Binding, Embedder, error)
}

// SetFormatRegistry replaces the accepted-format set.
func (w *PipelineWorker) SetFormatRegistry(registry formats.Registry) {
	if registry != nil {
		w.formats = registry
	}
}

// SetEmbeddingResolver wires per-knowledge-base embedding routing. A nil
// resolver keeps the single adapter passed to NewPipelineWorker.
func (w *PipelineWorker) SetEmbeddingResolver(resolver EmbeddingResolver) {
	w.embeddingResolver = resolver
}

func (w *PipelineWorker) runtime() *pipeline.Runtime {
	return pipeline.NewRuntime(w.db, &w.leaseOwner, w.leaseTTL)
}

// ProjectionTrigger is the Pipeline B port. It is optional: when projection is
// disabled, or when one trigger fails, the Evidence Index result is unaffected.
type ProjectionTrigger interface {
	TriggerWiki(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error
	TriggerRelation(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error
	// InvalidateProjectionState drops cached expected hashes before the triggers
	// run, so a failed trigger cannot leave ProjectionJoin trusting stale state.
	InvalidateProjectionState(ctx context.Context, tenantID, knowledgeBaseID string) error
}

func (w *PipelineWorker) SetProjectionTrigger(trigger ProjectionTrigger) {
	w.projection = trigger
}

func NewPipelineWorker(db *gorm.DB, objects ObjectStore, reader DocumentReader, embedder Embedder, indexWriter EvidenceIndexWriter, chunker Chunker, timeout time.Duration) *PipelineWorker {
	if chunker == nil {
		chunker = DefaultChunking
	}
	return &PipelineWorker{db: db, objects: objects, reader: reader, embedder: embedder, indexWriter: indexWriter, chunker: chunker, timeout: timeout, formats: formats.Default()}
}

func (w *PipelineWorker) Process(ctx context.Context, task Task) error {
	if task.SchemaVersion != SchemaTask || task.Stage == "" || task.DocumentVersionID == "" || task.PipelineRunID == "" {
		return fmt.Errorf("invalid ingestion task envelope")
	}
	claimed, err := w.claimInbox(ctx, task)
	if err != nil || !claimed {
		return err
	}
	processCtx, releaseLease, retainLease, leaseLost := w.runtime().Heartbeat(ctx, task.IdempotencyKey)
	retainOnReturn := false
	defer func() {
		if retainOnReturn {
			retainLease()
			return
		}
		releaseLease()
	}()
	current, err := w.isCurrent(ctx, task)
	if err != nil {
		return err
	}
	if !current {
		return w.finishInbox(ctx, task.IdempotencyKey, inboxSucceeded, nil)
	}
	if err := w.markStageProcessing(ctx, task); err != nil {
		return err
	}
	if w.timeout > 0 {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithTimeout(processCtx, w.timeout)
		defer cancel()
	}
	var processErr error
	switch task.Stage {
	case StageParse:
		processErr = w.parse(processCtx, task)
	case StageNormalize:
		processErr = w.normalize(processCtx, task)
	case StageChunk:
		processErr = w.chunk(processCtx, task)
	case StageEmbed:
		processErr = w.embed(processCtx, task)
	case StageIndex:
		processErr = w.index(processCtx, task)
	default:
		processErr = fmt.Errorf("unsupported stage %q", task.Stage)
	}
	// The lease can expire while a slow stage runs. Another worker then owns the
	// row, so this attempt must not write stage or inbox outcomes over it.
	if leaseLost() {
		return fmt.Errorf("inbox lease lost for %s: %w", task.IdempotencyKey, context.Canceled)
	}
	if processErr != nil {
		terminal := apperror.IsTerminal(processErr)
		code, message := splitPipelineError(processErr)
		if err := w.failStage(ctx, task, code, message); err != nil {
			return err
		}
		if terminal {
			return w.finishInbox(ctx, task.IdempotencyKey, inboxFailed, processErr)
		}
		if err := pipeline.RetainFailedInbox(ctx, w.db, task.IdempotencyKey, processErr.Error()); err != nil {
			return err
		}
		retainOnReturn = true
		return processErr
	}
	return w.completeStage(ctx, task)
}

// readNormalizedDocument decodes the stored normalized document as a stream.
// Normalized JSON can approach the source file size, so it must not be buffered
// whole just to be decoded.
func readNormalizedDocument(ctx context.Context, objects ObjectStore, key string) (NormalizedDocument, error) {
	reader, err := objects.Open(ctx, key)
	if err != nil {
		return NormalizedDocument{}, fmt.Errorf("load normalized document: %w", err)
	}
	defer reader.Close()
	var normalized NormalizedDocument
	if err := json.NewDecoder(reader).Decode(&normalized); err != nil {
		return NormalizedDocument{}, fmt.Errorf("decode normalized document: %w", err)
	}
	return normalized, nil
}

func (w *PipelineWorker) parse(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	content, err := w.objects.Get(ctx, version.SourceObjectKey)
	if err != nil {
		return fmt.Errorf("load source object: %w", err)
	}
	source := SourceObject{SchemaVersion: SchemaSourceObject, Envelope: Envelope{SchemaVersion: SchemaSourceObject, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, ContentHash: version.ContentHash}, ObjectKey: version.SourceObjectKey, Filename: filenameFromKey(version.SourceObjectKey), MimeType: w.mimeForFilename(version.SourceObjectKey), SizeBytes: int64(len(content))}
	result, err := w.reader.Parse(ctx, source, content)
	if err != nil {
		return fmt.Errorf("parser_error: %w", err)
	}
	if result.ErrorCode != "" {
		if terminalParserCode(result.ErrorCode) {
			return apperror.Terminalf(result.ErrorCode, "%s", result.ErrorMessage)
		}
		return fmt.Errorf("%s: %s", result.ErrorCode, result.ErrorMessage)
	}
	if len(result.Blocks) == 0 {
		return apperror.Terminalf("empty_document", "parser returned no text blocks")
	}
	normalized := NormalizedDocument{SchemaVersion: SchemaNormalizedDocument, Envelope: Envelope{SchemaVersion: SchemaNormalizedDocument, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: task.PipelineRunID, StageRunID: task.StageRunID, TraceID: task.TraceID, RequestID: task.RequestID, ActorID: task.ActorID, IdempotencyKey: task.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC()}, Title: result.Title, Language: result.Language, ParserVersion: result.ParserVersion, Blocks: result.Blocks}
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal normalized document: %w", err)
	}
	key := strings.TrimSuffix(version.SourceObjectKey, "/source/"+filenameFromKey(version.SourceObjectKey)) + "/normalized.json"
	if err := w.objects.Put(ctx, key, data, "application/json"); err != nil {
		return fmt.Errorf("store normalized document: %w", err)
	}
	return w.db.WithContext(ctx).Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusParsed, map[string]any{"normalized_key": key, "updated_at": time.Now()})).Error
}

func (w *PipelineWorker) normalize(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	normalized, err := readNormalizedDocument(ctx, w.objects, version.NormalizedKey)
	if err != nil {
		return err
	}
	blocks := normalized.Blocks[:0]
	for _, block := range normalized.Blocks {
		block.Text = cleanText(block.Text)
		block.HeadingPath = cleanText(block.HeadingPath)
		if block.Text != "" {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return apperror.Terminalf("empty_document", "normalized document has no text")
	}
	normalized.Blocks = blocks
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal cleaned document: %w", err)
	}
	if err := w.objects.Put(ctx, version.NormalizedKey, data, "application/json"); err != nil {
		return fmt.Errorf("store cleaned document: %w", err)
	}
	return w.db.WithContext(ctx).Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusNormalized, map[string]any{"updated_at": time.Now()})).Error
}

type chunkRow struct {
	ID, ParentID, Content, ContentHash, HeadingPath, SourcePath string
	Ordinal, PageNumber, StartOffset, EndOffset                 int
}

// chunkDocuments maps the rows just written into the searchable documents an
// external keyword/hybrid adapter receives. Chunk persistence stays in this
// package; only the derived index is handed to the port.
func chunkDocuments(rows []chunkRow) []ChunkDocument {
	documents := make([]ChunkDocument, 0, len(rows))
	for _, row := range rows {
		status := StatusParent
		if row.ParentID != "" {
			status = StatusChunked
		}
		documents = append(documents, ChunkDocument{
			ID: row.ID, ParentID: row.ParentID, Ordinal: row.Ordinal, Content: row.Content,
			ContentHash: row.ContentHash, HeadingPath: row.HeadingPath, PageNumber: row.PageNumber,
			SourcePath: row.SourcePath, StartOffset: row.StartOffset, EndOffset: row.EndOffset, Status: status,
		})
	}
	return documents
}

// chunkInsertBatchSize bounds how many chunk rows share one INSERT statement.
// Each row binds 17 parameters, so the default stays far below PostgreSQL's
// 65535-parameter limit.
var chunkInsertBatchSize = 500

const (
	chunkInsertColumns     = 17
	chunkInsertPlaceholder = "(?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)"
)

// insertChunks writes rows in multi-row INSERT batches. The explicit timestamps
// keep SQLite test schemas (which have no column defaults) aligned with
// PostgreSQL without depending on database-side defaults.
func insertChunks(tx *gorm.DB, version models.DocumentVersion, rows []chunkRow, now time.Time) error {
	for start := 0; start < len(rows); start += chunkInsertBatchSize {
		end := start + chunkInsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*chunkInsertColumns)
		for _, item := range batch {
			chunkStatus := StatusParent
			if item.ParentID != "" {
				chunkStatus = StatusChunked
			}
			placeholders = append(placeholders, chunkInsertPlaceholder)
			args = append(args, item.ID, version.TenantID, version.KnowledgeBaseID, version.ID, item.ParentID, item.Ordinal, item.Content, item.ContentHash, item.HeadingPath, item.PageNumber, item.SourcePath, item.StartOffset, item.EndOffset, chunkStatus, now, now)
		}
		query := `INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, ordinal, content, content_hash, heading_path, page_number, source_path, start_offset, end_offset, embedding_dimension, status, created_at, updated_at) VALUES ` + strings.Join(placeholders, ", ")
		if err := tx.Exec(query, args...).Error; err != nil {
			return fmt.Errorf("insert chunk batch: %w", err)
		}
	}
	return nil
}

func (w *PipelineWorker) chunk(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	normalized, err := readNormalizedDocument(ctx, w.objects, version.NormalizedKey)
	if err != nil {
		return err
	}
	chunker := w.chunker
	if w.chunkingResolver != nil {
		resolved, err := w.chunkingResolver.ChunkerForTenant(ctx, version.TenantID)
		if err != nil {
			return fmt.Errorf("resolve chunking policy: %w", err)
		}
		if resolved != nil {
			chunker = resolved
		}
	}
	rows := make([]chunkRow, 0, len(normalized.Blocks)*2)
	childOrdinal := 0
	for blockIndex, block := range normalized.Blocks {
		parentID := stableID("parent", version.ID, fmt.Sprint(blockIndex), block.Text)
		rows = append(rows, chunkRow{ID: parentID, Content: block.Text, ContentHash: hashString(block.Text), HeadingPath: block.HeadingPath, PageNumber: block.PageNumber, SourcePath: block.SourcePath, StartOffset: block.StartOffset, EndOffset: block.EndOffset, Ordinal: blockIndex})
		for _, content := range chunker.Split(block.Text) {
			content = cleanText(content)
			if content == "" {
				continue
			}
			rows = append(rows, chunkRow{ID: stableID("chunk", version.ID, fmt.Sprint(childOrdinal), content), ParentID: parentID, Content: content, ContentHash: hashString(content), HeadingPath: block.HeadingPath, PageNumber: block.PageNumber, SourcePath: block.SourcePath, StartOffset: block.StartOffset, EndOffset: block.EndOffset, Ordinal: childOrdinal})
			childOrdinal++
		}
	}
	if len(rows) == 0 {
		return apperror.Terminalf("empty_document", "chunker returned no chunks")
	}
	// A reprocessed version reuses its id, so the old index entries must be
	// removed before the new chunks are written. PostgreSQL is a no-op; an
	// external index adapter performs the delete.
	if w.indexWriter != nil {
		if err := w.indexWriter.DeleteVersion(ctx, version.ID); err != nil {
			return fmt.Errorf("delete previous index: %w", err)
		}
	}
	if err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("document_version_id = ?", version.ID).Delete(&models.Chunk{}).Error; err != nil {
			return err
		}
		if err := insertChunks(tx, version, rows, time.Now()); err != nil {
			return err
		}
		return tx.Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusChunked, nil)).Error
	}); err != nil {
		return err
	}
	if w.indexWriter == nil {
		return fmt.Errorf("keyword_index_error: evidence index writer is not configured")
	}
	// The rows are committed before the derived index is notified, so a failed
	// external write is retried against the same version id.
	if err := w.indexWriter.IndexChunks(ctx, version.ID, chunkDocuments(rows)); err != nil {
		return fmt.Errorf("keyword_index_error: %w", err)
	}
	return nil
}

func (w *PipelineWorker) embeddingFor(ctx context.Context, tenantID, knowledgeBaseID string) (embedding.Binding, Embedder, error) {
	if w.embeddingResolver != nil {
		return w.embeddingResolver.ResolveEmbedding(ctx, tenantID, knowledgeBaseID)
	}
	if w.embedder == nil {
		return embedding.Binding{}, nil, fmt.Errorf("embedding adapter is not configured")
	}
	return embedding.Binding{Model: w.embedder.Name(), Dimension: w.embedder.Dimension()}, w.embedder, nil
}

func (w *PipelineWorker) embed(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	// A managed migration rewrites this knowledge base's space in batches. New
	// writes during that window would be missing from the target verification,
	// so ingestion waits for the operator to finish or cancel the migration.
	var migrating int64
	if err := w.db.WithContext(ctx).Table("embedding_migrations").
		Where("knowledge_base_id = ? AND status IN ?", version.KnowledgeBaseID,
			[]string{"pending", "building_index", "reembedding", "verifying", "ready"}).
		Count(&migrating).Error; err != nil {
		return fmt.Errorf("check embedding migration: %w", err)
	}
	if migrating > 0 {
		return apperror.Terminalf("embedding_migration_in_progress", "knowledge base %s has an active embedding migration", version.KnowledgeBaseID)
	}
	binding, adapter, err := w.embeddingFor(ctx, version.TenantID, version.KnowledgeBaseID)
	if err != nil {
		// A missing, disabled or mismatched embedding provider is a
		// configuration error; retrying the same document would reproduce it.
		return apperror.Terminalf("embedding_binding_unavailable", "%v", err)
	}
	if adapter == nil {
		return apperror.Terminalf("embedding_binding_unavailable", "knowledge base %s has no embedding adapter", version.KnowledgeBaseID)
	}
	var chunks []models.Chunk
	// A restarted pipeline can find chunks that its previous attempt already
	// embedded; re-embedding them is idempotent and avoids reporting an empty
	// document for work that actually exists.
	if err := w.db.WithContext(ctx).Where("document_version_id = ? AND parent_id IS NOT NULL AND status IN ?", version.ID, []string{StatusChunked, StatusEmbedded}).Order("ordinal").Find(&chunks).Error; err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	if len(chunks) == 0 {
		return apperror.Terminalf("empty_document", "no child chunks to embed")
	}
	texts := make([]string, len(chunks))
	for i := range chunks {
		texts[i] = chunks[i].Content
	}
	vectors, err := adapter.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding_error: %w", err)
	}
	if len(vectors) != len(chunks) {
		return fmt.Errorf("embedding_error: vector count mismatch")
	}
	if w.indexWriter == nil {
		return fmt.Errorf("embedding_error: evidence index writer is not configured")
	}
	records := make([]EmbeddingRecord, len(chunks))
	for index, chunk := range chunks {
		if len(vectors[index]) != binding.Dimension {
			return fmt.Errorf("embedding_error: vector dimension mismatch for chunk %s: got %d, want %d", chunk.ID, len(vectors[index]), binding.Dimension)
		}
		records[index] = EmbeddingRecord{ChunkID: chunk.ID, Vector: vectors[index]}
	}
	if err := w.indexWriter.WriteEmbeddings(ctx, version.ID, binding.Model, records); err != nil {
		return fmt.Errorf("embedding_error: %w", err)
	}
	return nil
}

func (w *PipelineWorker) index(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	var chunks []models.Chunk
	// Indexing is also replayed after a restart, so chunks that a previous
	// attempt already marked ready still belong in the package.
	if err := w.db.WithContext(ctx).Where("document_version_id = ? AND parent_id IS NOT NULL AND status IN ?", version.ID, []string{StatusEmbedded, StatusEvidenceReady}).Order("ordinal").Find(&chunks).Error; err != nil {
		return fmt.Errorf("load embedded chunks: %w", err)
	}
	if len(chunks) == 0 {
		return apperror.Terminalf("empty_document", "no embedded chunks to index")
	}
	if w.indexWriter == nil {
		return fmt.Errorf("evidence index writer is not configured")
	}
	if err := w.indexWriter.IndexVersion(ctx, version.ID); err != nil {
		return fmt.Errorf("keyword_index_error: %w", err)
	}
	var knowledgeBase struct{ ACLVersion int64 }
	if err := w.db.WithContext(ctx).Table("knowledge_bases").Select("acl_version").Where("id = ?", version.KnowledgeBaseID).Scan(&knowledgeBase).Error; err != nil {
		return fmt.Errorf("load knowledge base acl: %w", err)
	}
	var allChunks []models.Chunk
	if err := w.db.WithContext(ctx).Where("document_version_id = ? AND status IN ?", version.ID, []string{StatusParent, StatusEmbedded, StatusEvidenceReady}).Order("ordinal, id").Find(&allChunks).Error; err != nil {
		return fmt.Errorf("load package chunks: %w", err)
	}
	packageChunks := projectChunks(allChunks)
	packageID := task.PackageID
	if packageID == "" {
		packageID = stableID("pkg", version.ID)
	}
	packageValue := EvidencePackage{ID: packageID, SchemaVersion: SchemaEvidencePackage, Envelope: Envelope{SchemaVersion: SchemaEvidencePackage, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: task.PipelineRunID, StageRunID: task.StageRunID, TraceID: task.TraceID, RequestID: task.RequestID, ActorID: task.ActorID, IdempotencyKey: task.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC()}, ContentHash: version.ContentHash, ACLVersion: knowledgeBase.ACLVersion, Status: StatusEvidenceReady, Chunks: packageChunks}
	data, err := json.Marshal(packageValue)
	if err != nil {
		return fmt.Errorf("marshal evidence package: %w", err)
	}
	now := time.Now()
	if err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, chunk := range chunks {
			if err := tx.Model(&models.Chunk{}).Where("id = ? AND document_version_id = ?", chunk.ID, version.ID).Update("status", StatusEvidenceReady).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("document_version_id = ?", version.ID).Delete(&models.EvidencePackage{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.EvidencePackage{ID: packageID, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentVersionID: version.ID, ContentHash: version.ContentHash, ACLVersion: knowledgeBase.ACLVersion, Status: StatusEvidenceReady, PackageJSON: string(data), PublishedAt: &now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusEvidenceReady, nil)).Error; err != nil {
			return err
		}
		return tx.Model(&models.Document{}).Where("id = ? AND latest_version_id = ?", version.DocumentID, version.ID).Update("status", StatusEvidenceReady).Error
	}); err != nil {
		return err
	}
	w.triggerProjection(ctx, version.TenantID, version.KnowledgeBaseID, packageID, knowledgeBase.ACLVersion)
	return nil
}

// triggerProjection starts both Pipeline B projections after the
// EvidencePackage is committed. Each uses a detached context so an API/worker
// shutdown cannot strand the outbox event, and each logs rather than returning:
// a Wiki or relation failure must never turn a successful Evidence indexing
// into a failed ingestion stage.
func (w *PipelineWorker) triggerProjection(ctx context.Context, tenantID, knowledgeBaseID, packageID string, aclVersion int64) {
	if w.projection == nil {
		return
	}
	triggerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.projection.InvalidateProjectionState(triggerCtx, tenantID, knowledgeBaseID); err != nil {
		slog.Warn("projection_state_invalidate_failed", "knowledge_base_id", knowledgeBaseID, "error", err)
	}
	if err := w.projection.TriggerWiki(triggerCtx, tenantID, knowledgeBaseID, packageID, aclVersion); err != nil {
		slog.Warn("wiki_projection_trigger_failed", "knowledge_base_id", knowledgeBaseID, "package_id", packageID, "error", err)
	}
	if err := w.projection.TriggerRelation(triggerCtx, tenantID, knowledgeBaseID, packageID, aclVersion); err != nil {
		slog.Warn("relation_projection_trigger_failed", "knowledge_base_id", knowledgeBaseID, "package_id", packageID, "error", err)
	}
}

func (w *PipelineWorker) claimInbox(ctx context.Context, task Task) (bool, error) {
	return w.runtime().Claim(ctx, task.IdempotencyKey, task.TaskType, parseUint(task.StageRunID))
}

func (w *PipelineWorker) markStageProcessing(ctx context.Context, task Task) error {
	return w.runtime().Start(ctx, workerStage{task: task})
}

func (w *PipelineWorker) completeStage(ctx context.Context, task Task) error {
	return w.runtime().Complete(ctx, task.IdempotencyKey, workerStage{task: task})
}

func (w *PipelineWorker) failStage(ctx context.Context, task Task, code, message string) error {
	return w.runtime().Fail(ctx, workerStage{task: task}, code, message)
}

// workerStage adapts one ingestion stage to the shared pipeline runtime. It
// keeps the ingestion state machine and row layout inside this package.
type workerStage struct {
	task Task
}

func (s workerStage) Start(tx *gorm.DB, now time.Time) error {
	var pipelineRun models.PipelineRun
	if err := tx.First(&pipelineRun, "id = ?", s.task.PipelineRunID).Error; err != nil {
		return err
	}
	target := StatusForStage(s.task.Stage)
	if err := ValidateTransition(pipelineRun.Status, target); err != nil {
		return err
	}
	if err := tx.Model(&pipelineRun).Updates(map[string]any{"status": target, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Model(&models.StageRun{}).Where("idempotency_key = ?", s.task.IdempotencyKey).Updates(map[string]any{"status": StageProcessing, "attempts": gorm.Expr("attempts + 1"), "started_at": &now, "updated_at": now}).Error
}

func (s workerStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	task := s.task
	var pipelineRun models.PipelineRun
	if err := tx.First(&pipelineRun, "id = ?", task.PipelineRunID).Error; err != nil {
		return nil, err
	}
	completedStatus := CompletedStatusForStage(task.Stage)
	if err := ValidateTransition(pipelineRun.Status, completedStatus); err != nil {
		return nil, err
	}
	if err := tx.Model(&models.StageRun{}).Where("idempotency_key = ?", task.IdempotencyKey).Updates(statusFields(StageSucceeded, map[string]any{"completed_at": &now, "updated_at": now})).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&models.PipelineRun{}).Where("id = ? AND status <> ?", task.PipelineRunID, StatusCancelled).Updates(statusFields(completedStatus, map[string]any{"updated_at": now})).Error; err != nil {
		return nil, err
	}
	next, hasNext := NextStage(task.Stage)
	if !hasNext {
		return nil, nil
	}
	stageKey := task.PipelineRunID + ":" + next
	var nextStage models.StageRun
	if err := tx.Where("idempotency_key = ?", stageKey).First(&nextStage).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		nextStage = models.StageRun{PipelineRunID: task.PipelineRunID, Stage: next, Status: StagePending, IdempotencyKey: stageKey}
		if err := tx.Create(&nextStage).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	nextTask := task
	nextTask.Stage = next
	nextTask.StageRunID = fmt.Sprint(nextStage.ID)
	nextTask.IdempotencyKey = stageKey
	payload, err := json.Marshal(nextTask)
	if err != nil {
		return nil, err
	}
	return &models.OutboxEvent{
		TenantID: task.TenantID, AggregateType: "pipeline_run", AggregateID: task.PipelineRunID,
		EventType: StageEventType(task.Stage), IdempotencyKey: stageKey, Payload: string(payload),
	}, nil
}

func (s workerStage) Fail(tx *gorm.DB, code, message string, now time.Time) error {
	return failStageRecords(tx, s.task, code, message, now)
}

// failStageRecords is the single failure projection for ingestion: the stage,
// the pipeline and the document version move together. Reconcile uses it too,
// so a retry-budget failure and a worker failure cannot diverge. A record that
// another writer already settled (evidence ready, cancelled) is left alone.
func failStageRecords(tx *gorm.DB, task Task, code, message string, now time.Time) error {
	if err := tx.Model(&models.StageRun{}).Where("idempotency_key = ? AND status <> ?", task.IdempotencyKey, StageCancelled).Updates(map[string]any{
		"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.PipelineRun{}).Where("id = ? AND tenant_id = ? AND status NOT IN ?", task.PipelineRunID, task.TenantID, []string{StatusEvidenceReady, StatusCancelled}).Updates(map[string]any{
		"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&models.DocumentVersion{}).Where("id = ? AND tenant_id = ? AND status <> ?", task.DocumentVersionID, task.TenantID, StatusCancelled).Updates(map[string]any{
		"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error
}

// statusFields builds the field set for a status change that is not a failure.
// It clears the error fields, so a stage that failed and then succeeded on retry
// stops reporting the failed attempt as the record's current state. The task
// inbox already does this when it claims a task; the pipeline records did not.
func statusFields(status string, extra map[string]any) map[string]any {
	fields := make(map[string]any, len(extra)+3)
	for key, value := range extra {
		fields[key] = value
	}
	fields["status"] = status
	fields["error_code"] = ""
	fields["error_message"] = ""
	return fields
}

func (w *PipelineWorker) finishInbox(ctx context.Context, key, status string, processErr error) error {
	return w.runtime().FinishInbox(ctx, key, status, processErr)
}

func (w *PipelineWorker) isCurrent(ctx context.Context, task Task) (bool, error) {
	var count int64
	if err := w.db.WithContext(ctx).Table("documents d").Joins("JOIN document_versions v ON v.document_id = d.id").Where("d.tenant_id = ? AND d.knowledge_base_id = ? AND d.id = ? AND d.latest_version_id = ? AND v.id = ? AND d.status <> ?", task.TenantID, task.KnowledgeBaseID, task.DocumentID, task.DocumentVersionID, task.DocumentVersionID, StatusCancelled).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check document currency: %w", err)
	}
	return count == 1, nil
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func cleanText(value string) string {
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(value, " "))
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:])[:48]
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func splitPipelineError(err error) (string, string) {
	if terminal, ok := apperror.AsTerminal(err); ok {
		message := ""
		if terminal.Err != nil {
			message = terminal.Err.Error()
		}
		return terminal.Code, message
	}
	value := err.Error()
	parts := strings.SplitN(value, ": ", 2)
	if len(parts) == 1 {
		parts = strings.SplitN(value, ":", 2)
	}
	if len(parts) == 1 {
		return "pipeline_error", parts[0]
	}
	return parts[0], parts[1]
}

func parseUint(value string) uint64 {
	var result uint64
	_, _ = fmt.Sscan(value, &result)
	return result
}

func filenameFromKey(value string) string {
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}

func (w *PipelineWorker) mimeForFilename(value string) string {
	registry := w.formats
	if registry == nil {
		registry = formats.Default()
	}
	return registry.MIMEForFilename(value)
}

// terminalParserCode marks parser rejections that cannot succeed on retry:
// the bytes are what they are, so another attempt only burns the attempt budget.
func terminalParserCode(code string) bool {
	switch code {
	case "unsupported_scanned_pdf", "unsupported_legacy_office", "unsupported_corrupt_document", "document_too_complex":
		return true
	default:
		return false
	}
}
