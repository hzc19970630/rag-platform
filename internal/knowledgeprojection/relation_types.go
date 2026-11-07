package knowledgeprojection

import (
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection/relation"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

// Relation extraction types live in the relation subpackage; aliases keep the
// graph query/review layer and existing tests on the shared names.
const SchemaRelationProjection = relation.SchemaRelationProjection

type RelationProjectionPackage = relation.RelationProjectionPackage
type ExtractedEntity = relation.ExtractedEntity
type ExtractedRelation = relation.ExtractedRelation

// GraphSnapshotDetail is the review view of one extraction result. Sources are
// returned per relation so a reviewer can trace every edge before publishing.
type GraphSnapshotDetail struct {
	Snapshot  models.GraphSnapshot      `json:"snapshot"`
	Entities  []GraphEntityView         `json:"entities"`
	Relations []GraphRelationView       `json:"relations"`
	Aliases   []models.GraphEntityAlias `json:"aliases"`
}

type GraphEntityView struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Aliases    []string          `json:"aliases"`
	Sources    []GraphSource     `json:"sources"`
}

type GraphRelationView struct {
	ID         string            `json:"id"`
	SubjectID  string            `json:"subjectId"`
	Subject    string            `json:"subject"`
	Predicate  string            `json:"predicate"`
	ObjectID   string            `json:"objectId"`
	Object     string            `json:"object"`
	Properties map[string]string `json:"properties,omitempty"`
	Confidence float64           `json:"confidence"`
	Sources    []GraphSource     `json:"sources"`
}

type GraphSource struct {
	DocumentID        string `json:"documentId"`
	DocumentVersionID string `json:"documentVersionId"`
	ChunkID           string `json:"chunkId"`
	SourcePath        string `json:"sourcePath"`
	HeadingPath       string `json:"headingPath,omitempty"`
	PageNumber        int    `json:"pageNumber,omitempty"`
}

type GraphReviewInput struct {
	Action string `json:"action" binding:"required"`
	Note   string `json:"note"`
}

type GraphQueryOptions struct {
	KnowledgeBaseID string
	Query           string
	Depth           int
	Limit           int
}

type GraphQueryResult struct {
	Query      string              `json:"query"`
	SnapshotID string              `json:"snapshotId,omitempty"`
	Depth      int                 `json:"depth"`
	Nodes      []GraphNode         `json:"nodes"`
	Edges      []GraphRelationView `json:"edges"`
	Paths      []GraphPath         `json:"paths"`
	Truncated  bool                `json:"truncated"`
	Joined     bool                `json:"joined"`
}

type GraphNode struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Aliases    []string          `json:"aliases,omitempty"`
	Sources    []GraphSource     `json:"sources,omitempty"`
	Depth      int               `json:"depth"`
}

type GraphPath struct {
	NodeIDs []string `json:"nodeIds"`
	EdgeIDs []string `json:"edgeIds"`
}

// GraphRunDetail is the operator/reviewer view of one relation projection run.
type GraphRunDetail struct {
	Run      models.ProjectionRun        `json:"run"`
	Stages   []models.ProjectionStageRun `json:"stages"`
	Snapshot *models.GraphSnapshot       `json:"snapshot,omitempty"`
}

type graphEntityRecord struct {
	ID         string
	Canonical  string
	Name       string
	Type       string
	Properties map[string]string
	Aliases    []aliasRecord
}

type aliasRecord struct {
	Alias      string
	Normalized string
	Rule       string
	MergedKey  string
}

type graphRelationRecord struct {
	ID         string
	SubjectID  string
	Subject    string
	Predicate  string
	ObjectID   string
	Object     string
	Properties map[string]string
	Confidence float64
	SourceView []GraphSource
}
