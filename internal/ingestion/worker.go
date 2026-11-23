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
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	db                 *gorm.DB
	objects            ObjectStore
	reader             DocumentReader
	embedder           Embedder
	embeddingResolver  EmbeddingResolver
	indexWriter        EvidenceIndexWriter
	chunker            Chunker
	chunkingResolver   ChunkingResolver
	timeout            time.Duration
	streamChunkBytes   int64
	parseTimeout       time.Duration
	embeddingWarning   int
	embeddingMaxChunks int
	embeddingTimeout   time.Duration
	metrics            *observability.Registry
	projection         ProjectionTrigger
	leaseOwner         inboxlease.Owner
	leaseTTL           time.Duration
	formats            formats.Registry
	materializer       TableMaterializer
}

// ChunkingResolver resolves the chunking policy for one tenant. The worker
// calls it per document, so a policy change applies to the next ingestion
// without rewriting existing chunks.
type ChunkingResolver interface {
	ChunkerFor(ctx context.Context, tenantID, knowledgeBaseID string) (Chunker, error)
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
	// NoteEvidenceChange appends one ordered projection intent. Batch scheduling
	// turns the intents into runs after the quiet window; ingestion no longer
	// creates one run per published package.
	NoteEvidenceChange(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string) error
	// InvalidateProjectionState drops cached expected hashes before the triggers
	// run, so a failed trigger cannot leave ProjectionJoin trusting stale state.
	InvalidateProjectionState(ctx context.Context, tenantID, knowledgeBaseID string) error
}

func (w *PipelineWorker) SetProjectionTrigger(trigger ProjectionTrigger) {
	w.projection = trigger
}

// SetParseTransport switches parsing to the streaming docreader RPC and gives
// the parse stage its own deadline. Zero values keep the unary path.
func (w *PipelineWorker) SetParseTransport(chunkBytes int64, timeout time.Duration) {
	w.streamChunkBytes = chunkBytes
	w.parseTimeout = timeout
}

// SetEmbeddingBudget guards large tables before embedding. warning<=0 disables
// the warning; maxChunks<=0 disables the hard failure.
func (w *PipelineWorker) SetEmbeddingBudget(warning, maxChunks int) {
	w.embeddingWarning = warning
	w.embeddingMaxChunks = maxChunks
}

// SetEmbeddingTimeout bounds the embed stage independently of the ingestion
// task timeout so a large deep index is not killed by the parse budget.
func (w *PipelineWorker) SetEmbeddingTimeout(timeout time.Duration) {
	w.embeddingTimeout = timeout
}

func (w *PipelineWorker) SetMetrics(registry *observability.Registry) {
	w.metrics = registry
}

// SetTableMaterializer wires the columnar copy writer. When unset (tests,
// deployments without tablehouse) the parser keeps its block-only behavior.
func (w *PipelineWorker) SetTableMaterializer(materializer TableMaterializer) {
	w.materializer = materializer
}

func (w *PipelineWorker) recordParseMetrics(source SourceObject, result ParseResult, err error, elapsed time.Duration) {
	if w.metrics == nil {
		return
	}
	status := "ok"
	if err != nil || result.ErrorCode != "" {
		status = "error"
	}
	format := source.MimeType
	w.metrics.Inc("ingestion_parse_total", metrics.Labels("status", status, "format", format))
	w.metrics.Observe("ingestion_parse_duration", elapsed, metrics.Labels("format", format))
	if result.ErrorCode == "parser_busy" || result.ErrorCode == "spool_unavailable" ||
		(err != nil && (strings.Contains(err.Error(), "parser_busy") || strings.Contains(err.Error(), "spool_unavailable"))) {
		w.metrics.Inc("ingestion_spool_reject_total", nil)
	}
	if status == "ok" {
		w.metrics.Add("ingestion_parse_rows", result.IndexedRows, nil)
		if result.Truncated {
			w.metrics.Inc("ingestion_parse_truncated_total", nil)
		}
	}
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
	if errors.Is(err, inboxlease.ErrStaleDelivery) {
		slog.Info("stale_delivery_acked", "idempotency_key", task.IdempotencyKey)
		return nil
	}
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
		embedCtx := processCtx
		if w.embeddingTimeout > 0 {
			var cancel context.CancelFunc
			embedCtx, cancel = context.WithTimeout(processCtx, w.embeddingTimeout)
			defer cancel()
		}
		processErr = w.embed(embedCtx, task)
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
	if processErr != nil && errors.Is(processErr, apperror.ErrVersionSuperseded) {
		if err := w.cancelSupersededStage(ctx, task); err != nil {
			return err
		}
		return w.finishInbox(ctx, task.IdempotencyKey, inboxSucceeded, nil)
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
	if version.Status == StatusCancelled {
		return apperror.ErrVersionSuperseded
	}
	streaming, streamingOK := w.reader.(StreamingDocumentReader)
	var err error
	var content []byte
	if !streamingOK || w.streamChunkBytes <= 0 {
		content, err = w.objects.Get(ctx, version.SourceObjectKey)
		if err != nil {
			return fmt.Errorf("load source object: %w", err)
		}
	}
	source := SourceObject{SchemaVersion: SchemaSourceObject, Envelope: Envelope{SchemaVersion: SchemaSourceObject, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, ContentHash: version.ContentHash}, ObjectKey: version.SourceObjectKey, Filename: filenameFromKey(version.SourceObjectKey), MimeType: w.mimeForFilename(version.SourceObjectKey), SizeBytes: version.SizeBytes}
	parseCtx := ctx
	if w.parseTimeout > 0 {
		var cancel context.CancelFunc
		parseCtx, cancel = context.WithTimeout(ctx, w.parseTimeout)
		defer cancel()
	}
	parseStarted := time.Now()
	var result ParseResult
	materialized := map[string]TableMaterializeResult{}
	blockStreaming, blockStreamOK := w.reader.(BlockStreamingDocumentReader)
	rowStreaming, rowStreamOK := w.reader.(RowStreamingDocumentReader)
	target := TableMaterializeTarget{
		TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID,
		DocumentID: version.DocumentID, VersionID: version.ID,
	}
	if w.materializer != nil && rowStreamOK && w.streamChunkBytes > 0 {
		if err := w.materializer.ClearVersion(parseCtx, target); err != nil {
			return fmt.Errorf("tablehouse clear: %w", err)
		}
		body, openErr := w.objects.Open(parseCtx, version.SourceObjectKey)
		if openErr != nil {
			return openErr
		}
		defer body.Close()
		result, err = rowStreaming.ParseBlocksStreamRows(parseCtx, source, body, version.SizeBytes, w.streamChunkBytes,
			w.rowForwarder(parseCtx, target, materialized))
	} else if blockStreamOK && w.streamChunkBytes > 0 {
		body, openErr := w.objects.Open(parseCtx, version.SourceObjectKey)
		if openErr != nil {
			return openErr
		}
		defer body.Close()
		result, err = blockStreaming.ParseBlocksStream(parseCtx, source, body, version.SizeBytes, w.streamChunkBytes)
	} else if streamingOK && w.streamChunkBytes > 0 {
		body, openErr := w.objects.Open(parseCtx, version.SourceObjectKey)
		if openErr != nil {
			return openErr
		}
		defer body.Close()
		result, err = streaming.ParseStream(parseCtx, source, body, version.SizeBytes, w.streamChunkBytes)
	} else {
		result, err = w.reader.Parse(parseCtx, source, content)
	}
	w.recordParseMetrics(source, result, err, time.Since(parseStarted))
	if err != nil {
		return fmt.Errorf("parser_error: %w", err)
	}
	slog.Info("document_parsed", "document_version_id", version.ID, "parser_version", result.ParserVersion,
		"blocks", len(result.Blocks), "truncated", result.Truncated, "total_rows", result.TotalRows,
		"indexed_rows", result.IndexedRows, "total_sheets", result.TotalSheets, "indexed_sheets", result.IndexedSheets)
	if result.ErrorCode != "" {
		if terminalParserCode(result.ErrorCode) {
			return apperror.Terminalf(result.ErrorCode, "%s", result.ErrorMessage)
		}
		return fmt.Errorf("%s: %s", result.ErrorCode, result.ErrorMessage)
	}
	if len(result.Blocks) == 0 {
		return apperror.Terminalf("empty_document", "parser returned no text blocks")
	}
	normalized := NormalizedDocument{SchemaVersion: SchemaNormalizedDocument, Envelope: Envelope{SchemaVersion: SchemaNormalizedDocument, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: task.PipelineRunID, StageRunID: task.StageRunID, TraceID: task.TraceID, RequestID: task.RequestID, ActorID: task.ActorID, IdempotencyKey: task.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC()}, Title: result.Title, Language: result.Language, ParserVersion: result.ParserVersion, Blocks: result.Blocks, Truncated: result.Truncated, TotalRows: result.TotalRows, IndexedRows: result.IndexedRows, TotalSheets: result.TotalSheets, IndexedSheets: result.IndexedSheets, Sheets: result.Sheets, StorageMode: result.StorageMode}
	if len(materialized) > 0 {
		normalized.Materialize = materialized
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal normalized document: %w", err)
	}
	key := strings.TrimSuffix(version.SourceObjectKey, "/source/"+filenameFromKey(version.SourceObjectKey)) + "/normalized.json"
	if err := w.objects.Put(ctx, key, data, "application/json"); err != nil {
		return fmt.Errorf("store normalized document: %w", err)
	}
	updated := w.db.WithContext(ctx).Model(&models.DocumentVersion{}).
		Where("id = ? AND status <> ?", version.ID, StatusCancelled).
		Updates(statusFields(StatusParsed, map[string]any{"normalized_key": key, "updated_at": time.Now()}))
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return apperror.ErrVersionSuperseded
	}
	return nil
}

// rowForwarder sends parser row batches to the columnar store. A rejected
// sheet is recorded and skipped without failing the document; a transport
// failure returns an error so the parse stage retries and clears the copy.
func (w *PipelineWorker) rowForwarder(ctx context.Context, target TableMaterializeTarget, results map[string]TableMaterializeResult) func(TableRowBatch) error {
	failed := map[string]bool{}
	return func(batch TableRowBatch) error {
		sheet := batch.Sheet
		if sheet == "" {
			sheet = "default"
		}
		if failed[sheet] {
			return nil
		}
		started := time.Now()
		result, err := w.materializer.WriteBatch(ctx, target, batch)
		status := "ok"
		if err != nil {
			status = "error"
		}
		if w.metrics != nil {
			w.metrics.Inc("ingestion_table_materialize_total", metrics.Labels("status", status))
			w.metrics.Observe("ingestion_table_materialize_duration", time.Since(started), nil)
		}
		if err != nil {
			var sheetErr *TableSheetError
			if errors.As(err, &sheetErr) {
				failed[sheet] = true
				results[sheet] = TableMaterializeResult{Status: MaterializeStatusFailed, Error: sheetErr.Message}
				slog.Warn("table_materialize_sheet_failed", "document_version_id", target.VersionID, "sheet", sheet, "error", sheetErr.Message)
				return nil
			}
			return err
		}
		if result.Status != "" {
			results[sheet] = result
		} else if previous, ok := results[sheet]; !ok {
			results[sheet] = result
		} else {
			previous.Rows = result.Rows
			previous.URI = result.URI
			results[sheet] = previous
		}
		return nil
	}
}

func (w *PipelineWorker) normalize(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	if version.Status == StatusCancelled {
		return apperror.ErrVersionSuperseded
	}
	normalized, err := readNormalizedDocument(ctx, w.objects, version.NormalizedKey)
	if err != nil {
		return err
	}
	blocks := normalized.Blocks[:0]
	for _, block := range normalized.Blocks {
		if block.Kind == "table" {
			block.Text = cleanTableText(block.Text)
		} else {
			block.Text = cleanText(block.Text)
		}
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
	result := w.db.WithContext(ctx).Model(&models.DocumentVersion{}).
		Where("id = ? AND status <> ?", version.ID, StatusCancelled).
		Updates(statusFields(StatusNormalized, map[string]any{"updated_at": time.Now()}))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperror.ErrVersionSuperseded
	}
	return nil
}

type chunkRow struct {
	ID, ParentID, Content, ContentHash, HeadingPath, SourcePath string
	IndexRole, TableAssetID                                     string
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
	chunkInsertColumns     = 19
	chunkInsertPlaceholder = "(?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, COALESCE(NULLIF(?, ''), 'both'), NULLIF(?, ''), ?, ?)"
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
			args = append(args, item.ID, version.TenantID, version.KnowledgeBaseID, version.ID, item.ParentID, item.Ordinal, item.Content, item.ContentHash, item.HeadingPath, item.PageNumber, item.SourcePath, item.StartOffset, item.EndOffset, chunkStatus, item.IndexRole, item.TableAssetID, now, now)
		}
		query := `INSERT INTO chunks (id, tenant_id, knowledge_base_id, document_version_id, parent_id, ordinal, content, content_hash, heading_path, page_number, source_path, start_offset, end_offset, embedding_dimension, status, index_role, table_asset_id, created_at, updated_at) VALUES ` + strings.Join(placeholders, ", ")
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
	if version.Status == StatusCancelled {
		return apperror.ErrVersionSuperseded
	}
	normalized, err := readNormalizedDocument(ctx, w.objects, version.NormalizedKey)
	if err != nil {
		return err
	}
	chunker := w.chunker
	if w.chunkingResolver != nil {
		resolved, err := w.chunkingResolver.ChunkerFor(ctx, version.TenantID, version.KnowledgeBaseID)
		if err != nil {
			return fmt.Errorf("resolve chunking policy: %w", err)
		}
		if resolved != nil {
			chunker = resolved
		}
	}
	var document models.Document
	if err := w.db.WithContext(ctx).First(&document, "id = ?", version.DocumentID).Error; err != nil {
		return fmt.Errorf("load document: %w", err)
	}
	documentMode := document.IndexMode
	if version.IndexMode != "" {
		documentMode = version.IndexMode
	}
	indexMode := resolveIndexMode(documentMode, normalized.Blocks)
	rows := make([]chunkRow, 0, len(normalized.Blocks)*2)
	blockAsset, assets := buildTableAssets(version, normalized.Blocks, normalized.Sheets)
	applyMaterialize(assets, normalized.Materialize)
	profile := tableProfile(normalized.Blocks)
	assetByID := make(map[string]models.TableAsset, len(assets))
	for index := range assets {
		assets[index].IndexMode = resolveSheetIndexMode(documentMode, assets[index], profile)
		assetByID[assets[index].ID] = assets[index]
	}
	if !explicitIndexMode(documentMode) {
		indexMode = aggregateAssetMode(assets, indexMode)
	}
	emittedSummary := map[string]bool{}
	coverage := struct {
		totalRows      int64
		parsedRows     int64
		parsedSheets   int
		totalSheets    int
		keywordRows    int64
		vectorRows     int64
		vectorSheets   map[string]bool
		vectorChars    int64
		storageMode    string
		parseTruncated bool
		coverageKnown  bool
	}{vectorSheets: map[string]bool{}, storageMode: StorageModeFull}
	childOrdinal := 0
	type plannedChunk struct {
		content    string
		role       string
		sourcePath string
	}
	for blockIndex, block := range normalized.Blocks {
		info, isTable := tableMetaFor(block)
		assetID := blockAsset[blockIndex]
		asset := assetByID[assetID]
		fullStorage := asset.StorageMode == "" || asset.StorageMode == StorageModeFull
		parentID := stableID("parent", version.ID, fmt.Sprint(blockIndex), block.Text)
		rows = append(rows, chunkRow{ID: parentID, Content: block.Text, ContentHash: hashString(block.Text), HeadingPath: block.HeadingPath, PageNumber: block.PageNumber, SourcePath: block.SourcePath, StartOffset: block.StartOffset, EndOffset: block.EndOffset, Ordinal: blockIndex, IndexRole: IndexRoleBoth, TableAssetID: assetID})
		planned := make([]plannedChunk, 0, 2)
		if !isTable {
			for _, content := range chunker.Split(block.Text) {
				planned = append(planned, plannedChunk{content: cleanText(content), role: IndexRoleBoth})
			}
		} else if !fullStorage {
			// Sampled/metadata sheets have one summary representation only. It
			// can help retrieve the sheet, but it is not row evidence.
			if !emittedSummary[assetID] {
				summary := block.Text
				if !strings.Contains(block.SourcePath, "!summary") {
					summary = tableAssetSummary(asset)
				}
				if summary != "" {
					planned = append(planned, plannedChunk{content: cleanTableText(summary), role: IndexRoleBoth, sourcePath: summarySourcePath(asset.Sheet)})
					// The summary vector covers the sample rows, not every parsed
					// row; full-row materialization must not inflate vector coverage.
					coverage.vectorRows += int64(max(len(info.samples), 1))
					coverage.vectorChars += int64(len([]rune(summary)))
					if asset.Sheet != "" {
						coverage.vectorSheets[asset.Sheet] = true
					}
					emittedSummary[assetID] = true
				}
			}
		} else {
			contents := []string{block.Text}
			if splitter, ok := chunker.(TableSplitter); ok {
				contents = splitter.SplitTable(block.Text)
			}
			mode := asset.IndexMode
			if mode == "" {
				mode = indexMode
			}
			switch mode {
			case IndexModeDeep:
				for _, content := range contents {
					planned = append(planned, plannedChunk{content: cleanTableText(content), role: IndexRoleBoth})
				}
				coverage.vectorRows += int64(info.rowCount)
				if asset.Sheet != "" {
					coverage.vectorSheets[asset.Sheet] = true
				}
				for _, content := range contents {
					coverage.vectorChars += int64(len([]rune(content)))
				}
			case IndexModeLexical:
				for _, content := range contents {
					planned = append(planned, plannedChunk{content: cleanTableText(content), role: IndexRoleKeyword})
				}
			default:
				if !emittedSummary[assetID] {
					summary := tableSummaryText(info)
					if summary != "" {
						planned = append(planned, plannedChunk{content: summary, role: IndexRoleBoth, sourcePath: summarySourcePath(asset.Sheet)})
						coverage.vectorRows += int64(len(info.samples))
						if asset.Sheet != "" {
							coverage.vectorSheets[asset.Sheet] = true
						}
						coverage.vectorChars += int64(len([]rune(summary)))
						emittedSummary[assetID] = true
					}
				}
				for _, content := range contents {
					planned = append(planned, plannedChunk{content: cleanTableText(content), role: IndexRoleKeyword})
				}
			}
		}
		for _, item := range planned {
			if item.content == "" {
				continue
			}
			sourcePath := block.SourcePath
			if item.sourcePath != "" {
				sourcePath = item.sourcePath
			}
			rows = append(rows, chunkRow{ID: stableID("chunk", version.ID, fmt.Sprint(childOrdinal), item.content), ParentID: parentID, Content: item.content, ContentHash: hashString(item.content), HeadingPath: block.HeadingPath, PageNumber: block.PageNumber, SourcePath: sourcePath, StartOffset: block.StartOffset, EndOffset: block.EndOffset, Ordinal: childOrdinal, IndexRole: item.role, TableAssetID: assetID})
			childOrdinal++
		}
	}
	coverage.totalSheets = len(normalized.Sheets)
	if coverage.totalSheets == 0 {
		coverage.totalSheets = len(assets)
	}
	coverage.coverageKnown = len(normalized.Sheets) > 0
	coverage.storageMode = normalized.StorageMode
	for _, asset := range assets {
		rowsTotal := asset.TotalRows
		if rowsTotal == 0 {
			rowsTotal = int64(asset.RowCount)
		}
		parsedRows := asset.ParsedRows
		if parsedRows == 0 && (asset.StorageMode == "" || asset.StorageMode == StorageModeFull) {
			parsedRows = int64(asset.RowCount)
		}
		coverage.totalRows += rowsTotal
		coverage.parsedRows += parsedRows
		coverage.keywordRows += parsedRows
		if asset.ParseStatus != ParseStatusSkipped && asset.ParseStatus != ParseStatusFailed {
			coverage.parsedSheets++
		}
		if asset.Truncated {
			coverage.parseTruncated = true
		}
		coverage.storageMode = mergeStorageMode(coverage.storageMode, asset.StorageMode)
	}
	coverage.parseTruncated = coverage.parseTruncated || normalized.Truncated
	coverage.vectorRows = min(coverage.vectorRows, coverage.parsedRows)
	if coverage.storageMode == "" {
		coverage.storageMode = StorageModeFull
	}
	parseStatus := ParseStatusParsed
	if coverage.parseTruncated {
		parseStatus = "partial"
	}
	if coverage.storageMode == StorageModeMetadata && coverage.parsedRows == 0 {
		parseStatus = ParseStatusMetadata
	}
	if len(rows) == 0 {
		return apperror.Terminalf("empty_document", "chunker returned no chunks")
	}
	childChunks := 0
	for _, row := range rows {
		if row.ParentID != "" && row.IndexRole != IndexRoleKeyword {
			childChunks++
		}
	}
	if w.embeddingMaxChunks > 0 && childChunks > w.embeddingMaxChunks {
		return apperror.Terminalf("embedding_budget_exceeded",
			"document produced %d chunks, above the %d chunk limit; split the table or raise EMBEDDING_MAX_CHUNKS_PER_DOCUMENT", childChunks, w.embeddingMaxChunks)
	}
	if w.embeddingWarning > 0 && childChunks > w.embeddingWarning {
		slog.Warn("embedding_budget_high", "document_version_id", version.ID, "chunks", childChunks, "warning_threshold", w.embeddingWarning)
		if w.metrics != nil {
			w.metrics.Inc("ingestion_embedding_budget_high_total", nil)
		}
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
		if _, writable, err := lockVersionForWrite(tx, version.TenantID, version.ID); err != nil {
			return err
		} else if !writable {
			return apperror.ErrVersionSuperseded
		}
		if err := tx.Where("document_version_id = ?", version.ID).Delete(&models.Chunk{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_version_id = ?", version.ID).Delete(&models.TableAsset{}).Error; err != nil {
			return err
		}
		if len(assets) > 0 {
			if err := tx.Create(&assets).Error; err != nil {
				return fmt.Errorf("insert table assets: %w", err)
			}
		}
		if err := insertChunks(tx, version, rows, time.Now()); err != nil {
			return err
		}
		if err := tx.Model(&models.Document{}).Where("id = ?", version.DocumentID).
			Update("index_mode", indexMode).Error; err != nil {
			return err
		}
		result := tx.Model(&models.DocumentVersion{}).
			Where("id = ? AND status <> ?", version.ID, StatusCancelled).
			Updates(statusFields(StatusChunked, map[string]any{
				"index_mode": indexMode, "storage_mode": coverage.storageMode,
				"parse_status": parseStatus, "parse_truncated": coverage.parseTruncated,
				"parsed_sheets": coverage.parsedSheets, "total_sheets": coverage.totalSheets,
				"parsed_rows": coverage.parsedRows, "keyword_rows": coverage.keywordRows,
				"total_rows": coverage.totalRows, "vector_rows": coverage.vectorRows,
				"vector_sheets": len(coverage.vectorSheets),
				"vector_tokens": estimateTokens(int(coverage.vectorChars)), "estimated_tokens": estimateTokens(int(coverage.vectorChars)),
				"coverage_known": coverage.coverageKnown,
			}))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperror.ErrVersionSuperseded
		}
		return nil
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
	if version.Status == StatusCancelled {
		return apperror.ErrVersionSuperseded
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
	if err := w.db.WithContext(ctx).Where("document_version_id = ? AND parent_id IS NOT NULL AND status = ? AND index_role <> ?", version.ID, StatusChunked, IndexRoleKeyword).Order("ordinal").Find(&chunks).Error; err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	markKeywordOnly := func() error {
		return tenantdb.MustScoped(w.db.WithContext(ctx), version.TenantID).Model(&models.Chunk{}).
			Where("document_version_id = ? AND parent_id IS NOT NULL AND index_role = ? AND status = ?", version.ID, IndexRoleKeyword, StatusChunked).
			Update("status", StatusEmbedded).Error
	}
	if len(chunks) == 0 {
		var alreadyEmbedded int64
		if err := tenantdb.MustScoped(w.db.WithContext(ctx), version.TenantID).Model(&models.Chunk{}).
			Where("document_version_id = ? AND parent_id IS NOT NULL AND status = ? AND index_role <> ?", version.ID, StatusEmbedded, IndexRoleKeyword).
			Count(&alreadyEmbedded).Error; err != nil {
			return fmt.Errorf("count embedded chunks: %w", err)
		}
		if alreadyEmbedded > 0 {
			// Resume after a partial attempt: every vector chunk is already done.
			if err := markKeywordOnly(); err != nil {
				return fmt.Errorf("mark keyword chunks: %w", err)
			}
			return nil
		}
		// A lexical table produces keyword-only chunks; indexing still runs, but
		// no provider call is made. A document with no child chunks at all is
		// still a terminal failure.
		var keywordOnly int64
		if err := tenantdb.MustScoped(w.db.WithContext(ctx), version.TenantID).Model(&models.Chunk{}).
			Where("document_version_id = ? AND parent_id IS NOT NULL AND index_role = ?", version.ID, IndexRoleKeyword).
			Count(&keywordOnly).Error; err != nil {
			return fmt.Errorf("count keyword chunks: %w", err)
		}
		if keywordOnly == 0 {
			return apperror.Terminalf("empty_document", "no child chunks to embed")
		}
		if err := markKeywordOnly(); err != nil {
			return fmt.Errorf("mark keyword chunks: %w", err)
		}
		return nil
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
		if errors.Is(err, apperror.ErrVersionSuperseded) {
			return apperror.ErrVersionSuperseded
		}
		return fmt.Errorf("embedding_error: %w", err)
	}
	if err := markKeywordOnly(); err != nil {
		return fmt.Errorf("mark keyword chunks: %w", err)
	}
	// The adapter fences its own write, but a replacement may have committed
	// just after it; never leave a superseded version looking embedded.
	var status string
	if err := w.db.WithContext(ctx).Table("document_versions").Select("status").Where("id = ?", version.ID).Scan(&status).Error; err != nil {
		return err
	}
	if status == StatusCancelled {
		return apperror.ErrVersionSuperseded
	}
	return nil
}

func (w *PipelineWorker) index(ctx context.Context, task Task) error {
	var version models.DocumentVersion
	if err := w.db.WithContext(ctx).First(&version, "id = ?", task.DocumentVersionID).Error; err != nil {
		return fmt.Errorf("load version: %w", err)
	}
	if version.Status == StatusCancelled {
		return apperror.ErrVersionSuperseded
	}
	if w.indexWriter == nil {
		return fmt.Errorf("evidence index writer is not configured")
	}
	var knowledgeBase struct{ ACLVersion int64 }
	packageID := task.PackageID
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, writable, err := lockVersionForWrite(tx, version.TenantID, version.ID)
		if err != nil {
			return err
		}
		if !writable {
			return apperror.ErrVersionSuperseded
		}
		version = locked
		var chunks []models.Chunk
		// Indexing is also replayed after a restart, so chunks that a previous
		// attempt already marked ready still belong in the package.
		if err := tx.Where("document_version_id = ? AND parent_id IS NOT NULL AND status IN ?", version.ID, []string{StatusEmbedded, StatusEvidenceReady}).Order("ordinal").Find(&chunks).Error; err != nil {
			return fmt.Errorf("load embedded chunks: %w", err)
		}
		if len(chunks) == 0 {
			return apperror.Terminalf("empty_document", "no embedded chunks to index")
		}
		if err := w.indexWriter.IndexVersion(ctx, version.ID); err != nil {
			return fmt.Errorf("keyword_index_error: %w", err)
		}
		if err := tx.Table("knowledge_bases").Select("acl_version").Where("id = ?", version.KnowledgeBaseID).Scan(&knowledgeBase).Error; err != nil {
			return fmt.Errorf("load knowledge base acl: %w", err)
		}
		var allChunks []models.Chunk
		if err := tx.Where("document_version_id = ? AND status IN ?", version.ID, []string{StatusParent, StatusEmbedded, StatusEvidenceReady}).Order("ordinal, id").Find(&allChunks).Error; err != nil {
			return fmt.Errorf("load package chunks: %w", err)
		}
		packageChunks := projectChunks(allChunks)
		if packageID == "" {
			packageID = stableID("pkg", version.ID)
		}
		packageValue := EvidencePackage{ID: packageID, SchemaVersion: SchemaEvidencePackage, Envelope: Envelope{SchemaVersion: SchemaEvidencePackage, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: task.PipelineRunID, StageRunID: task.StageRunID, TraceID: task.TraceID, RequestID: task.RequestID, ActorID: task.ActorID, IdempotencyKey: task.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC()}, ContentHash: version.ContentHash, ACLVersion: knowledgeBase.ACLVersion, Status: StatusEvidenceReady, Chunks: packageChunks}
		data, err := json.Marshal(packageValue)
		if err != nil {
			return fmt.Errorf("marshal evidence package: %w", err)
		}
		now := time.Now()
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
		result := tx.Model(&models.DocumentVersion{}).
			Where("id = ? AND status <> ?", version.ID, StatusCancelled).
			Updates(statusFields(StatusEvidenceReady, nil))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperror.ErrVersionSuperseded
		}
		return tx.Model(&models.Document{}).Where("id = ? AND latest_version_id = ?", version.DocumentID, version.ID).Update("status", StatusEvidenceReady).Error
	})
	if err != nil {
		return err
	}
	w.triggerProjection(ctx, version.TenantID, version.KnowledgeBaseID, packageID, knowledgeBase.ACLVersion)
	return nil
}

// lockVersionForWrite locks the version row (PostgreSQL) and reports whether
// the stage may still write. A missing row means purge or replacement already
// removed it; a CANCELLED row means a newer version superseded this one.
func lockVersionForWrite(tx *gorm.DB, tenantID, versionID string) (models.DocumentVersion, bool, error) {
	var version models.DocumentVersion
	query := tx
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Where("id = ? AND tenant_id = ?", versionID, tenantID).First(&version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return version, false, nil
	}
	if err != nil {
		return version, false, err
	}
	return version, version.Status != StatusCancelled, nil
}

// cancelSupersededStage closes the pipeline of a version another writer already
// cancelled, so reconcile does not requeue its stages forever.
func (w *PipelineWorker) cancelSupersededStage(ctx context.Context, task Task) error {
	now := time.Now()
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.StageRun{}).
			Where("idempotency_key = ? AND status = ?", task.IdempotencyKey, StageProcessing).
			Updates(map[string]any{
				"status": StageCancelled, "error_code": "superseded",
				"error_message": "superseded by a newer document version", "updated_at": now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&models.PipelineRun{}).
			Where("id = ? AND tenant_id = ? AND status NOT IN ?", task.PipelineRunID, task.TenantID, []string{StatusEvidenceReady, StatusCancelled, StatusFailed}).
			Updates(map[string]any{
				"status": StatusCancelled, "error_code": "superseded",
				"error_message": "superseded by a newer document version", "updated_at": now,
			}).Error
	})
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
	if err := w.projection.NoteEvidenceChange(triggerCtx, tenantID, knowledgeBaseID, packageID); err != nil {
		slog.Warn("projection_change_note_failed", "knowledge_base_id", knowledgeBaseID, "package_id", packageID, "error", err)
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
	case "unsupported_scanned_pdf", "unsupported_legacy_office", "unsupported_corrupt_document", "document_too_complex", "file_too_large":
		return true
	default:
		return false
	}
}
