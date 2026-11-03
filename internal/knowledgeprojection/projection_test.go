package knowledgeprojection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/llm"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeChatter struct{ name string }

func (f fakeChatter) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	if len(request.Evidence) == 0 {
		return llm.ChatResponse{}, nil
	}
	return llm.ChatResponse{Text: "generated from " + request.Evidence[0].ChunkID}, nil
}

func (f fakeChatter) Name() string { return f.name }

func testGenerator(t *testing.T, name string) *Generator {
	t.Helper()
	registry, err := prompts.Load(filepath.Join("..", "..", "config", "prompt_templates"))
	if err != nil {
		t.Fatalf("load prompt templates: %v", err)
	}
	generator, err := NewGenerator(fakeChatter{name: name}, registry)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	return generator
}

func TestComputeJoin(t *testing.T) {
	wikiReady := &models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionReady, SourceHash: "wiki-hash"}
	relationReady := &models.ProjectionRun{ProjectionType: ProjectionTypeRelation, Status: RelationReady, SourceHash: "relation-hash"}
	cases := []struct {
		name        string
		wiki, graph bool
		wikiRun     *models.ProjectionRun
		relationRun *models.ProjectionRun
		expected    string
	}{
		{"no projection", false, false, nil, nil, JoinEvidenceReady},
		{"graph enabled without relation run", false, true, nil, nil, JoinKnowledgePartial},
		{"graph enabled with ready relation", false, true, nil, relationReady, JoinKnowledgeReady},
		{"graph enabled with stale relation hash", false, true, nil,
			&models.ProjectionRun{ProjectionType: ProjectionTypeRelation, Status: RelationReady, SourceHash: "old"}, JoinKnowledgePartial},
		{"wiki without run", true, false, nil, nil, JoinKnowledgePartial},
		{"wiki ready with current hash", true, false, wikiReady, nil, JoinKnowledgeReady},
		{"wiki ready with stale hash", true, false,
			&models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionReady, SourceHash: "old"}, nil, JoinKnowledgePartial},
		{"wiki pending", true, false,
			&models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionPending, SourceHash: "wiki-hash"}, nil, JoinKnowledgePartial},
		{"wiki failed", true, false,
			&models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionFailed, SourceHash: "wiki-hash"}, nil, JoinKnowledgePartial},
		{"wiki retrying", true, false,
			&models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionRetrying, SourceHash: "wiki-hash"}, nil, JoinKnowledgePartial},
		{"both ready with current hashes", true, true, wikiReady, relationReady, JoinKnowledgeReady},
		{"wiki ready but relation failed", true, true, wikiReady,
			&models.ProjectionRun{ProjectionType: ProjectionTypeRelation, Status: RelationFailed, SourceHash: "relation-hash"}, JoinKnowledgePartial},
		{"relation ready but wiki pending", true, true,
			&models.ProjectionRun{ProjectionType: ProjectionTypeWiki, Status: ProjectionPending, SourceHash: "wiki-hash"}, relationReady, JoinKnowledgePartial},
		{"wiki run labelled as relation ready does not satisfy wiki", true, true, relationReady, relationReady, JoinKnowledgePartial},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := ComputeJoin(testCase.wiki, testCase.graph, testCase.wikiRun, testCase.relationRun, "wiki-hash", "relation-hash")
			if actual != testCase.expected {
				t.Fatalf("expected %s, got %s", testCase.expected, actual)
			}
		})
	}
}

func TestValidateStageTransition(t *testing.T) {
	cases := []struct {
		runStatus string
		stage     string
		valid     bool
	}{
		{ProjectionPending, StageCollect, true},
		{ProjectionPending, StageGenerate, true},
		{ProjectionRunning, StageGenerate, true},
		{ProjectionRunning, StageFinalize, true},
		{ProjectionRetrying, StageFinalize, true},
		{ProjectionFailed, StageCollect, true},
		{ProjectionPending, StageFinalize, false},
		{ProjectionReady, StageCollect, false},
		{ProjectionCancelled, StageGenerate, false},
	}
	for _, testCase := range cases {
		err := ValidateStageTransition(testCase.runStatus, testCase.stage)
		if testCase.valid && err != nil {
			t.Fatalf("%s -> %s should be valid: %v", testCase.runStatus, testCase.stage, err)
		}
		if !testCase.valid && err == nil {
			t.Fatalf("%s -> %s should be rejected", testCase.runStatus, testCase.stage)
		}
	}
}

func TestResetRunForRetryUsesAFreshQueueAttempt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.ProjectionRun{}, &models.ProjectionStageRun{}, &models.TaskInbox{}, &models.OutboxEvent{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	now := time.Now()
	run := models.ProjectionRun{
		ID: "prj-retry", TenantID: "t1", KnowledgeBaseID: "kb-1", EvidencePackageID: "pkg-1",
		ProjectionType: ProjectionTypeWiki, Status: ProjectionFailed, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	stages := []models.ProjectionStageRun{
		{ProjectionRunID: run.ID, ProjectionType: ProjectionTypeWiki, Stage: StageCollect, Status: StageSucceeded,
			Attempts: 1, IdempotencyKey: run.ID + ":" + StageCollect, CreatedAt: now, UpdatedAt: now},
		{ProjectionRunID: run.ID, ProjectionType: ProjectionTypeWiki, Stage: StageFinalize, Status: StageFailed,
			Attempts: 1, IdempotencyKey: run.ID + ":" + StageFinalize, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&stages).Error; err != nil {
		t.Fatalf("create stages: %v", err)
	}
	payload, err := json.Marshal(Task{
		SchemaVersion: SchemaTask, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
		EvidencePackageID: run.EvidencePackageID, ProjectionRunID: run.ID,
		ProjectionType: ProjectionTypeWiki, Stage: StageFinalize,
		IdempotencyKey: run.ID + ":" + StageFinalize,
	})
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if err := db.Create(&models.OutboxEvent{
		TenantID: run.TenantID, AggregateType: "projection_run", AggregateID: run.ID,
		EventType: "projection.wiki", IdempotencyKey: run.ID + ":" + StageFinalize,
		Payload: string(payload), Attempts: 1, PublishedAt: &now, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create outbox event: %v", err)
	}
	inbox := models.TaskInbox{
		IdempotencyKey: run.ID + ":" + StageFinalize, TaskType: "projection.wiki." + StageFinalize,
		Status: inboxSucceeded, ProcessedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&inbox).Error; err != nil {
		t.Fatalf("create inbox: %v", err)
	}

	service := &Service{db: db}
	if err := service.resetRunForRetry(context.Background(), &run); err != nil {
		t.Fatalf("reset run for retry: %v", err)
	}

	var retried models.ProjectionRun
	if err := db.First(&retried, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if retried.Status != ProjectionRetrying {
		t.Fatalf("expected %s, got %s", ProjectionRetrying, retried.Status)
	}
	var stage models.ProjectionStageRun
	if err := db.First(&stage, "idempotency_key = ?", run.ID+":"+StageFinalize).Error; err != nil {
		t.Fatalf("reload stage: %v", err)
	}
	if stage.Status != StagePending {
		t.Fatalf("expected failed stage to reopen as %s, got %s", StagePending, stage.Status)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", run.ID+":"+StageFinalize).Error; err != nil {
		t.Fatalf("reload outbox event: %v", err)
	}
	if event.PublishedAt != nil || event.Attempts != 0 {
		t.Fatalf("expected outbox event to reopen, got publishedAt=%v attempts=%d", event.PublishedAt, event.Attempts)
	}
	var retryTask Task
	if err := json.Unmarshal([]byte(event.Payload), &retryTask); err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if retryTask.Attempt != stage.Attempts+1 {
		t.Fatalf("expected retry attempt %d, got %d", stage.Attempts+1, retryTask.Attempt)
	}
	previousQueueID := (Task{IdempotencyKey: retryTask.IdempotencyKey, Attempt: stage.Attempts}).QueueID()
	if retryTask.QueueID() == previousQueueID {
		t.Fatalf("retry must not reuse the completed queue id %s", previousQueueID)
	}
	claimed, err := service.claimInbox(context.Background(), retryTask)
	if err != nil {
		t.Fatalf("claim retry inbox: %v", err)
	}
	if !claimed {
		t.Fatalf("retry must be claimable even though the original attempt succeeded")
	}
}

func TestPackagesSourceHashCoversAllProjectionInputs(t *testing.T) {
	packages := []ingestion.EvidencePackage{
		{ID: "pkg-1", ContentHash: "hash-1"},
		{ID: "pkg-2", ContentHash: "hash-2"},
	}
	base, err := packagesSourceHash(packages, 1, "prompt-v1", "model-a")
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	reordered, _ := packagesSourceHash([]ingestion.EvidencePackage{packages[1], packages[0]}, 1, "prompt-v1", "model-a")
	if reordered == base {
		t.Fatalf("source hash must depend on package order as loaded")
	}
	variants := map[string]string{}
	variants["acl"], _ = packagesSourceHash(packages, 2, "prompt-v1", "model-a")
	variants["prompt"], _ = packagesSourceHash(packages, 1, "prompt-v2", "model-a")
	variants["model"], _ = packagesSourceHash(packages, 1, "prompt-v1", "model-b")
	changed := []ingestion.EvidencePackage{{ID: "pkg-1", ContentHash: "hash-1"}}
	variants["content"], _ = packagesSourceHash(changed, 1, "prompt-v1", "model-a")
	for name, variant := range variants {
		if variant == base {
			t.Fatalf("%s change must change the source hash", name)
		}
	}
}

func TestGeneratorProducesTraceablePages(t *testing.T) {
	generator := testGenerator(t, "local-extractive")
	packages := []ingestion.EvidencePackage{
		{
			ID: "pkg-1", ContentHash: "hash-1", ACLVersion: 1,
			Envelope: ingestion.Envelope{TenantID: "t1", KnowledgeBaseID: "kb-1", DocumentID: "doc-1", DocumentVersionID: "dv-1"},
			Chunks: []ingestion.Chunk{
				{ID: "c1", ParentID: "p1", Ordinal: 1, Content: "Alpha content", HeadingPath: "Intro", SourcePath: "a.md"},
				{ID: "c2", ParentID: "p1", Ordinal: 2, Content: "Beta content", HeadingPath: "Intro", SourcePath: "a.md"},
			},
		},
		{
			ID: "pkg-2", ContentHash: "hash-2", ACLVersion: 1,
			Envelope: ingestion.Envelope{TenantID: "t1", KnowledgeBaseID: "kb-1", DocumentID: "doc-2", DocumentVersionID: "dv-2"},
			Chunks: []ingestion.Chunk{
				{ID: "c3", ParentID: "p3", Ordinal: 1, Content: "Gamma content", HeadingPath: "Body", SourcePath: "b.md"},
			},
		},
	}
	pages, err := generator.Generate(context.Background(), "Knowledge base", packages)
	if err != nil {
		t.Fatalf("generate pages: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("expected two document pages and one overview, got %d", len(pages))
	}
	knownChunks := map[string]bool{"c1": true, "c2": true, "c3": true}
	overviewSources := map[string]bool{}
	for _, page := range pages {
		if len(page.Blocks) == 0 {
			t.Fatalf("page %s has no blocks", page.Title)
		}
		blockIDs := map[string]bool{}
		for _, block := range page.Blocks {
			if len(block.SourceChunkIDs) == 0 {
				t.Fatalf("page %s has a block without source chunks", page.Title)
			}
			for _, id := range block.SourceChunkIDs {
				if !knownChunks[id] {
					t.Fatalf("page %s cites unknown chunk %s", page.Title, id)
				}
				blockIDs[id] = true
			}
		}
		if len(page.Sources) != len(blockIDs) {
			t.Fatalf("page %s source map does not cover every cited chunk", page.Title)
		}
		for _, source := range page.Sources {
			if !blockIDs[source.ChunkID] {
				t.Fatalf("page %s maps uncited chunk %s", page.Title, source.ChunkID)
			}
			if source.DocumentVersionID == "" || source.DocumentID == "" {
				t.Fatalf("page %s source lacks document/version provenance", page.Title)
			}
		}
		if page.PageType == PageTypeOverview {
			for id := range blockIDs {
				overviewSources[id] = true
			}
		}
		if page.SourceHash == "" {
			t.Fatalf("page %s has an empty source hash", page.Title)
		}
	}
	if !overviewSources["c1"] || !overviewSources["c3"] {
		t.Fatalf("overview must span sources from multiple documents: %v", overviewSources)
	}
}

func TestPageHashIncludesModelAndPrompt(t *testing.T) {
	first := testGenerator(t, "model-a")
	second := testGenerator(t, "model-b")
	page := GeneratedPage{PageType: PageTypeDocument, Slug: "document-doc-1", Title: "a.md", Blocks: []models.WikiBlock{
		{HeadingPath: "Intro", Content: "Alpha", SourceChunkIDs: []string{"c1"}},
	}}
	packages := []ingestion.EvidencePackage{{ID: "pkg-1", ContentHash: "hash-1", ACLVersion: 1}}
	if first.PageHash(page, packages) == second.PageHash(page, packages) {
		t.Fatalf("model change must change the page hash")
	}
	before := first.PageHash(page, packages)
	changed := first.WithPromptVersion(first.PromptVersion() + "-changed")
	if changed.PageHash(page, packages) == before {
		t.Fatalf("prompt change must change the page hash")
	}
	if first.PageHash(page, packages) != first.PageHash(page, packages) {
		t.Fatalf("page hash must be deterministic")
	}
}

func TestFuseKnowledgeMergesPages(t *testing.T) {
	keyword := []KnowledgeHit{
		{PageID: "page-1", VersionID: "v1", Title: "one"},
		{PageID: "page-2", VersionID: "v2", Title: "two"},
	}
	vector := []KnowledgeHit{
		{PageID: "page-2", VersionID: "v2", Title: "two"},
	}
	fused := fuseKnowledge(keyword, vector, 60)
	if len(fused) != 2 {
		t.Fatalf("expected two fused pages, got %d", len(fused))
	}
	if fused[0].hit.PageID != "page-2" {
		t.Fatalf("page recalled by both adapters must rank first, got %s", fused[0].hit.PageID)
	}
	if fused[0].score <= fused[1].score {
		t.Fatalf("fused score must reflect both recall lists")
	}
}

func TestNormalizeSearchQueryAndSplitError(t *testing.T) {
	if actual := normalizeSearchQuery("  alpha \n\t beta  "); actual != "alpha beta" {
		t.Fatalf("unexpected normalized query %q", actual)
	}
	code, message := splitError(context.Canceled)
	if code != "projection_error" || !strings.Contains(message, "canceled") {
		t.Fatalf("unexpected split error %s / %s", code, message)
	}
	code, message = splitError(apperror.Terminalf("empty_projection", "nothing generated"))
	if code != "empty_projection" || message != "nothing generated" {
		t.Fatalf("unexpected terminal split error %s / %s", code, message)
	}
}
