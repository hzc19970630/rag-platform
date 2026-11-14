package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

// EmbeddingMigration maps one persisted migration to the operator wire shape.
func EmbeddingMigration(row models.EmbeddingMigration) httpapi.EmbeddingMigrationV1 {
	return httpapi.EmbeddingMigrationV1{
		ID: row.ID, KnowledgeBaseID: row.KnowledgeBaseID, Status: row.Status,
		SourceModel: row.SourceModel, SourceDimension: row.SourceDimension,
		TargetModel: row.TargetModel, TargetDimension: row.TargetDimension,
		TotalChunks: row.TotalChunks, DoneChunks: row.DoneChunks,
		CursorChunkID: row.CursorChunkID, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
		CreatedBy: row.CreatedBy, StartedAt: row.StartedAt, ReadyAt: row.ReadyAt,
		CutoverAt: row.CutoverAt, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
