// Package knowledgeprojection implements Pipeline B: Wiki generation over an
// immutable EvidencePackage and the page-level Knowledge Index. It never reads
// source objects and never writes Pipeline A rows, so a Wiki failure cannot
// change Evidence RAG.
package knowledgeprojection

import (
	"context"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/status"
)

const (
	SchemaWikiTask     = "WikiProjectionTaskV1"
	SchemaRelationTask = "RelationProjectionTaskV1"
	// SchemaTask is kept as the wiki task alias used by the M6 code paths.
	SchemaTask = SchemaWikiTask

	ProjectionTypeWiki     = "wiki"
	ProjectionTypeRelation = "relation"

	StageCollect  = "COLLECT"
	StageExtract  = "EXTRACT"
	StageGenerate = "GENERATE"
	StageFinalize = "FINALIZE"

	ProjectionPending   = "WIKI_PENDING"
	ProjectionRunning   = "WIKI_GENERATING"
	ProjectionReady     = "WIKI_READY"
	ProjectionFailed    = "FAILED_WIKI"
	ProjectionRetrying  = "RETRY_WIKI"
	RelationPending     = "RELATION_PENDING"
	RelationExtracting  = "RELATION_EXTRACTING"
	RelationReady       = "RELATION_READY"
	RelationFailed      = "FAILED_RELATION"
	RelationRetrying    = "RETRY_RELATION"
	ProjectionCancelled = "CANCELLED"

	StagePending    = "PENDING"
	StageProcessing = "PROCESSING"
	StageSucceeded  = "SUCCEEDED"
	StageFailed     = "FAILED"

	ReviewPending  = "PENDING"
	ReviewApproved = "APPROVED"
	ReviewRejected = "REJECTED"

	PageDraft          = "DRAFT"
	PagePublished      = "PUBLISHED"
	PageReviewRequired = "REVIEW_REQUIRED"
	PageStale          = "STALE"
	PageArchived       = "ARCHIVED"

	PageTypeOverview = "overview"
	PageTypeDocument = "document"

	JoinEvidenceReady    = status.EvidenceReady
	JoinKnowledgeReady   = "KNOWLEDGE_READY"
	JoinKnowledgePartial = "KNOWLEDGE_PARTIAL"

	IndexActive = "ACTIVE"
	IndexStale  = "STALE"
)

// Task is one projection stage message. It carries only ids and a stage, never
// page content or object keys; the worker loads the EvidencePackage by id.
type Task struct {
	SchemaVersion     string `json:"schema_version"`
	TenantID          string `json:"tenant_id"`
	KnowledgeBaseID   string `json:"knowledge_base_id"`
	EvidencePackageID string `json:"evidence_package_id"`
	ProjectionRunID   string `json:"projection_run_id"`
	ProjectionType    string `json:"projection_type"`
	Stage             string `json:"stage"`
	IdempotencyKey    string `json:"idempotency_key"`
	// Attempt distinguishes queue task ids across independent retries while the
	// stage row keeps the same idempotency key.
	Attempt int `json:"attempt"`
}

func (t Task) QueueID() string {
	return t.IdempotencyKey + ":" + itoa(t.Attempt)
}

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
