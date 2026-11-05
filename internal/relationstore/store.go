// Package relationstore defines the graph storage port. The knowledge
// projection domain owns graph semantics (seed matching, bounded traversal,
// authorization and evidence validation); this package only describes how
// snapshots and their graph rows are persisted and loaded. The port carries no
// vendor query types, so a future graph database adapter can replace the
// PostgreSQL implementation without touching the domain.
package relationstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when the requested snapshot does not exist in the
// caller's tenant.
var ErrNotFound = errors.New("relation store: snapshot not found")

// Snapshot is the reviewable and publishable unit of the Relation Store.
// Version is assigned by the store on write; callers leave it zero.
type Snapshot struct {
	ID              string
	TenantID        string
	KnowledgeBaseID string
	ProjectionRunID string
	Version         int
	Status          string
	SourceHash      string
	ACLVersion      int64
	Model           string
	PromptVersion   string
	Stats           string
	ReviewNote      string
	ReviewedBy      string
	ReviewedAt      *time.Time
	ActivatedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Entity is one normalized graph node.
type Entity struct {
	ID              string
	TenantID        string
	KnowledgeBaseID string
	CanonicalKey    string
	Name            string
	EntityType      string
	Properties      map[string]string
	ACLVersion      int64
}

// Alias keeps one original surface form and the rule that produced it.
type Alias struct {
	TenantID        string
	KnowledgeBaseID string
	EntityID        string
	Alias           string
	Normalized      string
	Rule            string
	MergedKey       string
	CreatedAt       time.Time
}

// Relation is one directed edge between two entities.
type Relation struct {
	ID              string
	TenantID        string
	KnowledgeBaseID string
	SubjectID       string
	Predicate       string
	ObjectID        string
	Properties      map[string]string
	Confidence      float64
	ACLVersion      int64
}

// Source is the traceability record for one relation.
type Source struct {
	TenantID          string
	KnowledgeBaseID   string
	RelationID        string
	DocumentID        string
	DocumentVersionID string
	ChunkID           string
	SourcePath        string
	HeadingPath       string
	PageNumber        int
}

// Graph is one snapshot's content. Relations are limited to ACTIVE rows.
type Graph struct {
	Entities  []Entity
	Aliases   []Alias
	Relations []Relation
	Sources   []Source
}

// SourceRef is the minimum provenance needed to re-check that a source still
// describes current, evidence-ready content the caller may read.
type SourceRef struct {
	RelationID        string
	TenantID          string
	KnowledgeBaseID   string
	DocumentID        string
	DocumentVersionID string
	ChunkID           string
}

// SourceRefs reports how many relations have no source at all, plus the
// provenance of every existing source.
type SourceRefs struct {
	SourcelessRelations int
	Refs                []SourceRef
}

// Counts is the graph summary shown by the projection status panel.
type Counts struct {
	Snapshots      int64
	ActiveSnapshot string
	Entities       int64
	Relations      int64
	PendingReview  int64
}

// SnapshotWrite replaces the snapshot previously produced by the same
// projection run and inserts the supplied graph rows atomically.
type SnapshotWrite struct {
	Snapshot  Snapshot
	Entities  []Entity
	Aliases   []Alias
	Relations []Relation
	Sources   []Source
}

// ReviewUpdate is the reviewer decision recorded on a snapshot.
type ReviewUpdate struct {
	Status   string
	Note     string
	Reviewer string
	At       time.Time
}

// SnapshotUpdate patches lifecycle fields. Nil pointers are left unchanged.
type SnapshotUpdate struct {
	Status     string
	SourceHash *string
	ACLVersion *int64
	At         time.Time
}

// RelationStore is the graph persistence port. Implementations must scope
// every read by tenant or knowledge base; callers still apply authorization
// and evidence checks because those facts live outside the graph store.
type RelationStore interface {
	ActiveSnapshot(ctx context.Context, tenantID, knowledgeBaseID string, aclVersion int64) (*Snapshot, error)
	SnapshotByID(ctx context.Context, tenantID, snapshotID string) (*Snapshot, error)
	SnapshotByRun(ctx context.Context, runID string) (*Snapshot, error)
	ListSnapshots(ctx context.Context, tenantID, knowledgeBaseID string, limit int) ([]Snapshot, error)

	Graph(ctx context.Context, snapshotID string) (*Graph, error)
	SourceRefs(ctx context.Context, snapshotID string) (SourceRefs, error)
	Counts(ctx context.Context, tenantID, knowledgeBaseID string) (Counts, error)

	ReplaceRunSnapshot(ctx context.Context, write SnapshotWrite) error
	Update(ctx context.Context, snapshotID string, update SnapshotUpdate) error
	UpdateReview(ctx context.Context, snapshotID string, update ReviewUpdate) error
	Approve(ctx context.Context, tenantID, snapshotID, knowledgeBaseID string, update ReviewUpdate) error
	MarkStale(ctx context.Context, snapshotIDs []string, now time.Time) error
	MarkStaleByKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string, now time.Time) error
	StaleSnapshotIDs(ctx context.Context) ([]string, error)
}
