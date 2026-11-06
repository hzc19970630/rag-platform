package views

import (
	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func ProjectionState(view knowledgeprojection.ProjectionStatus) httpapi.ProjectionState {
	return httpapi.ProjectionState{
		KnowledgeBaseID: view.KnowledgeBaseID,
		JoinStatus:      view.JoinStatus,
		WikiEnabled:     view.WikiEnabled,
		GraphEnabled:    view.GraphEnabled,
		Runs:            ProjectionRuns(view.Runs),
		RelationRuns:    ProjectionRuns(view.RelationRuns),
		Pages:           PageCounts(view.Pages),
		Graph:           GraphCounts(view.Graph),
	}
}

func PageCounts(view knowledgeprojection.PageCounts) httpapi.PageCounts {
	return httpapi.PageCounts{
		Total:         view.Total,
		Published:     view.Published,
		PendingReview: view.PendingReview,
		Stale:         view.Stale,
		Indexed:       view.Indexed,
	}
}

func GraphCounts(view knowledgeprojection.GraphCounts) httpapi.GraphCounts {
	return httpapi.GraphCounts{
		Snapshots:      view.Snapshots,
		ActiveSnapshot: view.ActiveSnapshot,
		Entities:       view.Entities,
		Relations:      view.Relations,
		PendingReview:  view.PendingReview,
	}
}

func ProjectionRun(row models.ProjectionRun) httpapi.ProjectionRun {
	return httpapi.ProjectionRun{
		ID:                row.ID,
		TenantID:          row.TenantID,
		KnowledgeBaseID:   row.KnowledgeBaseID,
		EvidencePackageID: row.EvidencePackageID,
		ProjectionType:    row.ProjectionType,
		Status:            row.Status,
		SourceHash:        row.SourceHash,
		ACLVersion:        row.ACLVersion,
		Model:             row.Model,
		PromptVersion:     row.PromptVersion,
		RetryOfRunID:      row.RetryOfRunID,
		ErrorCode:         row.ErrorCode,
		ErrorMessage:      row.ErrorMessage,
		CompletedAt:       row.CompletedAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func ProjectionRuns(rows []models.ProjectionRun) []httpapi.ProjectionRun {
	items := make([]httpapi.ProjectionRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProjectionRun(row))
	}
	return items
}

func ProjectionStageRun(row models.ProjectionStageRun) httpapi.ProjectionStageRun {
	return httpapi.ProjectionStageRun{
		ID:              row.ID,
		ProjectionRunID: row.ProjectionRunID,
		ProjectionType:  row.ProjectionType,
		Stage:           row.Stage,
		Status:          row.Status,
		Attempts:        row.Attempts,
		IdempotencyKey:  row.IdempotencyKey,
		ErrorCode:       row.ErrorCode,
		ErrorMessage:    row.ErrorMessage,
		StartedAt:       row.StartedAt,
		CompletedAt:     row.CompletedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func ProjectionStageRuns(rows []models.ProjectionStageRun) []httpapi.ProjectionStageRun {
	items := make([]httpapi.ProjectionStageRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProjectionStageRun(row))
	}
	return items
}

func WikiPage(row models.WikiPage) httpapi.WikiPage {
	return httpapi.WikiPage{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		KnowledgeBaseID:    row.KnowledgeBaseID,
		DocumentID:         row.DocumentID,
		PageType:           row.PageType,
		Slug:               row.Slug,
		Title:              row.Title,
		Status:             row.Status,
		CurrentVersionID:   row.CurrentVersionID,
		PublishedVersionID: row.PublishedVersionID,
		ACLVersion:         row.ACLVersion,
		SourceHash:         row.SourceHash,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func WikiPageVersion(row models.WikiPageVersion) httpapi.WikiPageVersion {
	return httpapi.WikiPageVersion{
		ID:              row.ID,
		WikiPageID:      row.WikiPageID,
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		Version:         row.Version,
		Content:         row.Content,
		SourceHash:      row.SourceHash,
		GenerationModel: row.GenerationModel,
		PromptVersion:   row.PromptVersion,
		ReviewStatus:    row.ReviewStatus,
		ReviewNote:      row.ReviewNote,
		ReviewedBy:      row.ReviewedBy,
		ReviewedAt:      row.ReviewedAt,
		ACLVersion:      row.ACLVersion,
		CreatedBy:       row.CreatedBy,
		CreatedAt:       row.CreatedAt,
	}
}

func WikiPageVersions(rows []models.WikiPageVersion) []httpapi.WikiPageVersion {
	items := make([]httpapi.WikiPageVersion, 0, len(rows))
	for _, row := range rows {
		items = append(items, WikiPageVersion(row))
	}
	return items
}

func WikiBlock(row models.WikiBlock) httpapi.WikiBlock {
	return httpapi.WikiBlock{
		HeadingPath:    row.HeadingPath,
		Content:        row.Content,
		SourceChunkIDs: cloneStrings(row.SourceChunkIDs),
	}
}

func WikiBlocks(rows []models.WikiBlock) []httpapi.WikiBlock {
	items := make([]httpapi.WikiBlock, 0, len(rows))
	for _, row := range rows {
		items = append(items, WikiBlock(row))
	}
	return items
}

func WikiPageSource(row models.WikiPageSource) httpapi.WikiPageSource {
	return httpapi.WikiPageSource{
		TenantID:          row.TenantID,
		KnowledgeBaseID:   row.KnowledgeBaseID,
		WikiPageVersionID: row.WikiPageVersionID,
		DocumentID:        row.DocumentID,
		DocumentVersionID: row.DocumentVersionID,
		ChunkID:           row.ChunkID,
		SourcePath:        row.SourcePath,
		HeadingPath:       row.HeadingPath,
		PageNumber:        row.PageNumber,
	}
}

func WikiPageSources(rows []models.WikiPageSource) []httpapi.WikiPageSource {
	items := make([]httpapi.WikiPageSource, 0, len(rows))
	for _, row := range rows {
		items = append(items, WikiPageSource(row))
	}
	return items
}

func WikiPageSummary(view knowledgeprojection.PageSummary) httpapi.WikiPageSummary {
	result := httpapi.WikiPageSummary{Page: WikiPage(view.Page)}
	if view.CurrentVersion != nil {
		version := WikiPageVersion(*view.CurrentVersion)
		result.CurrentVersion = &version
	}
	if view.PublishedVersion != nil {
		version := WikiPageVersion(*view.PublishedVersion)
		result.PublishedVersion = &version
	}
	return result
}

func WikiPageSummaries(rows []knowledgeprojection.PageSummary) []httpapi.WikiPageSummary {
	items := make([]httpapi.WikiPageSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, WikiPageSummary(row))
	}
	return items
}

func WikiPageDetail(view knowledgeprojection.PageDetail) httpapi.WikiPageDetail {
	result := httpapi.WikiPageDetail{
		Page:    WikiPage(view.Page),
		Blocks:  WikiBlocks(view.Blocks),
		Sources: WikiPageSources(view.Sources),
	}
	if view.Version != nil {
		version := WikiPageVersion(*view.Version)
		result.Version = &version
	}
	return result
}

func WikiRunDetail(view knowledgeprojection.RunDetail) httpapi.WikiRunDetail {
	return httpapi.WikiRunDetail{
		Run:    ProjectionRun(view.Run),
		Stages: ProjectionStageRuns(view.Stages),
	}
}

func KnowledgeSearchResult(view knowledgeprojection.SearchResult) httpapi.KnowledgeSearchResult {
	items := make([]httpapi.KnowledgeHit, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, KnowledgeHit(item))
	}
	return httpapi.KnowledgeSearchResult{Query: view.Query, Items: items, Joined: view.Joined}
}

func KnowledgeHit(view knowledgeprojection.KnowledgeHit) httpapi.KnowledgeHit {
	return httpapi.KnowledgeHit{
		PageID:          view.PageID,
		VersionID:       view.VersionID,
		KnowledgeBaseID: view.KnowledgeBaseID,
		Title:           view.Title,
		Content:         view.Content,
		Score:           view.Score,
		Sources:         WikiPageSources(view.Sources),
	}
}

func GraphQueryResult(view knowledgeprojection.GraphQueryResult) httpapi.GraphQueryResult {
	nodes := make([]httpapi.GraphNode, 0, len(view.Nodes))
	for _, node := range view.Nodes {
		nodes = append(nodes, GraphNode(node))
	}
	edges := make([]httpapi.GraphRelation, 0, len(view.Edges))
	for _, edge := range view.Edges {
		edges = append(edges, GraphRelation(edge))
	}
	paths := make([]httpapi.GraphPath, 0, len(view.Paths))
	for _, path := range view.Paths {
		paths = append(paths, GraphPath(path))
	}
	return httpapi.GraphQueryResult{
		Query:      view.Query,
		SnapshotID: view.SnapshotID,
		Depth:      view.Depth,
		Nodes:      nodes,
		Edges:      edges,
		Paths:      paths,
		Truncated:  view.Truncated,
		Joined:     view.Joined,
	}
}

func GraphNode(view knowledgeprojection.GraphNode) httpapi.GraphNode {
	return httpapi.GraphNode{
		ID:         view.ID,
		Name:       view.Name,
		Type:       view.Type,
		Properties: cloneMap(view.Properties),
		Aliases:    cloneStrings(view.Aliases),
		Sources:    GraphSources(view.Sources),
		Depth:      view.Depth,
	}
}

func GraphRelation(view knowledgeprojection.GraphRelationView) httpapi.GraphRelation {
	return httpapi.GraphRelation{
		ID:         view.ID,
		SubjectID:  view.SubjectID,
		Subject:    view.Subject,
		Predicate:  view.Predicate,
		ObjectID:   view.ObjectID,
		Object:     view.Object,
		Properties: cloneMap(view.Properties),
		Confidence: view.Confidence,
		Sources:    GraphSources(view.Sources),
	}
}

func GraphSource(view knowledgeprojection.GraphSource) httpapi.GraphSource {
	return httpapi.GraphSource{
		DocumentID:        view.DocumentID,
		DocumentVersionID: view.DocumentVersionID,
		ChunkID:           view.ChunkID,
		SourcePath:        view.SourcePath,
		HeadingPath:       view.HeadingPath,
		PageNumber:        view.PageNumber,
	}
}

func GraphSources(rows []knowledgeprojection.GraphSource) []httpapi.GraphSource {
	items := make([]httpapi.GraphSource, 0, len(rows))
	for _, row := range rows {
		items = append(items, GraphSource(row))
	}
	return items
}

func GraphPath(view knowledgeprojection.GraphPath) httpapi.GraphPath {
	return httpapi.GraphPath{NodeIDs: cloneStrings(view.NodeIDs), EdgeIDs: cloneStrings(view.EdgeIDs)}
}

func GraphSnapshot(row models.GraphSnapshot) httpapi.GraphSnapshot {
	return httpapi.GraphSnapshot{
		ID:              row.ID,
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		ProjectionRunID: row.ProjectionRunID,
		Version:         row.Version,
		Status:          row.Status,
		SourceHash:      row.SourceHash,
		ACLVersion:      row.ACLVersion,
		Model:           row.Model,
		PromptVersion:   row.PromptVersion,
		Stats:           row.Stats,
		ReviewNote:      row.ReviewNote,
		ReviewedBy:      row.ReviewedBy,
		ReviewedAt:      row.ReviewedAt,
		ActivatedAt:     row.ActivatedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func GraphSnapshots(rows []models.GraphSnapshot) []httpapi.GraphSnapshot {
	items := make([]httpapi.GraphSnapshot, 0, len(rows))
	for _, row := range rows {
		items = append(items, GraphSnapshot(row))
	}
	return items
}

func GraphEntityAlias(row models.GraphEntityAlias) httpapi.GraphEntityAlias {
	return httpapi.GraphEntityAlias{
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		EntityID:        row.EntityID,
		Alias:           row.Alias,
		Normalized:      row.Normalized,
		Rule:            row.Rule,
		MergedKey:       row.MergedKey,
		CreatedAt:       row.CreatedAt,
	}
}

func GraphSnapshotDetail(view knowledgeprojection.GraphSnapshotDetail) httpapi.GraphSnapshotDetail {
	entities := make([]httpapi.GraphEntity, 0, len(view.Entities))
	for _, entity := range view.Entities {
		entities = append(entities, GraphEntity(entity))
	}
	relations := make([]httpapi.GraphRelation, 0, len(view.Relations))
	for _, relation := range view.Relations {
		relations = append(relations, GraphRelation(relation))
	}
	aliases := make([]httpapi.GraphEntityAlias, 0, len(view.Aliases))
	for _, alias := range view.Aliases {
		aliases = append(aliases, GraphEntityAlias(alias))
	}
	return httpapi.GraphSnapshotDetail{
		Snapshot:  GraphSnapshot(view.Snapshot),
		Entities:  entities,
		Relations: relations,
		Aliases:   aliases,
	}
}

func GraphEntity(view knowledgeprojection.GraphEntityView) httpapi.GraphEntity {
	return httpapi.GraphEntity{
		ID:         view.ID,
		Name:       view.Name,
		Type:       view.Type,
		Properties: cloneMap(view.Properties),
		Aliases:    cloneStrings(view.Aliases),
		Sources:    GraphSources(view.Sources),
	}
}

func GraphRunDetail(view knowledgeprojection.GraphRunDetail) httpapi.GraphRunDetail {
	result := httpapi.GraphRunDetail{
		Run:    ProjectionRun(view.Run),
		Stages: ProjectionStageRuns(view.Stages),
	}
	if view.Snapshot != nil {
		snapshot := GraphSnapshot(*view.Snapshot)
		result.Snapshot = &snapshot
	}
	return result
}

func ReviewInput(input httpapi.WikiReviewRequest) knowledgeprojection.ReviewInput {
	return knowledgeprojection.ReviewInput{Action: input.Action, Note: input.Note}
}

func RollbackInput(input httpapi.WikiRollbackRequest) knowledgeprojection.RollbackInput {
	return knowledgeprojection.RollbackInput{VersionID: input.VersionID}
}

func GraphReviewInput(input httpapi.GraphReviewRequest) knowledgeprojection.GraphReviewInput {
	return knowledgeprojection.GraphReviewInput{Action: input.Action, Note: input.Note}
}
