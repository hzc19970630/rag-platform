package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type stubReader struct {
	result ParseResult
	err    error
}

func (r *stubReader) Parse(context.Context, SourceObject, []byte) (ParseResult, error) {
	return r.result, r.err
}

type recordingIndexWriter struct {
	db              *gorm.DB
	deletedVersions []string
	indexedChunks   []string
	indexedVersions []string
	embeddingModel  string
	embeddings      int
	deleteErr       error
	indexChunksErr  error
	writeErr        error
	indexVersionErr error
}

func (w *recordingIndexWriter) IndexChunks(_ context.Context, versionID string, chunks []ChunkDocument) error {
	w.indexedChunks = append(w.indexedChunks, versionID)
	if len(chunks) == 0 {
		return errors.New("no chunks")
	}
	return w.indexChunksErr
}

func (w *recordingIndexWriter) DeleteVersion(_ context.Context, versionID string) error {
	w.deletedVersions = append(w.deletedVersions, versionID)
	return w.deleteErr
}

func (w *recordingIndexWriter) IndexVersion(_ context.Context, versionID string) error {
	w.indexedVersions = append(w.indexedVersions, versionID)
	return w.indexVersionErr
}

func (w *recordingIndexWriter) WriteEmbeddings(_ context.Context, versionID, model string, records []EmbeddingRecord) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	w.embeddingModel = model
	w.embeddings += len(records)
	for _, record := range records {
		if err := w.db.Model(&models.Chunk{}).Where("id = ?", record.ChunkID).
			Updates(map[string]any{"status": StatusEmbedded, "embedding_dimension": len(record.Vector)}).Error; err != nil {
			return err
		}
	}
	return nil
}

type recordingProjection struct {
	invalidated []string
	noted       []string
	err         error
}

func (p *recordingProjection) InvalidateProjectionState(_ context.Context, _, knowledgeBaseID string) error {
	p.invalidated = append(p.invalidated, knowledgeBaseID)
	return p.err
}

func (p *recordingProjection) TriggerWiki(_ context.Context, _, knowledgeBaseID, packageID string, _ int64) error {
	return p.err
}

func (p *recordingProjection) TriggerRelation(_ context.Context, _, knowledgeBaseID, packageID string, _ int64) error {
	return p.err
}

func (p *recordingProjection) NoteEvidenceChange(_ context.Context, _, _, packageID string) error {
	p.noted = append(p.noted, packageID)
	return p.err
}

type failingChunkingResolver struct{ err error }

func (r failingChunkingResolver) ChunkerFor(context.Context, string, string) (Chunker, error) {
	return nil, r.err
}

type staticEmbeddingResolver struct {
	binding  embedding.Binding
	embedder Embedder
	err      error
}

func (r staticEmbeddingResolver) ResolveEmbedding(context.Context, string, string) (embedding.Binding, Embedder, error) {
	return r.binding, r.embedder, r.err
}

type shortEmbedder struct{ dimension int }

func (e shortEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return [][]float32{make([]float32, e.dimension)}, nil
}
func (e shortEmbedder) Dimension() int { return e.dimension }
func (e shortEmbedder) Name() string   { return "short-embed" }

func newWorkerFixture(t *testing.T) (*PipelineWorker, *gorm.DB, *memoryObjects, *recordingIndexWriter, *stubReader) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&models.KnowledgeBase{}, &models.Chunk{}, &models.EmbeddingMigration{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate worker tables: %v", err)
	}
	now := time.Now()
	if err := db.Create(&models.KnowledgeBase{ID: "kb-a", TenantID: "tenant-a", OwnerID: "user-a", Name: "KB", ACLVersion: 3, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.Document{
		ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "doc.txt", MimeType: "text/plain", Status: StatusParsing, LatestVersionID: "ver-1",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-1", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 1, ContentHash: "hash-1", SizeBytes: 11, SourceObjectKey: "source-key",
		Status: StatusParsing, ACLVersion: 3, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&models.PipelineRun{
		ID: "run-1", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-1", RequestID: "req-1", TraceID: "trace-1",
		IdempotencyKey: "doc-a:ver-1:parse", Status: StatusParsing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "run-1", Stage: StageParse, Status: StagePending, IdempotencyKey: "run-1:parse",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed parse stage: %v", err)
	}
	for _, stage := range []string{StageNormalize, StageChunk, StageEmbed, StageIndex} {
		if err := db.Create(&models.StageRun{
			PipelineRunID: "run-1", Stage: stage, Status: StagePending, IdempotencyKey: "run-1:" + stage,
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed %s stage: %v", stage, err)
		}
	}
	objects := &memoryObjects{data: map[string][]byte{"source-key": []byte("source text")}}
	reader := &stubReader{result: ParseResult{
		Title: "Doc", Language: "en", ParserVersion: "v1",
		Blocks: []DocumentBlock{
			{Text: "Hello   world.", HeadingPath: "H1", PageNumber: 1},
			{Text: "Second block.", HeadingPath: "H2", PageNumber: 2},
		},
	}}
	writer := &recordingIndexWriter{db: db}
	worker := NewPipelineWorker(db, objects, reader, rebuildEmbedder{dimension: 8, model: "test-embed"}, writer, nil, 0)
	return worker, db, objects, writer, reader
}

func workerTaskForStage(t *testing.T, db *gorm.DB, stage string) Task {
	t.Helper()
	key := "run-1:" + stage
	var stageRow models.StageRun
	if err := db.First(&stageRow, "idempotency_key = ?", key).Error; err != nil {
		t.Fatalf("load %s stage: %v", stage, err)
	}
	var inboxes int64
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Count(&inboxes).Error; err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if inboxes == 0 {
		now := time.Now()
		if err := db.Create(&models.TaskInbox{
			IdempotencyKey: key, TaskType: TypeTask, Status: inboxProcessing, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed inbox: %v", err)
		}
	}
	return Task{
		Envelope: Envelope{
			SchemaVersion: SchemaTask, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentID: "doc-a", DocumentVersionID: "ver-1", PipelineRunID: "run-1",
			StageRunID: fmt.Sprint(stageRow.ID), IdempotencyKey: key,
		},
		TaskType: TypeTask, Stage: stage,
	}
}

func runStage(t *testing.T, worker *PipelineWorker, db *gorm.DB, stage string) error {
	t.Helper()
	if err := db.Model(&models.PipelineRun{}).Where("id = ?", "run-1").Update("status", StatusForStage(stage)).Error; err != nil {
		t.Fatalf("reset pipeline status: %v", err)
	}
	task := workerTaskForStage(t, db, stage)
	if err := worker.Process(context.Background(), task); err != nil {
		return err
	}
	var stageRow models.StageRun
	if err := db.First(&stageRow, "idempotency_key = ?", "run-1:"+stage).Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stageRow.Status != StageSucceeded {
		t.Fatalf("%s stage status = %s, want succeeded", stage, stageRow.Status)
	}
	return nil
}

func expectTerminalStageFailure(t *testing.T, worker *PipelineWorker, db *gorm.DB, stage, wantCode string) {
	t.Helper()
	if err := db.Model(&models.PipelineRun{}).Where("id = ?", "run-1").Update("status", StatusForStage(stage)).Error; err != nil {
		t.Fatalf("reset pipeline status: %v", err)
	}
	task := workerTaskForStage(t, db, stage)
	if err := worker.Process(context.Background(), task); err != nil {
		t.Fatalf("terminal failure must settle without an error, got %v", err)
	}
	var stageRow models.StageRun
	if err := db.First(&stageRow, "idempotency_key = ?", "run-1:"+stage).Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stageRow.Status != StageFailed || stageRow.ErrorCode != wantCode {
		t.Fatalf("stage = %+v, want failed with %q", stageRow, wantCode)
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", "run-1:"+stage).Error; err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	if inbox.Status != inboxFailed {
		t.Fatalf("inbox status = %s, want failed", inbox.Status)
	}
}

func TestWorkerRunsEveryStageEndToEnd(t *testing.T) {
	worker, db, objects, writer, _ := newWorkerFixture(t)
	projection := &recordingProjection{}
	worker.SetProjectionTrigger(projection)

	if err := runStage(t, worker, db, StageParse); err != nil {
		t.Fatalf("parse stage: %v", err)
	}
	var version models.DocumentVersion
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.Status != StatusParsed || version.NormalizedKey == "" {
		t.Fatalf("version after parse = %+v", version)
	}
	if _, ok := objects.data[version.NormalizedKey]; !ok {
		t.Fatalf("normalized object %q was not stored", version.NormalizedKey)
	}

	if err := runStage(t, worker, db, StageNormalize); err != nil {
		t.Fatalf("normalize stage: %v", err)
	}
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if version.Status != StatusNormalized {
		t.Fatalf("version after normalize = %+v", version)
	}

	if err := runStage(t, worker, db, StageChunk); err != nil {
		t.Fatalf("chunk stage: %v", err)
	}
	if len(writer.deletedVersions) != 1 || len(writer.indexedChunks) != 1 {
		t.Fatalf("chunk index calls = deleted=%v indexed=%v", writer.deletedVersions, writer.indexedChunks)
	}
	var children int64
	if err := db.Model(&models.Chunk{}).Where("document_version_id = ? AND parent_id IS NOT NULL", "ver-1").Count(&children).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if children == 0 {
		t.Fatal("chunk stage wrote no child chunks")
	}

	if err := runStage(t, worker, db, StageEmbed); err != nil {
		t.Fatalf("embed stage: %v", err)
	}
	if writer.embeddingModel != "test-embed" || writer.embeddings == 0 {
		t.Fatalf("embedding writes = %q/%d", writer.embeddingModel, writer.embeddings)
	}

	if err := runStage(t, worker, db, StageIndex); err != nil {
		t.Fatalf("index stage: %v", err)
	}
	if len(writer.indexedVersions) != 1 || writer.indexedVersions[0] != "ver-1" {
		t.Fatalf("indexed versions = %v", writer.indexedVersions)
	}
	var packageCount int64
	if err := db.Model(&models.EvidencePackage{}).Where("document_version_id = ?", "ver-1").Count(&packageCount).Error; err != nil {
		t.Fatalf("count packages: %v", err)
	}
	if packageCount != 1 {
		t.Fatalf("evidence packages = %d, want 1", packageCount)
	}
	var pipelineRun models.PipelineRun
	if err := db.First(&pipelineRun, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipelineRun.Status != StatusEvidenceReady {
		t.Fatalf("pipeline status = %s, want %s", pipelineRun.Status, StatusEvidenceReady)
	}
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if version.Status != StatusEvidenceReady {
		t.Fatalf("version status = %s, want %s", version.Status, StatusEvidenceReady)
	}
	if len(projection.invalidated) != 1 || len(projection.noted) != 1 {
		t.Fatalf("projection calls = %+v", projection)
	}
}

func TestChunkStageRecordsPerSheetCoverage(t *testing.T) {
	worker, db, objects, _, _ := newWorkerFixture(t)
	normalized := NormalizedDocument{
		SchemaVersion: SchemaNormalizedDocument,
		Sheets: []SheetMeta{
			{Name: "small", Header: []string{"name", "qty"}, TotalRows: 2, ParsedRows: 2, ColumnCount: 2, ParseStatus: ParseStatusParsed},
			{Name: "huge", Header: []string{"name", "qty"}, DeclaredRows: 50000, TotalRows: 50000, ParsedRows: 50, ColumnCount: 2, ParseStatus: ParseStatusSampled, Truncated: true, SampleRows: []string{"pen | 2"}},
			{Name: "catalog", Header: []string{"id", "v"}, DeclaredRows: 6000000, TotalRows: 6000000, ColumnCount: 2, ParseStatus: ParseStatusMetadata, Truncated: true},
		},
		Blocks: []DocumentBlock{
			{Kind: "table", SourcePath: "sheet:small!A2:B3", Text: "name | qty\npen | 2\ntape | 5",
				Table: &TableMeta{Sheet: "small", Header: []string{"name", "qty"}, RowStart: 2, RowEnd: 3, ColumnCount: 2}},
			{Kind: "table", SourcePath: "sheet:huge!A2:B51", Text: "name | qty\npen | 2",
				Table: &TableMeta{Sheet: "huge", Header: []string{"name", "qty"}, RowStart: 2, RowEnd: 51, ColumnCount: 2}},
			{Kind: "table", SourcePath: "sheet:huge!A52:B52", Text: "name | qty\nbox | 3",
				Table: &TableMeta{Sheet: "huge", Header: []string{"name", "qty"}, RowStart: 52, RowEnd: 52, ColumnCount: 2}},
			{Kind: "table", SourcePath: "sheet:catalog!A1:B1", Text: "id | v",
				Table: &TableMeta{Sheet: "catalog", Header: []string{"id", "v"}, RowStart: 2, RowEnd: 1, ColumnCount: 2}},
		},
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal normalized: %v", err)
	}
	objects.data["normalized-key"] = data
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").
		Updates(map[string]any{"normalized_key": "normalized-key", "status": StatusNormalized}).Error; err != nil {
		t.Fatalf("set normalized key: %v", err)
	}
	if err := runStage(t, worker, db, StageChunk); err != nil {
		t.Fatalf("chunk stage: %v", err)
	}

	var assets []models.TableAsset
	if err := db.Where("document_version_id = ?", "ver-1").Order("sheet").Find(&assets).Error; err != nil {
		t.Fatalf("load assets: %v", err)
	}
	if len(assets) != 3 {
		t.Fatalf("assets = %d, want 3", len(assets))
	}
	bySheet := map[string]models.TableAsset{}
	for _, asset := range assets {
		bySheet[asset.Sheet] = asset
	}
	small := bySheet["small"]
	if small.StorageMode != StorageModeFull || small.RowCount != 2 || small.ParsedRows != 2 || small.IndexMode != IndexModeDeep {
		t.Fatalf("small asset = %+v", small)
	}
	huge := bySheet["huge"]
	if huge.StorageMode != StorageModeSampled || huge.RowCount != 50 || huge.ParsedRows != 50 || huge.TotalRows != 50000 || huge.IndexMode != IndexModeStandard || !huge.Truncated {
		t.Fatalf("huge asset = %+v", huge)
	}
	catalog := bySheet["catalog"]
	if catalog.StorageMode != StorageModeMetadata || catalog.ParsedRows != 0 || catalog.TotalRows != 6000000 || catalog.IndexMode != IndexModeStandard {
		t.Fatalf("catalog asset = %+v", catalog)
	}

	var tableRows int64
	if err := db.Model(&models.TableRow{}).Where("document_version_id = ?", "ver-1").Count(&tableRows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if tableRows != 2 {
		t.Fatalf("persisted table rows = %d, want only the full sheet's 2 rows", tableRows)
	}

	var summaries int64
	if err := db.Model(&models.Chunk{}).
		Where("document_version_id = ? AND source_path = ?", "ver-1", "sheet:huge!summary").Count(&summaries).Error; err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if summaries != 1 {
		t.Fatalf("huge summary chunks = %d, want 1 for repeated row blocks", summaries)
	}

	var version models.DocumentVersion
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.StorageMode != StorageModeMetadata || version.ParseStatus != "partial" || !version.ParseTruncated || !version.CoverageKnown {
		t.Fatalf("version coverage = %+v", version)
	}
	if version.ParsedSheets != 3 || version.TotalSheets != 3 || version.ParsedRows != 52 || version.TotalRows != 6050002 {
		t.Fatalf("version rows = parsed %d/%d sheets %d/%d total %d", version.ParsedRows, version.TotalRows, version.ParsedSheets, version.TotalSheets, version.TotalRows)
	}
	if version.KeywordRows != 52 || version.VectorRows != 52 || version.VectorSheets != 3 {
		t.Fatalf("version index coverage = %+v", version)
	}

	var document models.Document
	if err := db.First(&document, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if document.IndexMode != IndexModeStandard {
		t.Fatalf("document index mode = %q, want the aggregate of deep+standard sheets", document.IndexMode)
	}
}

type fixedChunker struct{ parts []string }

func (c fixedChunker) Split(string) []string { return c.parts }

func TestWorkerUsesResolvers(t *testing.T) {
	t.Run("chunking resolver", func(t *testing.T) {
		worker, db, objects, _, _ := newWorkerFixture(t)
		seedNormalizedObject(t, db, objects)
		worker.SetChunkingResolver(chunkingResolverFunc(func(context.Context, string) (Chunker, error) {
			return fixedChunker{parts: []string{"alpha", "beta"}}, nil
		}))
		if err := runStage(t, worker, db, StageChunk); err != nil {
			t.Fatalf("chunk stage: %v", err)
		}
		var children int64
		if err := db.Model(&models.Chunk{}).Where("document_version_id = ? AND parent_id IS NOT NULL", "ver-1").Count(&children).Error; err != nil {
			t.Fatalf("count children: %v", err)
		}
		if children != 2 {
			t.Fatalf("chunks = %d, want 2 from the resolved policy", children)
		}
	})

	t.Run("embedding resolver", func(t *testing.T) {
		worker, db, _, writer, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked)
		worker.SetEmbeddingResolver(staticEmbeddingResolver{
			binding:  embedding.Binding{Model: "resolved", Dimension: 8},
			embedder: rebuildEmbedder{dimension: 8, model: "resolved"},
		})
		if err := runStage(t, worker, db, StageEmbed); err != nil {
			t.Fatalf("embed stage: %v", err)
		}
		if writer.embeddingModel != "resolved" {
			t.Fatalf("embedding model = %q, want resolved", writer.embeddingModel)
		}
	})
}

type chunkingResolverFunc func(context.Context, string) (Chunker, error)

func (f chunkingResolverFunc) ChunkerFor(ctx context.Context, tenantID, knowledgeBaseID string) (Chunker, error) {
	return f(ctx, tenantID)
}

func TestWorkerProjectionFailuresDoNotFailIndexing(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	worker.SetProjectionTrigger(&recordingProjection{err: errors.New("projection down")})
	if err := runStage(t, worker, db, StageParse); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := runStage(t, worker, db, StageNormalize); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := runStage(t, worker, db, StageChunk); err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if err := runStage(t, worker, db, StageEmbed); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := runStage(t, worker, db, StageIndex); err != nil {
		t.Fatalf("index must ignore projection failures: %v", err)
	}
}

func TestWorkerRejectsInvalidEnvelope(t *testing.T) {
	worker := &PipelineWorker{}
	err := worker.Process(context.Background(), Task{})
	if err == nil || !strings.Contains(err.Error(), "invalid ingestion task envelope") {
		t.Fatalf("Process = %v", err)
	}
}

func TestWorkerParseFailurePaths(t *testing.T) {
	t.Run("parser error is retryable", func(t *testing.T) {
		worker, db, _, _, reader := newWorkerFixture(t)
		reader.err = errors.New("bad bytes")
		err := worker.Process(context.Background(), workerTaskForStage(t, db, StageParse))
		if err == nil || !strings.Contains(err.Error(), "parser_error") {
			t.Fatalf("Process = %v", err)
		}
		var stage models.StageRun
		if err := db.First(&stage, "idempotency_key = ?", "run-1:parse").Error; err != nil {
			t.Fatalf("load stage: %v", err)
		}
		if stage.Status != StageFailed || stage.ErrorCode != "parser_error" {
			t.Fatalf("stage = %+v", stage)
		}
	})

	t.Run("parser reads a missing object", func(t *testing.T) {
		worker, db, objects, _, _ := newWorkerFixture(t)
		delete(objects.data, "source-key")
		err := worker.Process(context.Background(), workerTaskForStage(t, db, StageParse))
		if err == nil || !strings.Contains(err.Error(), "load source object") {
			t.Fatalf("parse = %v", err)
		}
	})

	t.Run("terminal parser code", func(t *testing.T) {
		worker, db, _, _, reader := newWorkerFixture(t)
		reader.result = ParseResult{ErrorCode: "unsupported_scanned_pdf", ErrorMessage: "scanned"}
		expectTerminalStageFailure(t, worker, db, StageParse, "unsupported_scanned_pdf")
	})

	t.Run("empty document", func(t *testing.T) {
		worker, db, _, _, reader := newWorkerFixture(t)
		reader.result = ParseResult{Blocks: nil}
		expectTerminalStageFailure(t, worker, db, StageParse, "empty_document")
	})
}

func TestWorkerNormalizeEmptyDocumentIsTerminal(t *testing.T) {
	worker, db, objects, _, _ := newWorkerFixture(t)
	normalized := NormalizedDocument{SchemaVersion: SchemaNormalizedDocument, Blocks: []DocumentBlock{{Text: "   ", HeadingPath: " "}}}
	data, _ := json.Marshal(normalized)
	objects.data["normalized-key"] = data
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("normalized_key", "normalized-key").Error; err != nil {
		t.Fatalf("set normalized key: %v", err)
	}
	expectTerminalStageFailure(t, worker, db, StageNormalize, "empty_document")
}

func TestWorkerNormalizeMissingObject(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("normalized_key", "missing-key").Error; err != nil {
		t.Fatalf("set normalized key: %v", err)
	}
	err := runStage(t, worker, db, StageNormalize)
	if err == nil || !strings.Contains(err.Error(), "load normalized document") {
		t.Fatalf("normalize = %v", err)
	}
}

func TestWorkerChunkMissingNormalizedObject(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("normalized_key", "missing-key").Error; err != nil {
		t.Fatalf("set normalized key: %v", err)
	}
	err := runStage(t, worker, db, StageChunk)
	if err == nil || !strings.Contains(err.Error(), "load normalized document") {
		t.Fatalf("chunk = %v", err)
	}
}

func TestWorkerEmbedMissingWriter(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	seedEmbeddableChunks(t, db, StatusChunked)
	worker.indexWriter = nil
	err := runStage(t, worker, db, StageEmbed)
	if err == nil || !strings.Contains(err.Error(), "evidence index writer is not configured") {
		t.Fatalf("embed = %v", err)
	}
}

func TestWorkerChunkFailurePaths(t *testing.T) {
	t.Run("chunking resolver error", func(t *testing.T) {
		worker, db, objects, _, _ := newWorkerFixture(t)
		seedNormalizedObject(t, db, objects)
		worker.SetChunkingResolver(failingChunkingResolver{err: errors.New("policy down")})
		err := runStage(t, worker, db, StageChunk)
		if err == nil || !strings.Contains(err.Error(), "resolve chunking policy") {
			t.Fatalf("chunk = %v", err)
		}
	})

	t.Run("missing index writer", func(t *testing.T) {
		worker, db, objects, _, _ := newWorkerFixture(t)
		seedNormalizedObject(t, db, objects)
		worker.indexWriter = nil
		err := runStage(t, worker, db, StageChunk)
		if err == nil || !strings.Contains(err.Error(), "keyword_index_error") {
			t.Fatalf("chunk = %v", err)
		}
	})

	t.Run("index chunks failure", func(t *testing.T) {
		worker, db, objects, writer, _ := newWorkerFixture(t)
		seedNormalizedObject(t, db, objects)
		writer.indexChunksErr = errors.New("keyword down")
		err := runStage(t, worker, db, StageChunk)
		if err == nil || !strings.Contains(err.Error(), "keyword_index_error") {
			t.Fatalf("chunk = %v", err)
		}
	})

	t.Run("delete version failure", func(t *testing.T) {
		worker, db, objects, writer, _ := newWorkerFixture(t)
		seedNormalizedObject(t, db, objects)
		writer.deleteErr = errors.New("delete down")
		err := runStage(t, worker, db, StageChunk)
		if err == nil || !strings.Contains(err.Error(), "delete previous index") {
			t.Fatalf("chunk = %v", err)
		}
	})
}

func seedNormalizedObject(t *testing.T, db *gorm.DB, objects *memoryObjects) {
	t.Helper()
	normalized := NormalizedDocument{
		SchemaVersion: SchemaNormalizedDocument,
		Blocks:        []DocumentBlock{{Text: "Hello world.", HeadingPath: "H1", PageNumber: 1}},
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal normalized document: %v", err)
	}
	objects.data["normalized-key"] = data
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").
		Updates(map[string]any{"normalized_key": "normalized-key", "status": StatusNormalized}).Error; err != nil {
		t.Fatalf("set normalized key: %v", err)
	}
}

func seedEmbeddableChunks(t *testing.T, db *gorm.DB, statuses ...string) {
	t.Helper()
	now := time.Now()
	for index, chunkStatus := range statuses {
		chunk := models.Chunk{
			ID: fmt.Sprintf("chunk-%d", index), TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentVersionID: "ver-1", ParentID: "parent-1", Ordinal: index, Content: "text",
			ContentHash: "hash", Status: chunkStatus, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&chunk).Error; err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
}

func TestWorkerEmbedFailurePaths(t *testing.T) {
	t.Run("active migration blocks ingestion", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked)
		if err := db.Create(&models.EmbeddingMigration{
			ID: "emig-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Status: "reembedding",
			SourceModel: "old", SourceDimension: 4, TargetModel: "new", TargetDimension: 8,
		}).Error; err != nil {
			t.Fatalf("seed migration: %v", err)
		}
		expectTerminalStageFailure(t, worker, db, StageEmbed, "embedding_migration_in_progress")
	})

	t.Run("missing adapter", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked)
		worker.embedder = nil
		expectTerminalStageFailure(t, worker, db, StageEmbed, "embedding_binding_unavailable")
	})

	t.Run("no child chunks", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		expectTerminalStageFailure(t, worker, db, StageEmbed, "empty_document")
	})

	t.Run("vector count mismatch", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked, StatusChunked)
		worker.embedder = shortEmbedder{dimension: 8}
		err := runStage(t, worker, db, StageEmbed)
		if err == nil || !strings.Contains(err.Error(), "vector count mismatch") {
			t.Fatalf("embed = %v", err)
		}
	})

	t.Run("vector dimension mismatch", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked)
		worker.SetEmbeddingResolver(staticEmbeddingResolver{
			binding:  embedding.Binding{Model: "resolved", Dimension: 8},
			embedder: shortEmbedder{dimension: 4},
		})
		err := runStage(t, worker, db, StageEmbed)
		if err == nil || !strings.Contains(err.Error(), "vector dimension mismatch") {
			t.Fatalf("embed = %v", err)
		}
	})

	t.Run("writer failure", func(t *testing.T) {
		worker, db, _, writer, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusChunked)
		writer.writeErr = errors.New("write down")
		err := runStage(t, worker, db, StageEmbed)
		if err == nil || !strings.Contains(err.Error(), "embedding_error") {
			t.Fatalf("embed = %v", err)
		}
	})
}

func TestWorkerIndexFailurePaths(t *testing.T) {
	t.Run("no embedded chunks", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		expectTerminalStageFailure(t, worker, db, StageIndex, "empty_document")
	})

	t.Run("missing writer", func(t *testing.T) {
		worker, db, _, _, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusEmbedded)
		worker.indexWriter = nil
		err := runStage(t, worker, db, StageIndex)
		if err == nil || !strings.Contains(err.Error(), "evidence index writer is not configured") {
			t.Fatalf("index = %v", err)
		}
	})

	t.Run("index version failure", func(t *testing.T) {
		worker, db, _, writer, _ := newWorkerFixture(t)
		seedEmbeddableChunks(t, db, StatusEmbedded)
		writer.indexVersionErr = errors.New("index down")
		err := runStage(t, worker, db, StageIndex)
		if err == nil || !strings.Contains(err.Error(), "keyword_index_error") {
			t.Fatalf("index = %v", err)
		}
	})
}
