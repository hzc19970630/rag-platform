package ingestion

import (
	"fmt"

	"github.com/hzc19970630/rag-platform/internal/pipeline"
)

var transitions = map[string]map[string]bool{
	StatusReceived:    {StatusStored: true, StatusFailed: true, StatusCancelled: true},
	StatusStored:      {StatusParsing: true, StatusFailed: true, StatusCancelled: true},
	StatusParsing:     {StatusParsed: true, StatusFailed: true, StatusCancelled: true},
	StatusParsed:      {StatusNormalizing: true, StatusFailed: true, StatusCancelled: true},
	StatusNormalizing: {StatusNormalized: true, StatusFailed: true, StatusCancelled: true},
	StatusNormalized:  {StatusChunking: true, StatusFailed: true, StatusCancelled: true},
	StatusChunking:    {StatusChunked: true, StatusFailed: true, StatusCancelled: true},
	StatusChunked:     {StatusEmbedding: true, StatusFailed: true, StatusCancelled: true},
	StatusEmbedding:   {StatusEmbedded: true, StatusFailed: true, StatusCancelled: true},
	StatusEmbedded:    {StatusIndexing: true, StatusFailed: true, StatusCancelled: true},
	StatusIndexing:    {StatusEvidenceReady: true, StatusFailed: true, StatusCancelled: true},
	StatusFailed:      {StatusParsing: true, StatusNormalizing: true, StatusChunking: true, StatusEmbedding: true, StatusIndexing: true, StatusCancelled: true},
}

func ValidateTransition(from, to string) error {
	if from == to {
		return nil
	}
	if transitions[from][to] {
		return nil
	}
	return fmt.Errorf("invalid ingestion state transition %s -> %s", from, to)
}

var ingestionStageTable = pipeline.NewStageTable(
	pipeline.StageSpec{Stage: StageParse, Processing: StatusParsing, Completed: StatusParsed, EventType: "document." + StageNormalize},
	pipeline.StageSpec{Stage: StageNormalize, Processing: StatusNormalizing, Completed: StatusNormalized, EventType: "document." + StageChunk},
	pipeline.StageSpec{Stage: StageChunk, Processing: StatusChunking, Completed: StatusChunked, EventType: "document." + StageEmbed},
	pipeline.StageSpec{Stage: StageEmbed, Processing: StatusEmbedding, Completed: StatusEmbedded, EventType: "document." + StageIndex},
	pipeline.StageSpec{Stage: StageIndex, Processing: StatusIndexing, Completed: StatusEvidenceReady},
)

var stageOrder = ingestionStageTable.Stages()

func NextStage(stage string) (string, bool) {
	next, ok := ingestionStageTable.Next(stage)
	return next.Stage, ok
}

func StatusForStage(stage string) string {
	spec, ok := ingestionStageTable.Spec(stage)
	if !ok {
		return StatusFailed
	}
	return spec.Processing
}

func StageEventType(stage string) string {
	spec, ok := ingestionStageTable.Spec(stage)
	if !ok {
		return ""
	}
	return spec.EventType
}

// StatusBeforeStage is the status a pipeline and version hold while the stage is
// still pending. Restarting a stage must rewind to it: the state machine has no
// transition out of CANCELLED, so a cancelled pipeline cannot start a stage
// directly.
func StatusBeforeStage(stage string) string {
	switch stage {
	case StageParse:
		return StatusStored
	case StageNormalize:
		return StatusParsed
	case StageChunk:
		return StatusNormalized
	case StageEmbed:
		return StatusChunked
	case StageIndex:
		return StatusEmbedded
	default:
		return ""
	}
}

func CompletedStatusForStage(stage string) string {
	spec, ok := ingestionStageTable.Spec(stage)
	if !ok {
		return StatusFailed
	}
	return spec.Completed
}
