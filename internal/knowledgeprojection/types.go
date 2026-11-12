// Package knowledgeprojection implements Pipeline B: Wiki generation over an
// immutable EvidencePackage and the page-level Knowledge Index. It never reads
// source objects and never writes Pipeline A rows, so a Wiki failure cannot
// change Evidence RAG.
package knowledgeprojection

import (
	"context"

	"github.com/hzc19970630/rag-platform/contracts/tasks"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/status"
)

const (
	SchemaWikiTask     = tasks.SchemaWiki
	SchemaRelationTask = tasks.SchemaRelation
	// SchemaTask is kept as the wiki task alias used by the M6 code paths.
	SchemaTask = SchemaWikiTask

	ProjectionTypeWiki     = "wiki"
	ProjectionTypeRelation = "relation"

	StageCollect  = "COLLECT"
	StageExtract  = "EXTRACT"
	StageGenerate = "GENERATE"
	StageFinalize = "FINALIZE"

	ProjectionPending   = status.WikiPending
	ProjectionRunning   = status.WikiGenerating
	ProjectionReady     = status.WikiReady
	ProjectionFailed    = status.WikiFailed
	ProjectionRetrying  = status.WikiRetrying
	RelationPending     = status.RelationPending
	RelationExtracting  = status.RelationExtracting
	RelationReady       = status.RelationReady
	RelationFailed      = status.RelationFailed
	RelationRetrying    = status.RelationRetrying
	ProjectionCancelled = status.Cancelled

	StagePending    = status.Pending
	StageProcessing = status.Processing
	StageSucceeded  = status.Succeeded
	StageFailed     = status.Failed

	ReviewPending  = status.Pending
	ReviewApproved = status.ReviewApproved
	ReviewRejected = status.ReviewRejected

	PageDraft          = status.Draft
	PagePublished      = status.Published
	PageReviewRequired = status.ReviewRequired
	PageStale          = status.Stale
	PageArchived       = status.Archived

	PageTypeOverview = "overview"
	PageTypeDocument = "document"

	JoinEvidenceReady    = status.EvidenceReady
	JoinKnowledgeReady   = status.KnowledgeReady
	JoinKnowledgePartial = status.KnowledgePartial

	IndexActive = status.Active
	IndexStale  = status.Stale
)

// Task is one projection stage message. It carries only ids and a stage, never
// page content or object keys; the worker loads the EvidencePackage by id.
type Task = tasks.Projection

// Publisher is implemented by the queue adapter; this package does not import
// transport code.
type Publisher interface {
	PublishWiki(ctx context.Context, task Task) error
	PublishRelation(ctx context.Context, task Task) error
}

// Trigger is the ingestion-side port. Pipeline A calls it after publishing an
// EvidencePackage; each enabled projection is triggered independently.
type Trigger interface {
	TriggerWiki(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error
	TriggerRelation(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error
}

// Lifecycle is the knowledge-base-side port. It runs outside a Pipeline A
// transaction and only touches projection-derived rows.
type Lifecycle interface {
	InvalidateKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID, reason string) error
	RebuildKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string) (int, error)
	RebuildRelations(ctx context.Context, tenantID, knowledgeBaseID string) (int, error)
}

type ProjectionStatus struct {
	KnowledgeBaseID string                 `json:"knowledgeBaseId"`
	JoinStatus      string                 `json:"joinStatus"`
	WikiEnabled     bool                   `json:"wikiEnabled"`
	GraphEnabled    bool                   `json:"graphEnabled"`
	Runs            []models.ProjectionRun `json:"runs"`
	RelationRuns    []models.ProjectionRun `json:"relationRuns"`
	Pages           PageCounts             `json:"pages"`
	Graph           GraphCounts            `json:"graph"`
}

type GraphCounts struct {
	Snapshots      int64  `json:"snapshots"`
	ActiveSnapshot string `json:"activeSnapshot,omitempty"`
	Entities       int64  `json:"entities"`
	Relations      int64  `json:"relations"`
	PendingReview  int64  `json:"pendingReview"`
}

type PageCounts struct {
	Total         int64 `json:"total"`
	Published     int64 `json:"published"`
	PendingReview int64 `json:"pendingReview"`
	Stale         int64 `json:"stale"`
	Indexed       int64 `json:"indexed"`
}

type PageDetail struct {
	Page    models.WikiPage         `json:"page"`
	Version *models.WikiPageVersion `json:"version,omitempty"`
	Blocks  []models.WikiBlock      `json:"blocks"`
	Sources []models.WikiPageSource `json:"sources"`
}

type PageSummary struct {
	Page             models.WikiPage         `json:"page"`
	CurrentVersion   *models.WikiPageVersion `json:"currentVersion,omitempty"`
	PublishedVersion *models.WikiPageVersion `json:"publishedVersion,omitempty"`
}

type KnowledgeHit struct {
	PageID          string                  `json:"pageId"`
	VersionID       string                  `json:"wikiPageVersionId"`
	KnowledgeBaseID string                  `json:"knowledgeBaseId"`
	Title           string                  `json:"title"`
	Content         string                  `json:"content"`
	Score           float64                 `json:"score"`
	Sources         []models.WikiPageSource `json:"sources"`
}

type SearchResult struct {
	Query  string         `json:"query"`
	Items  []KnowledgeHit `json:"items"`
	Joined bool           `json:"joined"`
}

type ReviewInput struct {
	Action string `json:"action" binding:"required"`
	Note   string `json:"note"`
}

type RollbackInput struct {
	VersionID string `json:"version_id" binding:"required"`
}

type RunDetail struct {
	Run    models.ProjectionRun        `json:"run"`
	Stages []models.ProjectionStageRun `json:"stages"`
}
