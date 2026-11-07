package ingestion

import (
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestStateMachineAllowsOnlySequentialStages(t *testing.T) {
	allowed := [][2]string{
		{StatusReceived, StatusStored},
		{StatusStored, StatusParsing},
		{StatusParsing, StatusParsed},
		{StatusParsed, StatusNormalizing},
		{StatusNormalizing, StatusNormalized},
		{StatusNormalized, StatusChunking},
		{StatusChunking, StatusChunked},
		{StatusChunked, StatusEmbedding},
		{StatusEmbedding, StatusEmbedded},
		{StatusEmbedded, StatusIndexing},
		{StatusIndexing, StatusEvidenceReady},
	}
	for _, pair := range allowed {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected %s -> %s to be allowed: %v", pair[0], pair[1], err)
		}
	}
	if err := ValidateTransition(StatusReceived, StatusEvidenceReady); err == nil {
		t.Fatal("expected state machine to reject a skipped stage")
	}
}

func TestStatusBeforeStageCanStartEachStage(t *testing.T) {
	for _, stage := range stageOrder {
		before := StatusBeforeStage(stage)
		if before == "" {
			t.Fatalf("expected a predecessor status for %s", stage)
		}
		if err := ValidateTransition(before, StatusForStage(stage)); err != nil {
			t.Fatalf("restarting %s from %s must be an allowed transition: %v", stage, before, err)
		}
	}
	if got := StatusBeforeStage("unknown"); got != "" {
		t.Fatalf("expected no predecessor for an unknown stage, got %q", got)
	}
}

func TestRestartTargetStageRewindsToFirstUnsucceeded(t *testing.T) {
	cancelled := []models.StageRun{
		{Stage: StageParse, Status: StageSucceeded},
		{Stage: StageNormalize, Status: StageSucceeded},
		{Stage: StageChunk, Status: StageCancelled},
		{Stage: StageEmbed, Status: StageCancelled},
	}
	if got := restartTargetStage(cancelled); got != StageChunk {
		t.Fatalf("expected restart at %s, got %q", StageChunk, got)
	}
	if got := restartTargetStage(cancelled[:2]); got != StageChunk {
		t.Fatalf("a missing stage must restart at %s, got %q", StageChunk, got)
	}
	complete := []models.StageRun{
		{Stage: StageParse, Status: StageSucceeded},
		{Stage: StageNormalize, Status: StageSucceeded},
		{Stage: StageChunk, Status: StageSucceeded},
		{Stage: StageEmbed, Status: StageSucceeded},
		{Stage: StageIndex, Status: StageSucceeded},
	}
	if got := restartTargetStage(complete); got != "" {
		t.Fatalf("a fully succeeded pipeline has no restart target, got %q", got)
	}
}

func TestTaskDoesNotCarryDocumentContent(t *testing.T) {
	task := Task{TaskType: "document.ingestion.v1", Stage: StageParse, ObjectKey: "tenant/doc/source/a.md", PackageID: "pkg-1"}
	if task.ObjectKey == "" || task.PackageID == "" {
		t.Fatal("task must carry references")
	}
	if task.ContentHash != "" {
		t.Fatal("task must not carry document content")
	}
}
