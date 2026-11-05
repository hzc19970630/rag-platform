package knowledgeprojection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/relationstore"
)

// graph_snapshot.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// ListGraphSnapshots returns snapshots newest first; only ids and review state
// travel, never source content.
func (s *Service) ListGraphSnapshots(ctx context.Context, principal authz.Principal, knowledgeBaseID string, limit int) ([]models.GraphSnapshot, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, fmt.Errorf("relation store is not configured")
	}
	snapshots, err := s.store.ListSnapshots(ctx, principal.TenantID, knowledgeBaseID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]models.GraphSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, snapshotToModel(snapshot))
	}
	return result, nil
}

// GetGraphSnapshot is the review view: every entity, alias and relation source
// is returned so a reviewer can audit the projection before activating it.

// GetGraphSnapshot is the review view: every entity, alias and relation source
// is returned so a reviewer can audit the projection before activating it.
func (s *Service) GetGraphSnapshot(ctx context.Context, principal authz.Principal, snapshotID string) (GraphSnapshotDetail, error) {
	if s.store == nil {
		return GraphSnapshotDetail{}, fmt.Errorf("relation store is not configured")
	}
	snapshot, err := s.store.SnapshotByID(ctx, principal.TenantID, snapshotID)
	if err != nil {
		if errors.Is(err, relationstore.ErrNotFound) {
			return GraphSnapshotDetail{}, apperror.ErrNotFound
		}
		return GraphSnapshotDetail{}, err
	}
	if _, err := s.authorize(ctx, principal, snapshot.KnowledgeBaseID, "read"); err != nil {
		return GraphSnapshotDetail{}, err
	}
	graph, err := s.loadSnapshotGraph(ctx, authz.AuthorizationScope{TenantID: principal.TenantID}, *snapshot, false)
	if err != nil {
		return GraphSnapshotDetail{}, err
	}
	detail := GraphSnapshotDetail{Snapshot: snapshotToModel(*snapshot), Entities: []GraphEntityView{}, Relations: []GraphRelationView{}, Aliases: []models.GraphEntityAlias{}}
	for _, entity := range graph.entities {
		view := GraphEntityView{ID: entity.ID, Name: entity.Name, Type: entity.Type, Properties: entity.Properties, Aliases: []string{}}
		for _, alias := range entity.Aliases {
			view.Aliases = append(view.Aliases, alias.Alias)
		}
		for _, relation := range graph.relations {
			if relation.SubjectID == entity.ID || relation.ObjectID == entity.ID {
				view.Sources = append(view.Sources, dedupeGraphSources(relation.SourceView)...)
			}
		}
		view.Sources = dedupeGraphSources(view.Sources)
		detail.Entities = append(detail.Entities, view)
	}
	for _, relation := range graph.relations {
		detail.Relations = append(detail.Relations, GraphRelationView{
			ID: relation.ID, SubjectID: relation.SubjectID, Subject: relation.Subject,
			Predicate: relation.Predicate, ObjectID: relation.ObjectID, Object: relation.Object,
			Properties: relation.Properties, Confidence: relation.Confidence, Sources: relation.SourceView,
		})
	}
	for _, alias := range graph.aliases {
		detail.Aliases = append(detail.Aliases, models.GraphEntityAlias{
			TenantID: alias.TenantID, KnowledgeBaseID: alias.KnowledgeBaseID, EntityID: alias.EntityID,
			Alias: alias.Alias, Normalized: alias.Normalized, Rule: alias.Rule,
			MergedKey: alias.MergedKey, CreatedAt: alias.CreatedAt,
		})
	}
	return detail, nil
}

// ReviewGraphSnapshot approves or rejects a pending snapshot. Approval is the
// only path to ACTIVE; it re-validates every source and supersedes the previous
// ACTIVE snapshot in the same transaction.

// ReviewGraphSnapshot approves or rejects a pending snapshot. Approval is the
// only path to ACTIVE; it re-validates every source and supersedes the previous
// ACTIVE snapshot in the same transaction.
func (s *Service) ReviewGraphSnapshot(ctx context.Context, principal authz.Principal, snapshotID string, input GraphReviewInput) (models.GraphSnapshot, error) {
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "approve" && action != "reject" {
		return models.GraphSnapshot{}, apperror.Invalidf("unsupported review action %q", input.Action)
	}
	if s.store == nil {
		return models.GraphSnapshot{}, fmt.Errorf("relation store is not configured")
	}
	snapshot, err := s.store.SnapshotByID(ctx, principal.TenantID, snapshotID)
	if err != nil {
		if errors.Is(err, relationstore.ErrNotFound) {
			return models.GraphSnapshot{}, apperror.ErrNotFound
		}
		return models.GraphSnapshot{}, err
	}
	if _, err := s.requireReviewer(ctx, principal, snapshot.KnowledgeBaseID); err != nil {
		return models.GraphSnapshot{}, err
	}
	if snapshot.Status != models.GraphSnapshotPending {
		return models.GraphSnapshot{}, fmt.Errorf("%w: only pending graph snapshots can be reviewed", apperror.ErrConflict)
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, principal.TenantID, snapshot.KnowledgeBaseID)
	if err != nil {
		return models.GraphSnapshot{}, apperror.ErrNotFound
	}
	if snapshot.ACLVersion != knowledgeBase.ACLVersion {
		return models.GraphSnapshot{}, fmt.Errorf("%w: acl_changed: rebuild and review the snapshot under the current ACL", apperror.ErrConflict)
	}
	status := models.GraphSnapshotRejected
	if action == "approve" {
		if err := s.validateSnapshotSources(ctx, snapshot.ID); err != nil {
			return models.GraphSnapshot{}, err
		}
		status = models.GraphSnapshotActive
	}
	now := time.Now()
	update := relationstore.ReviewUpdate{Status: status, Note: input.Note, Reviewer: principal.SubjectID, At: now}
	if action == "approve" {
		err = s.store.Approve(ctx, principal.TenantID, snapshot.ID, snapshot.KnowledgeBaseID, update)
	} else {
		err = s.store.UpdateReview(ctx, snapshot.ID, update)
	}
	if err != nil {
		return models.GraphSnapshot{}, err
	}
	result := snapshotToModel(*snapshot)
	result.Status = status
	result.ReviewNote = input.Note
	result.ReviewedBy = principal.SubjectID
	result.ReviewedAt = &now
	s.recordAuditResource(ctx, principal, snapshot.KnowledgeBaseID, "graph_snapshot", snapshot.ID, "graph_review_"+status)
	return result, nil
}

// ListRelationRuns lists relation projection runs for the graph run panel.

func snapshotToModel(snapshot relationstore.Snapshot) models.GraphSnapshot {
	return models.GraphSnapshot{
		ID: snapshot.ID, TenantID: snapshot.TenantID, KnowledgeBaseID: snapshot.KnowledgeBaseID,
		ProjectionRunID: snapshot.ProjectionRunID, Version: snapshot.Version, Status: snapshot.Status,
		SourceHash: snapshot.SourceHash, ACLVersion: snapshot.ACLVersion, Model: snapshot.Model,
		PromptVersion: snapshot.PromptVersion, Stats: snapshot.Stats, ReviewNote: snapshot.ReviewNote,
		ReviewedBy: snapshot.ReviewedBy, ReviewedAt: snapshot.ReviewedAt, ActivatedAt: snapshot.ActivatedAt,
		CreatedAt: snapshot.CreatedAt, UpdatedAt: snapshot.UpdatedAt,
	}
}
