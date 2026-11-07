package knowledgeprojection

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection/relation"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
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
		return GraphQueryResult{}, apperror.Invalidf("query is required")
	}
	if strings.TrimSpace(options.KnowledgeBaseID) == "" {
		return GraphQueryResult{}, apperror.Invalidf("knowledge base id is required")
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
