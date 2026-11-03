package ingestion

import "fmt"

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

var stageOrder = []string{StageParse, StageNormalize, StageChunk, StageEmbed, StageIndex}

func NextStage(stage string) (string, bool) {
	switch stage {
	case StageParse:
		return StageNormalize, true
	case StageNormalize:
		return StageChunk, true
	case StageChunk:
		return StageEmbed, true
	case StageEmbed:
		return StageIndex, true
	default:
		return "", false
	}
}

func StatusForStage(stage string) string {
	switch stage {
	case StageParse:
		return StatusParsing
	case StageNormalize:
		return StatusNormalizing
	case StageChunk:
		return StatusChunking
	case StageEmbed:
		return StatusEmbedding
	case StageIndex:
		return StatusIndexing
	default:
		return StatusFailed
	}
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
	switch stage {
	case StageParse:
		return StatusParsed
	case StageNormalize:
		return StatusNormalized
	case StageChunk:
		return StatusChunked
	case StageEmbed:
		return StatusEmbedded
	case StageIndex:
		return StatusEvidenceReady
	default:
		return StatusFailed
	}
}
