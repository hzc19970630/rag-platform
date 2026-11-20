package knowledgeprojection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openRelationUnitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.ProjectionRun{}, &models.ProjectionStageRun{}, &models.ProjectionUnit{},
		&models.TaskInbox{}, &models.OutboxEvent{}, &models.AuditLog{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedRelationRun(t *testing.T, db *gorm.DB, status string) *models.ProjectionRun {
	t.Helper()
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: "prj-units", TenantID: "t1", KnowledgeBaseID: "kb1", EvidencePackageID: "pkg-1",
		ProjectionType: ProjectionTypeRelation, Status: status, ACLVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	return &run
}

func seedRelationUnits(t *testing.T, db *gorm.DB, run *models.ProjectionRun, statuses ...string) []models.ProjectionUnit {
	t.Helper()
	now := time.Now().UTC()
	units := make([]models.ProjectionUnit, 0, len(statuses))
	for index, status := range statuses {
		payload := ""
		if status == StageSucceeded {
			value := `{"schema_version":"RelationProjectionPackageV1","entities":[{"key":"e` + string(rune('0'+index)) + `","name":"E"}],"relations":[]}`
			payload = value
		}
		unit := models.ProjectionUnit{
			ID:              run.ID + ":" + StageExtract + ":pkg-1:" + string(rune('0'+index)),
			ProjectionRunID: run.ID, PackageID: "pkg-1", BatchIndex: index,
			ChunkIDs: `["c` + string(rune('0'+index)) + `"]`, Status: status,
			CreatedAt: now, UpdatedAt: now,
		}
		if payload != "" {
			unit.Payload = &payload
			unit.CompletedAt = &now
		}
		units = append(units, unit)
	}
	if err := db.Create(&units).Error; err != nil {
		t.Fatalf("create units: %v", err)
	}
	return units
}

func relationUnitPackages() []ingestion.EvidencePackage {
	chunk := func(id string, ordinal int) evidence.Chunk {
		return evidence.Chunk{ID: id, ParentID: "parent-1", Ordinal: ordinal, Content: "text " + id, ContentHash: "hash-" + id}
	}
	return []ingestion.EvidencePackage{
		{
			ID: "pkg-b",
			Envelope: evidence.Envelope{
				TenantID: "t1", KnowledgeBaseID: "kb1", DocumentID: "d2", DocumentVersionID: "dv2",
			},
			ContentHash: "pkg-b-hash", ACLVersion: 1,
			Chunks: []evidence.Chunk{chunk("c1", 1), chunk("c2", 2), chunk("c3", 3), chunk("c4", 4), chunk("c5", 5)},
		},
		{
			ID: "pkg-a",
			Envelope: evidence.Envelope{
				TenantID: "t1", KnowledgeBaseID: "kb1", DocumentID: "d1", DocumentVersionID: "dv1",
			},
			ContentHash: "pkg-a-hash", ACLVersion: 1,
			Chunks: []evidence.Chunk{chunk("a1", 1), chunk("a2", 2), chunk("a3", 3), {ID: "parent", ParentID: "", Content: "parent text"}},
		},
	}
}

func TestBuildRelationUnitsSplitsAndCoversEveryChunk(t *testing.T) {
	run := &models.ProjectionRun{ID: "prj-1", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeRelation}
	units, err := buildRelationUnits(run, relationUnitPackages(), 2, time.Now())
	if err != nil {
		t.Fatalf("build units: %v", err)
	}
	// d1 has 3 child chunks -> 2 units; d2 has 5 -> 3 units. Documents are
	// ordered by DocumentID so d1 comes first.
	if len(units) != 5 {
		t.Fatalf("expected 5 units, got %d", len(units))
	}
	if units[0].PackageID != "pkg-a" || units[0].BatchIndex != 0 || units[4].PackageID != "pkg-b" || units[4].BatchIndex != 2 {
		t.Fatalf("unexpected unit order: %+v", units)
	}
	seen := map[string]bool{}
	for _, unit := range units {
		var ids []string
		if err := json.Unmarshal([]byte(unit.ChunkIDs), &ids); err != nil {
			t.Fatalf("decode chunk ids: %v", err)
		}
		if len(ids) == 0 || len(ids) > 2 {
			t.Fatalf("unit %s has %d chunks", unit.ID, len(ids))
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("chunk %s appears in more than one unit", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 8 {
		t.Fatalf("expected all 8 child chunks covered, got %d", len(seen))
	}
	again, err := buildRelationUnits(run, relationUnitPackages(), 2, time.Now())
	if err != nil {
		t.Fatalf("rebuild units: %v", err)
	}
	for index := range units {
		if units[index].ID != again[index].ID || units[index].ChunkIDs != again[index].ChunkIDs {
			t.Fatalf("unit split is not deterministic at %d", index)
		}
	}
}

func TestFanOutUnitsIsIdempotent(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationPending)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageCollect,
		Status: StageProcessing, IdempotencyKey: run.ID + ":" + StageCollect, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create collect stage: %v", err)
	}
	service := &Service{db: db, unitChunks: 2}
	_ = service
	stage := projectionStage{
		task: Task{
			SchemaVersion: SchemaRelationTask, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
			ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageCollect,
			IdempotencyKey: run.ID + ":" + StageCollect,
		},
		run: run, packages: relationUnitPackages(), unitChunks: 2,
	}

	var first []models.OutboxEvent
	if err := db.Transaction(func(tx *gorm.DB) error {
		events, err := stage.SucceedMany(tx, now)
		first = events
		return err
	}); err != nil {
		t.Fatalf("first fan-out: %v", err)
	}
	if len(first) != 5 {
		t.Fatalf("expected 5 unit events, got %d", len(first))
	}
	var second []models.OutboxEvent
	if err := db.Transaction(func(tx *gorm.DB) error {
		events, err := stage.SucceedMany(tx, now)
		second = events
		return err
	}); err != nil {
		t.Fatalf("second fan-out: %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("repeat fan-out changed the event count: %d vs %d", len(second), len(first))
	}
	var unitCount int64
	if err := db.Model(&models.ProjectionUnit{}).Where("projection_run_id = ?", run.ID).Count(&unitCount).Error; err != nil {
		t.Fatalf("count units: %v", err)
	}
	if unitCount != 5 {
		t.Fatalf("expected 5 persistent units, got %d", unitCount)
	}
	keys := map[string]bool{}
	for _, event := range first {
		if keys[event.IdempotencyKey] {
			t.Fatalf("duplicate unit event key %s", event.IdempotencyKey)
		}
		keys[event.IdempotencyKey] = true
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.UnitTotal != 5 || stored.UnitSucceeded != 0 || stored.UnitFailed != 0 {
		t.Fatalf("unexpected run counters: %+v", stored)
	}
	var extractStage models.ProjectionStageRun
	if err := db.First(&extractStage, "idempotency_key = ?", run.ID+":"+StageExtract).Error; err != nil {
		t.Fatalf("extract stage must exist after fan-out: %v", err)
	}
	_ = service
}

func TestLastUnitCompletionEmitsOneFinalize(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	units := seedRelationUnits(t, db, run, StagePending, StagePending)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageProcessing, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	service := &Service{db: db}
	ctx := context.Background()

	for index, unit := range units {
		task := Task{
			SchemaVersion: SchemaRelationTask, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
			EvidencePackageID: unit.PackageID, ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation,
			Stage: StageExtract, UnitID: unit.ID, IdempotencyKey: unit.ID,
		}
		stage := projectionUnitStage{task: task, run: run}
		payload := `{"schema_version":"RelationProjectionPackageV1","entities":[],"relations":[]}`
		stage.payload = &payload
		if err := service.runtime().Start(ctx, stage); err != nil {
			t.Fatalf("start unit %d: %v", index, err)
		}
		if err := service.runtime().Complete(ctx, unit.ID, stage); err != nil {
			t.Fatalf("complete unit %d: %v", index, err)
		}
		var finalize int64
		if err := db.Model(&models.OutboxEvent{}).Where("idempotency_key = ?", run.ID+":"+StageFinalize).Count(&finalize).Error; err != nil {
			t.Fatalf("count finalize events: %v", err)
		}
		if index == 0 && finalize != 0 {
			t.Fatalf("the first unit must not emit FINALIZE")
		}
	}
	var finalize int64
	if err := db.Model(&models.OutboxEvent{}).Where("idempotency_key = ?", run.ID+":"+StageFinalize).Count(&finalize).Error; err != nil {
		t.Fatalf("count finalize events: %v", err)
	}
	if finalize != 1 {
		t.Fatalf("expected exactly one FINALIZE event, got %d", finalize)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.UnitSucceeded != 2 || stored.UnitTotal != 2 || stored.UnitFailed != 0 {
		t.Fatalf("unexpected counters: %+v", stored)
	}
	var extractStage models.ProjectionStageRun
	if err := db.First(&extractStage, "idempotency_key = ?", run.ID+":"+StageExtract).Error; err != nil {
		t.Fatalf("reload extract stage: %v", err)
	}
	if extractStage.Status != StageSucceeded {
		t.Fatalf("extract stage must be succeeded, got %s", extractStage.Status)
	}
}

func TestResetRelationUnitsForRetryKeepsSucceededPayloads(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationFailed)
	units := seedRelationUnits(t, db, run, StageSucceeded, StageFailed)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageFailed, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	service := &Service{db: db}
	if err := service.resetRunForRetry(context.Background(), run); err != nil {
		t.Fatalf("reset for retry: %v", err)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != RelationRetrying {
		t.Fatalf("expected %s, got %s", RelationRetrying, stored.Status)
	}
	var kept models.ProjectionUnit
	if err := db.First(&kept, "id = ?", units[0].ID).Error; err != nil {
		t.Fatalf("reload succeeded unit: %v", err)
	}
	if kept.Status != StageSucceeded || kept.Payload == nil {
		t.Fatalf("a succeeded unit must keep its payload, got %+v", kept)
	}
	var retried models.ProjectionUnit
	if err := db.First(&retried, "id = ?", units[1].ID).Error; err != nil {
		t.Fatalf("reload failed unit: %v", err)
	}
	if retried.Status != StagePending {
		t.Fatalf("expected failed unit to reopen as %s, got %s", StagePending, retried.Status)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", units[1].ID).Error; err != nil {
		t.Fatalf("failed unit event must be reopened: %v", err)
	}
	var task Task
	if err := json.Unmarshal([]byte(event.Payload), &task); err != nil {
		t.Fatalf("decode unit event: %v", err)
	}
	if task.UnitID != units[1].ID || task.Attempt == 0 {
		t.Fatalf("expected a fresh unit attempt, got %+v", task)
	}
	var finalize int64
	if err := db.Model(&models.OutboxEvent{}).Where("idempotency_key = ?", run.ID+":"+StageFinalize).Count(&finalize).Error; err != nil {
		t.Fatalf("count finalize events: %v", err)
	}
	if finalize != 0 {
		t.Fatalf("unit retry must not emit FINALIZE")
	}
}

func TestResetRelationRetryReopensFinalizeWhenUnitsAreDone(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationFailed)
	seedRelationUnits(t, db, run, StageSucceeded, StageSucceeded)
	now := time.Now().UTC()
	stages := []models.ProjectionStageRun{
		{ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract, Status: StageSucceeded, Attempts: 1, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now},
		{ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageFinalize, Status: StageFailed, Attempts: 1, IdempotencyKey: run.ID + ":" + StageFinalize, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&stages).Error; err != nil {
		t.Fatalf("create stages: %v", err)
	}
	service := &Service{db: db}
	if err := service.resetRunForRetry(context.Background(), run); err != nil {
		t.Fatalf("reset for retry: %v", err)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", run.ID+":"+StageFinalize).Error; err != nil {
		t.Fatalf("finalize event must be reopened: %v", err)
	}
	var task Task
	if err := json.Unmarshal([]byte(event.Payload), &task); err != nil {
		t.Fatalf("decode finalize event: %v", err)
	}
	if task.Stage != StageFinalize || task.Attempt == 0 {
		t.Fatalf("unexpected finalize retry task: %+v", task)
	}
	var extractStage models.ProjectionStageRun
	if err := db.First(&extractStage, "idempotency_key = ?", run.ID+":"+StageExtract).Error; err != nil {
		t.Fatalf("reload extract stage: %v", err)
	}
	if extractStage.Status != StageSucceeded {
		t.Fatalf("finished extraction must stay succeeded, got %s", extractStage.Status)
	}
}

func TestRetryableUnitFailureKeepsTheRunRetrying(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	units := seedRelationUnits(t, db, run, StagePending)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageProcessing, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	service := &Service{db: db}
	stage := projectionUnitStage{task: Task{UnitID: units[0].ID}, run: run}
	if err := service.runtime().Fail(context.Background(), stage, "projection_error", "provider 502"); err != nil {
		t.Fatalf("fail unit: %v", err)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != RelationRetrying {
		t.Fatalf("a retryable unit failure must leave the run retrying, got %s", stored.Status)
	}
	var unit models.ProjectionUnit
	if err := db.First(&unit, "id = ?", units[0].ID).Error; err != nil {
		t.Fatalf("reload unit: %v", err)
	}
	if unit.Status != StageFailed {
		t.Fatalf("expected unit failed, got %s", unit.Status)
	}
}

func TestTerminalUnitFailureMarksTheRunFailed(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	units := seedRelationUnits(t, db, run, StagePending)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageProcessing, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	service := &Service{db: db}
	stage := projectionUnitStage{task: Task{UnitID: units[0].ID}, run: run, terminal: true}
	if err := service.runtime().Fail(context.Background(), stage, "evidence_chunk_missing", "chunk gone"); err != nil {
		t.Fatalf("fail unit: %v", err)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != RelationFailed {
		t.Fatalf("a terminal unit failure must fail the run, got %s", stored.Status)
	}
}

func TestAbandonRelationUnitFailsTheRunAndReleasesTheInbox(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationRetrying)
	units := seedRelationUnits(t, db, run, StageFailed)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageFailed, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	tenantID := "t1"
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: units[0].ID, TenantID: &tenantID, TaskType: "projection.relation.unit",
		Status: inboxProcessing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create inbox: %v", err)
	}
	service := &Service{db: db}
	if err := service.AbandonRelationUnit(context.Background(), "t1", run.ID, units[0].ID, errors.New("model provider returned 502")); err != nil {
		t.Fatalf("abandon unit: %v", err)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != RelationFailed {
		t.Fatalf("expected failed run after the retry budget is spent, got %s", stored.Status)
	}
	if stored.ErrorCode == "" {
		t.Fatalf("abandoned run must keep the last error")
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", units[0].ID).Error; err != nil {
		t.Fatalf("reload inbox: %v", err)
	}
	if inbox.Status != inboxFailed {
		t.Fatalf("expected the inbox released as failed, got %s", inbox.Status)
	}
}

func TestCancelRelationRunKeepsSucceededUnits(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	units := seedRelationUnits(t, db, run, StageSucceeded, StagePending, StageProcessing)
	now := time.Now().UTC()
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		Status: StageProcessing, IdempotencyKey: run.ID + ":" + StageExtract, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create extract stage: %v", err)
	}
	service := &Service{db: db, authz: fakeAuthorizer{}}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	cancelled, err := service.Cancel(context.Background(), principal, run.ID)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if cancelled.Status != ProjectionCancelled {
		t.Fatalf("expected cancelled run, got %s", cancelled.Status)
	}
	var stored models.ProjectionRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != ProjectionCancelled || stored.CompletedAt == nil {
		t.Fatalf("run must be cancelled with a completion time: %+v", stored)
	}
	var kept models.ProjectionUnit
	if err := db.First(&kept, "id = ?", units[0].ID).Error; err != nil {
		t.Fatalf("reload succeeded unit: %v", err)
	}
	if kept.Status != StageSucceeded {
		t.Fatalf("a finished unit must stay succeeded, got %s", kept.Status)
	}
	var stopped models.ProjectionUnit
	if err := db.First(&stopped, "id = ?", units[1].ID).Error; err != nil {
		t.Fatalf("reload pending unit: %v", err)
	}
	if stopped.Status != ProjectionCancelled {
		t.Fatalf("an unfinished unit must be cancelled, got %s", stopped.Status)
	}
	var audit models.AuditLog
	if err := db.First(&audit, "resource_id = ?", run.ID).Error; err != nil {
		t.Fatalf("cancel must be audited: %v", err)
	}
	if !strings.HasPrefix(audit.Reason, "graph_cancel:") {
		t.Fatalf("unexpected audit reason %q", audit.Reason)
	}
	if _, err := service.Cancel(context.Background(), principal, run.ID); err != nil {
		t.Fatalf("cancelling an already-cancelled run must be idempotent: %v", err)
	}
}

func TestBuildRelationUnitsDedupesRepeatedContent(t *testing.T) {
	run := &models.ProjectionRun{ID: "prj-dedup", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeRelation}
	chunk := func(id, hash string, ordinal int) evidence.Chunk {
		return evidence.Chunk{ID: id, ParentID: "parent-1", Ordinal: ordinal, Content: "text " + id, ContentHash: hash}
	}
	pkg := ingestion.EvidencePackage{
		ID: "pkg-dup",
		Envelope: evidence.Envelope{
			TenantID: "t1", KnowledgeBaseID: "kb1", DocumentID: "d1", DocumentVersionID: "dv1",
		},
		ContentHash: "pkg-dup-hash", ACLVersion: 1,
		Chunks: []evidence.Chunk{
			chunk("c1", "same", 1),
			chunk("c2", "other", 2),
			chunk("c3", "same", 3),
			chunk("c4", "other", 4),
			chunk("c5", "third", 5),
		},
	}
	units, err := buildRelationUnits(run, []ingestion.EvidencePackage{pkg}, 32, time.Now())
	if err != nil {
		t.Fatalf("build units: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("expected one unit after dedupe, got %d", len(units))
	}
	var ids []string
	if err := json.Unmarshal([]byte(units[0].ChunkIDs), &ids); err != nil {
		t.Fatalf("decode chunk ids: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 unique chunks, got %v", ids)
	}
	if ids[0] != "c1" || ids[1] != "c2" || ids[2] != "c5" {
		t.Fatalf("dedupe must keep the first copy of each text, got %v", ids)
	}
}

func TestMergeUnitsRejectsIncompleteRun(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	seedRelationUnits(t, db, run, StageSucceeded, StageFailed)
	service := &Service{db: db}
	if _, err := service.mergeUnits(context.Background(), run); err == nil {
		t.Fatalf("incomplete units must not merge")
	}
}

func TestMergeUnitsCombinesSucceededPayloads(t *testing.T) {
	db := openRelationUnitTestDB(t)
	run := seedRelationRun(t, db, RelationExtracting)
	now := time.Now().UTC()
	payloads := []string{
		`{"schema_version":"RelationProjectionPackageV1","entities":[{"key":"alpha","name":"Alpha"}],"relations":[{"subject_key":"alpha","predicate":"links","object_key":"beta","source_chunk_ids":["c0"]}]}`,
		`{"schema_version":"RelationProjectionPackageV1","entities":[{"key":"beta","name":"Beta"}],"relations":[{"subject_key":"beta","predicate":"links","object_key":"gamma","source_chunk_ids":["c1"]}]}`,
	}
	for index, payload := range payloads {
		value := payload
		unit := models.ProjectionUnit{
			ID: run.ID + ":" + StageExtract + ":pkg-1:" + string(rune('0'+index)), ProjectionRunID: run.ID,
			PackageID: "pkg-1", BatchIndex: index, ChunkIDs: `["c` + string(rune('0'+index)) + `"]`,
			Status: StageSucceeded, Payload: &value, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&unit).Error; err != nil {
			t.Fatalf("create unit: %v", err)
		}
	}
	service := &Service{db: db}
	merged, err := service.mergeUnits(context.Background(), run)
	if err != nil {
		t.Fatalf("merge units: %v", err)
	}
	if len(merged.Relations) != 2 || len(merged.Entities) != 3 {
		t.Fatalf("unexpected merged package: %+v", merged)
	}
}
