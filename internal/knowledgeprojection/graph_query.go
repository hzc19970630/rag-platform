package knowledgeprojection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection/relation"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/relationstore"
	"github.com/example/rag-platform/internal/retrieval"
)

const (
	defaultGraphDepth = 2
	maxGraphDepth     = 3
	defaultGraphLimit = 30
	maxGraphLimit     = 50
)

// QueryGraph answers a bounded node/edge/path query against the ACTIVE graph
// snapshot. Visibility is edge-first: a node or path is returned only when it is
// reachable through relations whose every source chunk is current,
// evidence-ready and readable by the caller. Relations without a readable
// source are hidden entirely, so the response carries no side channel.
func (s *Service) QueryGraph(ctx context.Context, principal authz.Principal, options GraphQueryOptions) (GraphQueryResult, error) {
	query := normalizeSearchQuery(options.Query)
	if query == "" {
		return GraphQueryResult{}, fmt.Errorf("query is required")
	}
	if strings.TrimSpace(options.KnowledgeBaseID) == "" {
		return GraphQueryResult{}, fmt.Errorf("knowledge base id is required")
	}
	depth := options.Depth
	if depth <= 0 {
		depth = defaultGraphDepth
	}
	if depth > maxGraphDepth {
		depth = maxGraphDepth
	}
	limit := options.Limit
	if limit <= 0 {
		limit = defaultGraphLimit
	}
	if limit > maxGraphLimit {
		limit = maxGraphLimit
	}
	scope, err := s.authorize(ctx, principal, options.KnowledgeBaseID, "read")
	if err != nil {
		return GraphQueryResult{}, err
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, principal.TenantID, options.KnowledgeBaseID)
	if err != nil {
		return GraphQueryResult{}, apperror.ErrNotFound
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return GraphQueryResult{}, err
	}
	result := GraphQueryResult{Query: query, Depth: depth, Nodes: []GraphNode{}, Edges: []GraphRelationView{}, Paths: []GraphPath{}}
	if !strategy.GraphEnabled {
		return result, nil
	}
	snapshot, err := s.activeSnapshot(ctx, principal.TenantID, knowledgeBase.ID, knowledgeBase.ACLVersion)
	if err != nil {
		return GraphQueryResult{}, err
	}
	if snapshot == nil {
		return result, nil
	}
	result.SnapshotID = snapshot.ID
	graph, err := s.loadSnapshotGraph(ctx, scope, *snapshot, true)
	if err != nil {
		return GraphQueryResult{}, err
	}
	seeds := graph.matchSeeds(query)
	if len(seeds) == 0 {
		return result, nil
	}

	nodesByID := graph.nodeIndex()
	visible := graph.visibleEdges()
	adjacency := map[string][]int{}
	for index, edge := range visible {
		adjacency[edge.SubjectID] = append(adjacency[edge.SubjectID], index)
		adjacency[edge.ObjectID] = append(adjacency[edge.ObjectID], index)
	}

	visited := map[string]graphVisit{}
	queue := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if _, ok := nodesByID[seed]; !ok {
			continue
		}
		visited[seed] = graphVisit{depth: 0}
		queue = append(queue, seed)
	}
	truncated := false
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		currentVisit := visited[current]
		if currentVisit.depth >= depth {
			continue
		}
		for _, edgeIndex := range adjacency[current] {
			edge := visible[edgeIndex]
			next := edge.ObjectID
			if next == current {
				next = edge.SubjectID
			}
			if _, seen := visited[next]; seen {
				continue
			}
			if len(visited) >= limit {
				truncated = true
				continue
			}
			visited[next] = graphVisit{depth: currentVisit.depth + 1, edgeID: edge.ID, parent: current}
			queue = append(queue, next)
		}
	}

	// Only edges whose both endpoints were reached inside the depth/node budget
	// are returned. A seed that has no such edge is dropped: entity existence
	// must not leak when all of its evidence is hidden.
	edgesByID := map[string]GraphRelationView{}
	nodeSources := map[string][]GraphSource{}
	for _, edge := range visible {
		if _, ok := visited[edge.SubjectID]; !ok {
			continue
		}
		if _, ok := visited[edge.ObjectID]; !ok {
			continue
		}
		edgesByID[edge.ID] = edge
		nodeSources[edge.SubjectID] = append(nodeSources[edge.SubjectID], edge.Sources...)
		nodeSources[edge.ObjectID] = append(nodeSources[edge.ObjectID], edge.Sources...)
	}
	visibleNodes := map[string]bool{}
	for _, edge := range edgesByID {
		visibleNodes[edge.SubjectID] = true
		visibleNodes[edge.ObjectID] = true
	}
	for id := range visited {
		if !visibleNodes[id] {
			delete(visited, id)
		}
	}
	if len(visited) == 0 {
		return result, nil
	}
	edgeIDs := make([]string, 0, len(edgesByID))
	for id := range edgesByID {
		edgeIDs = append(edgeIDs, id)
	}
	sort.Strings(edgeIDs)
	for _, id := range edgeIDs {
		result.Edges = append(result.Edges, edgesByID[id])
	}

	nodeIDs := make([]string, 0, len(visited))
	for id := range visited {
		nodeIDs = append(nodeIDs, id)
	}
	sort.SliceStable(nodeIDs, func(i, j int) bool {
		left, right := visited[nodeIDs[i]], visited[nodeIDs[j]]
		if left.depth != right.depth {
			return left.depth < right.depth
		}
		return nodeIDs[i] < nodeIDs[j]
	})
	for _, id := range nodeIDs {
		entity := nodesByID[id]
		node := GraphNode{
			ID: entity.ID, Name: entity.Name, Type: entity.Type, Properties: entity.Properties,
			Depth: visited[id].depth, Sources: dedupeGraphSources(nodeSources[id]),
		}
		for _, alias := range entity.Aliases {
			node.Aliases = append(node.Aliases, alias.Alias)
		}
		result.Nodes = append(result.Nodes, node)
	}

	paths := make([]GraphPath, 0)
	for id := range visited {
		path, ok := graph.pathFor(id, visited[id].edgeID, visited)
		if !ok || len(path.NodeIDs) == 0 {
			continue
		}
		paths = append(paths, path)
	}
	sort.SliceStable(paths, func(i, j int) bool {
		return strings.Join(paths[i].NodeIDs, "\x00") < strings.Join(paths[j].NodeIDs, "\x00")
	})
	result.Paths = paths

	joinStatus, err := s.joinStatus(ctx, knowledgeBase, strategy)
	if err != nil {
		return GraphQueryResult{}, err
	}
	result.Joined = joinStatus == JoinKnowledgeReady
	result.Truncated = truncated
	return result, nil
}

type graphVisit struct {
	depth  int
	edgeID string
	parent string
}

type snapshotGraph struct {
	entities  []graphEntityRecord
	aliases   []relationstore.Alias
	relations []graphRelationRecord
	byID      map[string]graphEntityRecord
	edgeByID  map[string]graphRelationRecord
}

func (s *Service) activeSnapshot(ctx context.Context, tenantID, knowledgeBaseID string, aclVersion int64) (*relationstore.Snapshot, error) {
	if s.store == nil {
		return nil, fmt.Errorf("relation store is not configured")
	}
	return s.store.ActiveSnapshot(ctx, tenantID, knowledgeBaseID, aclVersion)
}

// loadSnapshotGraph loads entities, aliases and relations from the Relation
// Store, then evaluates source validity and authorization once for the whole
// snapshot. With filterSources false the caller gets the reviewer view without
// per-source hiding.
func (s *Service) loadSnapshotGraph(ctx context.Context, scope authz.AuthorizationScope, snapshot relationstore.Snapshot, filterSources bool) (*snapshotGraph, error) {
	if s.store == nil {
		return nil, fmt.Errorf("relation store is not configured")
	}
	raw, err := s.store.Graph(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	graph := &snapshotGraph{aliases: raw.Aliases, byID: map[string]graphEntityRecord{}, edgeByID: map[string]graphRelationRecord{}}
	aliasByEntity := map[string][]aliasRecord{}
	for _, alias := range raw.Aliases {
		aliasByEntity[alias.EntityID] = append(aliasByEntity[alias.EntityID], aliasRecord{
			Alias: alias.Alias, Normalized: alias.Normalized, Rule: alias.Rule, MergedKey: alias.MergedKey,
		})
	}
	for _, entity := range raw.Entities {
		record := graphEntityRecord{
			ID: entity.ID, Canonical: entity.CanonicalKey, Name: entity.Name, Type: entity.EntityType,
			Properties: entity.Properties, Aliases: aliasByEntity[entity.ID],
		}
		graph.entities = append(graph.entities, record)
		graph.byID[entity.ID] = record
	}
	if len(raw.Relations) == 0 {
		return graph, nil
	}
	sourcesByRelation := map[string][]relationstore.Source{}
	for _, source := range raw.Sources {
		sourcesByRelation[source.RelationID] = append(sourcesByRelation[source.RelationID], source)
	}
	valid, readable, err := s.validateGraphSources(ctx, scope, raw.Sources, filterSources)
	if err != nil {
		return nil, err
	}
	for _, relation := range raw.Relations {
		subject, subjectOK := graph.byID[relation.SubjectID]
		object, objectOK := graph.byID[relation.ObjectID]
		if !subjectOK || !objectOK {
			continue
		}
		relationSources := sourcesByRelation[relation.ID]
		if len(relationSources) == 0 {
			continue
		}
		view := GraphRelationView{
			ID: relation.ID, SubjectID: relation.SubjectID, Subject: subject.Name,
			Predicate: relation.Predicate, ObjectID: relation.ObjectID, Object: object.Name,
			Properties: relation.Properties, Confidence: relation.Confidence,
		}
		allVisible := true
		for _, source := range relationSources {
			if filterSources {
				if !valid[sourceKey(source.RelationID, source.ChunkID)] || !readable[source.ChunkID] {
					allVisible = false
					break
				}
			}
			view.Sources = append(view.Sources, GraphSource{
				DocumentID: source.DocumentID, DocumentVersionID: source.DocumentVersionID,
				ChunkID: source.ChunkID, SourcePath: source.SourcePath,
				HeadingPath: source.HeadingPath, PageNumber: source.PageNumber,
			})
		}
		if filterSources && !allVisible {
			continue
		}
		record := graphRelationRecord{
			ID: relation.ID, SubjectID: relation.SubjectID, Subject: subject.Name,
			Predicate: relation.Predicate, ObjectID: relation.ObjectID, Object: object.Name,
			Properties: relation.Properties, Confidence: relation.Confidence,
			SourceView: view.Sources,
		}
		graph.relations = append(graph.relations, record)
		graph.edgeByID[record.ID] = record
	}
	return graph, nil
}

func sourceKey(relationID, chunkID string) string {
	return relationID + "\x00" + chunkID
}

// validateGraphSources returns the sources that still describe current evidence
// and, when filterSources is set, the subset the caller may read.
func (s *Service) validateGraphSources(ctx context.Context, scope authz.AuthorizationScope, sources []relationstore.Source, filterSources bool) (map[string]bool, map[string]bool, error) {
	readable := map[string]bool{}
	if len(sources) == 0 {
		return map[string]bool{}, readable, nil
	}
	refs := make([]relationstore.SourceRef, 0, len(sources))
	for _, source := range sources {
		refs = append(refs, relationstore.SourceRef{
			RelationID: source.RelationID, TenantID: source.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
			DocumentID: source.DocumentID, DocumentVersionID: source.DocumentVersionID, ChunkID: source.ChunkID,
		})
	}
	valid, err := s.currentEvidence(ctx, refs)
	if err != nil {
		return nil, nil, err
	}
	if !filterSources {
		return valid, readable, nil
	}
	authRefs := make([]authz.ResourceRef, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		if !valid[sourceKey(source.RelationID, source.ChunkID)] || seen[source.ChunkID] {
			continue
		}
		seen[source.ChunkID] = true
		authRefs = append(authRefs, authz.ResourceRef{
			TenantID: scope.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
			ResourceID: source.ChunkID, ResourceType: "chunk",
		})
	}
	allowed, err := s.authz.FilterReadable(ctx, scope, authRefs)
	if err != nil {
		return nil, nil, err
	}
	for _, ref := range allowed {
		readable[ref.ResourceID] = true
	}
	return valid, readable, nil
}

// currentEvidence verifies that every source reference still points at the
// current, evidence-ready chunk of its document version. The Relation Store
// only supplies references; this invariant belongs to the evidence domain.
func (s *Service) currentEvidence(ctx context.Context, refs []relationstore.SourceRef) (map[string]bool, error) {
	valid := map[string]bool{}
	if len(refs) == 0 {
		return valid, nil
	}
	chunkIDs := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.ChunkID == "" || seen[ref.ChunkID] {
			continue
		}
		seen[ref.ChunkID] = true
		chunkIDs = append(chunkIDs, ref.ChunkID)
	}
	if len(chunkIDs) == 0 {
		return valid, nil
	}
	var rows []struct {
		ChunkID           string `gorm:"column:chunk_id"`
		KnowledgeBaseID   string `gorm:"column:knowledge_base_id"`
		DocumentID        string `gorm:"column:document_id"`
		DocumentVersionID string `gorm:"column:document_version_id"`
	}
	if err := s.db.WithContext(ctx).Table("chunks c").
		Select("c.id AS chunk_id, c.knowledge_base_id, dv.document_id, c.document_version_id").
		Joins("JOIN document_versions dv ON dv.id = c.document_version_id").
		Joins("JOIN documents d ON d.id = dv.document_id").
		Where("c.id IN ?", chunkIDs).
		Where("d.latest_version_id = c.document_version_id AND d.deleted_at IS NULL").
		Where("d.status = ? AND dv.status = ? AND c.status = ?",
			ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady, ingestion.StatusEvidenceReady).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("validate graph sources: %w", err)
	}
	current := make(map[string]bool, len(rows))
	for _, row := range rows {
		current[evidenceKey(row.ChunkID, row.DocumentID, row.DocumentVersionID, row.KnowledgeBaseID)] = true
	}
	for _, ref := range refs {
		if current[evidenceKey(ref.ChunkID, ref.DocumentID, ref.DocumentVersionID, ref.KnowledgeBaseID)] {
			valid[sourceKey(ref.RelationID, ref.ChunkID)] = true
		}
	}
	return valid, nil
}

func evidenceKey(chunkID, documentID, documentVersionID, knowledgeBaseID string) string {
	return strings.Join([]string{chunkID, documentID, documentVersionID, knowledgeBaseID}, "\x00")
}

func (g *snapshotGraph) matchSeeds(query string) []string {
	normalized := relation.NormalizeEntityKey(query)
	if normalized == "" {
		return nil
	}
	seen := map[string]bool{}
	var seeds []string
	for _, entity := range g.entities {
		match := relation.NormalizeEntityKey(entity.Canonical) == normalized || relation.NormalizeEntityKey(entity.Name) == normalized
		if !match {
			for _, alias := range entity.Aliases {
				if alias.Normalized == normalized || relation.NormalizeEntityKey(alias.Alias) == normalized {
					match = true
					break
				}
			}
		}
		if match && !seen[entity.ID] {
			seen[entity.ID] = true
			seeds = append(seeds, entity.ID)
		}
	}
	sort.Strings(seeds)
	return seeds
}

func (g *snapshotGraph) nodeIndex() map[string]graphEntityRecord {
	return g.byID
}

func (g *snapshotGraph) visibleEdges() []GraphRelationView {
	edges := make([]GraphRelationView, 0, len(g.relations))
	for _, relation := range g.relations {
		edges = append(edges, GraphRelationView{
			ID: relation.ID, SubjectID: relation.SubjectID, Subject: relation.Subject,
			Predicate: relation.Predicate, ObjectID: relation.ObjectID, Object: relation.Object,
			Properties: relation.Properties, Confidence: relation.Confidence, Sources: relation.SourceView,
		})
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}

func (g *snapshotGraph) pathFor(nodeID, edgeID string, visited map[string]graphVisit) (GraphPath, bool) {
	if edgeID == "" {
		return GraphPath{}, false
	}
	nodeIDs := []string{nodeID}
	var edgeIDs []string
	current := nodeID
	edge := edgeID
	guard := 0
	for edge != "" {
		if guard > maxGraphLimit {
			return GraphPath{}, false
		}
		guard++
		relation, ok := g.edgeByID[edge]
		if !ok {
			return GraphPath{}, false
		}
		edgeIDs = append(edgeIDs, edge)
		parent := relation.SubjectID
		if parent == current {
			parent = relation.ObjectID
		}
		nodeIDs = append(nodeIDs, parent)
		previous, ok := visited[parent]
		if !ok {
			return GraphPath{}, false
		}
		current = parent
		edge = previous.edgeID
	}
	// Reverse into root-to-node order.
	for left, right := 0, len(nodeIDs)-1; left < right; left, right = left+1, right-1 {
		nodeIDs[left], nodeIDs[right] = nodeIDs[right], nodeIDs[left]
	}
	for left, right := 0, len(edgeIDs)-1; left < right; left, right = left+1, right-1 {
		edgeIDs[left], edgeIDs[right] = edgeIDs[right], edgeIDs[left]
	}
	return GraphPath{NodeIDs: nodeIDs, EdgeIDs: edgeIDs}, true
}

func dedupeGraphSources(sources []GraphSource) []GraphSource {
	if len(sources) == 0 {
		return nil
	}
	seen := map[string]bool{}
	result := make([]GraphSource, 0, len(sources))
	for _, source := range sources {
		if seen[source.ChunkID] {
			continue
		}
		seen[source.ChunkID] = true
		result = append(result, source)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ChunkID < result[j].ChunkID })
	return result
}

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
func (s *Service) ReviewGraphSnapshot(ctx context.Context, principal authz.Principal, snapshotID string, input GraphReviewInput) (models.GraphSnapshot, error) {
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "approve" && action != "reject" {
		return models.GraphSnapshot{}, fmt.Errorf("unsupported review action %q", input.Action)
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
		err = s.store.Approve(ctx, snapshot.ID, snapshot.KnowledgeBaseID, update)
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
func (s *Service) ListRelationRuns(ctx context.Context, principal authz.Principal, knowledgeBaseID string, limit int) ([]models.ProjectionRun, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var runs []models.ProjectionRun
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeRelation).
		Order("created_at DESC, id DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// GetGraphRun returns the relation run detail with its current snapshot.
func (s *Service) GetGraphRun(ctx context.Context, principal authz.Principal, runID string) (GraphRunDetail, error) {
	var run models.ProjectionRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND tenant_id = ? AND projection_type = ?", runID, principal.TenantID, ProjectionTypeRelation).Error; err != nil {
		return GraphRunDetail{}, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, run.KnowledgeBaseID, "read"); err != nil {
		return GraphRunDetail{}, err
	}
	var stages []models.ProjectionStageRun
	if err := s.db.WithContext(ctx).Where("projection_run_id = ?", run.ID).Order("id").Find(&stages).Error; err != nil {
		return GraphRunDetail{}, err
	}
	detail := GraphRunDetail{Run: run, Stages: stages}
	if s.store != nil {
		snapshot, err := s.store.SnapshotByRun(ctx, run.ID)
		if err == nil {
			model := snapshotToModel(*snapshot)
			detail.Snapshot = &model
		} else if !errors.Is(err, relationstore.ErrNotFound) {
			return GraphRunDetail{}, err
		}
	}
	return detail, nil
}

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
