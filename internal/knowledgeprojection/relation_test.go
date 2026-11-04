package knowledgeprojection

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection/relation"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	pgrelationstore "github.com/example/rag-platform/internal/platform/relationstore"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeAuthorizer struct {
	deniedChunks  map[string]bool
	deniedActions map[string]bool
	filterErr     error
}

func (f fakeAuthorizer) Authorize(_ context.Context, _ authz.Principal, action, _ string) (bool, error) {
	return !f.deniedActions[action], nil
}

func (f fakeAuthorizer) BuildScope(_ context.Context, principal authz.Principal, target authz.QueryTarget) (authz.AuthorizationScope, error) {
	return authz.AuthorizationScope{
		SubjectID: principal.SubjectID, TenantID: principal.TenantID,
		KnowledgeBaseIDs: []string{target.KnowledgeBaseID},
	}, nil
}

func (f fakeAuthorizer) FilterReadable(_ context.Context, _ authz.AuthorizationScope, refs []authz.ResourceRef) ([]authz.ResourceRef, error) {
	if f.filterErr != nil {
		return nil, f.filterErr
	}
	result := make([]authz.ResourceRef, 0, len(refs))
	for _, ref := range refs {
		if !f.deniedChunks[ref.ResourceID] {
			result = append(result, ref)
		}
	}
	return result, nil
}

func testExtractor(t *testing.T) *Extractor {
	t.Helper()
	registry, err := prompts.Load(filepath.Join("..", "..", "config", "prompt_templates"))
	if err != nil {
		t.Fatalf("load prompt templates: %v", err)
	}
	extractor, err := NewExtractor(fakeChatter{name: "local-extractive"}, registry)
	if err != nil {
		t.Fatalf("new extractor: %v", err)
	}
	return extractor
}

func TestDeterministicExtractionKeepsEverySourceChunk(t *testing.T) {
	chunks := []ingestion.Chunk{
		{ID: "c1", Content: "[[Acme|supplies|Beta|Acme Corp;ACME]] and [[alias|ignored|Thing]]"},
		{ID: "c2", Content: "[[Beta|supplies|Gamma]]"},
	}
	raw := relation.DeterministicExtraction(chunks)
	if len(raw.Relations) != 2 {
		t.Fatalf("expected two triples, got %d: %+v", len(raw.Relations), raw.Relations)
	}
	for _, relation := range raw.Relations {
		if len(relation.SourceChunkIDs) != 1 || relation.SourceChunkIDs[0] == "" {
			t.Fatalf("relation %s must cite exactly the chunk it came from", relation.Predicate)
		}
	}
	if raw.Relations[0].SourceChunkIDs[0] != "c1" || raw.Relations[1].SourceChunkIDs[0] != "c2" {
		t.Fatalf("unexpected source mapping: %+v", raw.Relations)
	}

	merged := relation.MergeExtraction(raw)
	if len(merged.Entities) != 3 {
		t.Fatalf("expected Acme, Beta and Gamma, got %+v", merged.Entities)
	}
	var acme *ExtractedEntity
	for index := range merged.Entities {
		if merged.Entities[index].Key == "acme" {
			acme = &merged.Entities[index]
		}
	}
	if acme == nil || acme.Name != "Acme" || acme.AliasRules["acme corp"] != "declared_alias" {
		t.Fatalf("declared alias rule was lost: %+v", acme)
	}
	if len(acme.Aliases) != 1 || acme.Aliases[0] != "Acme Corp" {
		t.Fatalf("unexpected alias merge result: %+v", acme.Aliases)
	}
}

func TestMergeExtractionDropsUnsourcedRelationsAndMergesDuplicates(t *testing.T) {
	raw := RelationProjectionPackage{
		Entities: []ExtractedEntity{
			{Name: "Acme", Type: "Organization", Aliases: []string{"ACME Corp"}, SourceChunkIDs: []string{"c1"}},
		},
		Relations: []ExtractedRelation{
			{SubjectKey: "Acme", Predicate: "acquires", ObjectKey: "Beta", Confidence: 0.9, SourceChunkIDs: []string{"c1"}},
			{SubjectKey: "ACME Corp", Predicate: "acquires", ObjectKey: "Beta", Confidence: 0.7, SourceChunkIDs: []string{"c2"}},
			{SubjectKey: "Acme", Predicate: "ghost", ObjectKey: "Beta"},
		},
	}
	merged := relation.MergeExtraction(raw)
	if len(merged.Relations) != 1 {
		t.Fatalf("expected one deduplicated sourced relation, got %+v", merged.Relations)
	}
	relation := merged.Relations[0]
	if relation.SubjectKey != "acme" || relation.ObjectKey != "beta" || relation.Predicate != "acquires" {
		t.Fatalf("relation endpoint was not canonicalized: %+v", relation)
	}
	if len(relation.SourceChunkIDs) != 2 || relation.SourceChunkIDs[0] != "c1" || relation.SourceChunkIDs[1] != "c2" {
		t.Fatalf("relation sources were not merged: %+v", relation.SourceChunkIDs)
	}
	if relation.Confidence != 0.7 {
		t.Fatalf("expected the minimum confidence, got %v", relation.Confidence)
	}
	var acme, beta bool
	for _, entity := range merged.Entities {
		switch entity.Key {
		case "acme":
			acme = entity.Type == "Organization" && len(entity.SourceChunkIDs) == 1
		case "beta":
			beta = true
		}
	}
	if !acme || !beta {
		t.Fatalf("relation endpoints must stay closed entities: %+v", merged.Entities)
	}
}

func TestNormalizeEntityKey(t *testing.T) {
	cases := map[string]string{
		"  ACME-Ö Corp. ": "acme ö corp",
		"Acme   Inc":      "acme inc",
		"acme_inc":        "acme inc",
		"--":              "",
	}
	for input, expected := range cases {
		if actual := relation.NormalizeEntityKey(input); actual != expected {
			t.Fatalf("normalize %q: expected %q, got %q", input, expected, actual)
		}
	}
}

func TestRelationStageTransitions(t *testing.T) {
	valid := []struct{ status, stage string }{
		{RelationPending, StageCollect},
		{RelationRetrying, StageCollect},
		{RelationFailed, StageCollect},
		{RelationPending, StageExtract},
		{RelationExtracting, StageExtract},
		{RelationRetrying, StageExtract},
		{RelationExtracting, StageFinalize},
		{RelationRetrying, StageFinalize},
	}
	for _, testCase := range valid {
		if err := ValidateRelationStageTransition(testCase.status, testCase.stage); err != nil {
			t.Fatalf("%s -> %s should be valid: %v", testCase.status, testCase.stage, err)
		}
	}
	invalid := []struct{ status, stage string }{
		{RelationReady, StageCollect},
		{RelationPending, StageFinalize},
		{ProjectionCancelled, StageExtract},
	}
	for _, testCase := range invalid {
		if err := ValidateRelationStageTransition(testCase.status, testCase.stage); err == nil {
			t.Fatalf("%s -> %s should be rejected", testCase.status, testCase.stage)
		}
	}
	if next, ok := nextStageFor(ProjectionTypeRelation, StageCollect); !ok || next != StageExtract {
		t.Fatalf("relation COLLECT must advance to EXTRACT, got %q %v", next, ok)
	}
	if next, ok := nextStageFor(ProjectionTypeRelation, StageExtract); !ok || next != StageFinalize {
		t.Fatalf("relation EXTRACT must advance to FINALIZE, got %q %v", next, ok)
	}
	if _, ok := nextStageFor(ProjectionTypeRelation, StageFinalize); ok {
		t.Fatalf("FINALIZE is the last relation stage")
	}
	if stageStatusFor(ProjectionTypeRelation, StageCollect) != RelationPending {
		t.Fatalf("relation COLLECT must use RELATION_PENDING")
	}
	if stageStatusFor(ProjectionTypeRelation, StageExtract) != RelationExtracting {
		t.Fatalf("relation EXTRACT must use RELATION_EXTRACTING")
	}
}

func openGraphTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.Document{}, &models.DocumentVersion{}, &models.Chunk{},
		&models.EvidencePackage{}, &models.ProjectionRun{}, &models.ProjectionStageRun{},
		&models.GraphSnapshot{}, &models.GraphEntity{}, &models.GraphEntityAlias{},
		&models.GraphRelation{}, &models.GraphRelationSource{}, &models.AuditLog{},
		&models.KnowledgeProjectionState{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedGraphKnowledgeBase(t *testing.T, db *gorm.DB, aclVersion int64) models.KnowledgeBase {
	t.Helper()
	now := time.Now().UTC()
	knowledgeBase := models.KnowledgeBase{
		ID: "kb1", TenantID: "t1", OwnerID: "u1", Name: "Graph KB", ACLVersion: aclVersion,
		IndexingStrategy: `{"vector_enabled":true,"keyword_enabled":true,"graph_enabled":true,"required_capabilities":["evidence"]}`,
		CreatedAt:        now, UpdatedAt: now,
	}
	if err := db.Create(&knowledgeBase).Error; err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	return knowledgeBase
}

func seedGraphDocument(t *testing.T, db *gorm.DB, documentID, versionID, chunkID string) {
	t.Helper()
	now := time.Now().UTC()
	document := models.Document{
		ID: documentID, TenantID: "t1", KnowledgeBaseID: "kb1", OwnerID: "u1",
		Filename: documentID + ".md", MimeType: "text/markdown", Status: ingestion.StatusEvidenceReady,
		LatestVersionID: versionID, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&document).Error; err != nil {
		t.Fatalf("create document: %v", err)
	}
	version := models.DocumentVersion{
		ID: versionID, DocumentID: documentID, TenantID: "t1", KnowledgeBaseID: "kb1",
		Version: 1, ContentHash: versionID + "-hash", SourceObjectKey: "objects/" + versionID,
		Status: ingestion.StatusEvidenceReady, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&version).Error; err != nil {
		t.Fatalf("create document version: %v", err)
	}
	chunk := models.Chunk{
		ID: chunkID, TenantID: "t1", KnowledgeBaseID: "kb1", DocumentVersionID: versionID,
		Ordinal: 1, Content: "evidence " + chunkID, ContentHash: chunkID + "-hash",
		SourcePath: documentID + ".md", Status: ingestion.StatusEvidenceReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
}

type graphFixture struct {
	service *Service
	db      *gorm.DB
	auth    fakeAuthorizer
}

func newGraphFixture(t *testing.T) graphFixture {
	t.Helper()
	db := openGraphTestDB(t)
	seedGraphKnowledgeBase(t, db, 1)
	seedGraphDocument(t, db, "d1", "dv1", "c1")
	seedGraphDocument(t, db, "d2", "dv2", "c2")
	seedGraphDocument(t, db, "d3", "dv3", "c3")
	now := time.Now().UTC()
	if err := db.Create(&models.DocumentVersion{
		ID: "dv1b", DocumentID: "d1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 2,
		ContentHash: "dv1b-hash", SourceObjectKey: "objects/dv1b", Status: ingestion.StatusEvidenceReady,
		ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create replacement version: %v", err)
	}
	if err := db.Create(&models.GraphSnapshot{
		ID: "gs1", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: "prj1",
		Version: 1, Status: models.GraphSnapshotActive, ACLVersion: 1, SourceHash: "hash",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	entities := []models.GraphEntity{
		{ID: "ge-alpha", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "alpha", Name: "Alpha", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-beta", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "beta", Name: "Beta", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-gamma", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "gamma", Name: "Gamma", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-shared", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "shared", Name: "Shared", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-delta", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "delta", Name: "Delta", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-hidden", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1", CanonicalKey: "hidden", Name: "Hidden", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&entities).Error; err != nil {
		t.Fatalf("create entities: %v", err)
	}
	if err := db.Create(&models.GraphEntityAlias{
		TenantID: "t1", KnowledgeBaseID: "kb1", EntityID: "ge-alpha",
		Alias: "A-One", Normalized: "a one", Rule: "declared_alias", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	type edgeSeed struct {
		id, subject, object, predicate, chunkID, documentID, versionID string
	}
	edges := []edgeSeed{
		{"e1", "ge-alpha", "ge-beta", "knows", "c1", "d1", "dv1"},
		{"e2", "ge-beta", "ge-gamma", "knows", "c1", "d1", "dv1"},
		{"e3", "ge-gamma", "ge-shared", "shares", "c2", "d2", "dv2"},
		{"e6", "ge-beta", "ge-shared", "shares", "c2", "d2", "dv2"},
		{"e7", "ge-gamma", "ge-delta", "leads_to", "c1", "d1", "dv1"},
		{"e4", "ge-alpha", "ge-hidden", "leads_to", "c3", "d3", "dv3"},
		{"e5", "ge-shared", "ge-hidden", "leads_to", "c3", "d3", "dv3"},
	}
	for _, edge := range edges {
		if err := db.Create(&models.GraphRelation{
			ID: edge.id, TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs1",
			SubjectID: edge.subject, Predicate: edge.predicate, ObjectID: edge.object,
			Properties: "{}", Confidence: 1, ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create relation %s: %v", edge.id, err)
		}
		if err := db.Create(&models.GraphRelationSource{
			TenantID: "t1", KnowledgeBaseID: "kb1", RelationID: edge.id,
			DocumentID: edge.documentID, DocumentVersionID: edge.versionID, ChunkID: edge.chunkID,
			SourcePath: edge.documentID + ".md",
		}).Error; err != nil {
			t.Fatalf("create relation source %s: %v", edge.id, err)
		}
	}
	auth := fakeAuthorizer{deniedChunks: map[string]bool{"c3": true}}
	service := NewService(Options{DB: db, Authz: auth, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	return graphFixture{service: service, db: db, auth: auth}
}

func queryGraph(t *testing.T, fixture graphFixture, query string, depth, limit int) GraphQueryResult {
	t.Helper()
	result, err := fixture.service.QueryGraph(context.Background(), authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}, GraphQueryOptions{
		KnowledgeBaseID: "kb1", Query: query, Depth: depth, Limit: limit,
	})
	if err != nil {
		t.Fatalf("query graph: %v", err)
	}
	return result
}

func nodeIDSet(nodes []GraphNode) map[string]bool {
	result := map[string]bool{}
	for _, node := range nodes {
		result[node.ID] = true
	}
	return result
}

func edgeIDSet(edges []GraphRelationView) map[string]bool {
	result := map[string]bool{}
	for _, edge := range edges {
		result[edge.ID] = true
	}
	return result
}

func TestQueryGraphHidesNodesEdgesAndPathsWithoutSideChannel(t *testing.T) {
	fixture := newGraphFixture(t)
	result := queryGraph(t, fixture, "A-One", 3, 0)
	nodes := nodeIDSet(result.Nodes)
	for _, expected := range []string{"ge-alpha", "ge-beta", "ge-gamma", "ge-shared", "ge-delta"} {
		if !nodes[expected] {
			t.Fatalf("expected visible node %s, got %+v", expected, result.Nodes)
		}
	}
	if nodes["ge-hidden"] {
		t.Fatalf("an entity whose every edge source is denied must not be visible")
	}
	edges := edgeIDSet(result.Edges)
	for _, expected := range []string{"e1", "e2", "e3", "e6", "e7"} {
		if !edges[expected] {
			t.Fatalf("expected visible edge %s, got %+v", expected, result.Edges)
		}
	}
	if edges["e4"] || edges["e5"] {
		t.Fatalf("edges with a denied source must be hidden: %+v", result.Edges)
	}
	for _, edge := range result.Edges {
		for _, source := range edge.Sources {
			if source.ChunkID == "c3" {
				t.Fatalf("hidden source chunk leaked through edge %s", edge.ID)
			}
		}
	}
	if paths := len(result.Paths); paths == 0 {
		t.Fatalf("expected root-to-node paths")
	}
	for _, path := range result.Paths {
		for _, id := range path.NodeIDs {
			if id == "ge-hidden" {
				t.Fatalf("a hidden node leaked through a path: %+v", path)
			}
		}
	}

	hidden := queryGraph(t, fixture, "Hidden", 2, 0)
	if len(hidden.Nodes) != 0 || len(hidden.Edges) != 0 || len(hidden.Paths) != 0 {
		t.Fatalf("querying a hidden entity by name must return nothing: %+v", hidden)
	}
}

func TestQueryGraphSharedEntityDoesNotBridgeHiddenEvidence(t *testing.T) {
	fixture := newGraphFixture(t)
	result := queryGraph(t, fixture, "Shared", 1, 0)
	nodes := nodeIDSet(result.Nodes)
	if !nodes["ge-shared"] || !nodes["ge-beta"] || !nodes["ge-gamma"] {
		t.Fatalf("shared entity must expose only visible neighbours: %+v", result.Nodes)
	}
	if nodes["ge-hidden"] || nodes["ge-alpha"] || nodes["ge-delta"] {
		t.Fatalf("shared entity leaked a node beyond depth 1 or through hidden evidence: %+v", result.Nodes)
	}
	edges := edgeIDSet(result.Edges)
	if !edges["e3"] || !edges["e6"] || edges["e5"] {
		t.Fatalf("unexpected visible edges for shared entity: %+v", result.Edges)
	}
}

func TestQueryGraphDepthLimitAndNodeBudget(t *testing.T) {
	fixture := newGraphFixture(t)
	depthTwo := nodeIDSet(queryGraph(t, fixture, "Alpha", 2, 0).Nodes)
	if depthTwo["ge-delta"] {
		t.Fatalf("depth 2 must not reach the distance-3 entity")
	}
	depthThree := queryGraph(t, fixture, "Alpha", 3, 0)
	if !nodeIDSet(depthThree.Nodes)["ge-delta"] {
		t.Fatalf("depth 3 must reach the distance-3 entity")
	}
	if depthThree.Truncated {
		t.Fatalf("a complete traversal must not report truncation")
	}

	limited := queryGraph(t, fixture, "Alpha", 3, 2)
	if len(limited.Nodes) != 2 || !limited.Truncated {
		t.Fatalf("node budget must cap the result and report truncation: %+v", limited)
	}
	for _, edge := range limited.Edges {
		if edge.ID != "e1" {
			t.Fatalf("budgeted result must only contain edges between visited nodes: %+v", limited.Edges)
		}
	}
}

func TestQueryGraphDropsGraphWhenSnapshotSourceDrifts(t *testing.T) {
	fixture := newGraphFixture(t)
	if err := fixture.db.Model(&models.Document{}).Where("id = ?", "d1").
		Update("latest_version_id", "dv1b").Error; err != nil {
		t.Fatalf("replace document version: %v", err)
	}
	result := queryGraph(t, fixture, "A-One", 3, 0)
	if len(result.Nodes) != 0 || len(result.Edges) != 0 {
		t.Fatalf("relations whose source version is no longer current must disappear: %+v", result)
	}
	if _, err := fixture.service.reconcileGraph(context.Background()); err != nil {
		t.Fatalf("reconcile graph: %v", err)
	}
	var snapshot models.GraphSnapshot
	if err := fixture.db.First(&snapshot, "id = ?", "gs1").Error; err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}
	if snapshot.Status != models.GraphSnapshotStale {
		t.Fatalf("drifted snapshot must become STALE, got %s", snapshot.Status)
	}
}

func TestQueryGraphRequiresActiveSnapshotMatchingACL(t *testing.T) {
	fixture := newGraphFixture(t)
	if err := fixture.db.Model(&models.GraphSnapshot{}).Where("id = ?", "gs1").
		Update("status", models.GraphSnapshotPending).Error; err != nil {
		t.Fatalf("move snapshot back to review: %v", err)
	}
	if result := queryGraph(t, fixture, "Alpha", 2, 0); len(result.Nodes) != 0 {
		t.Fatalf("a non-ACTIVE snapshot must not answer queries: %+v", result)
	}
	if err := fixture.db.Model(&models.GraphSnapshot{}).Where("id = ?", "gs1").
		Updates(map[string]any{"status": models.GraphSnapshotActive, "acl_version": 99}).Error; err != nil {
		t.Fatalf("change snapshot ACL: %v", err)
	}
	if result := queryGraph(t, fixture, "Alpha", 2, 0); len(result.Nodes) != 0 {
		t.Fatalf("a snapshot from an older ACL must not answer queries: %+v", result)
	}
}

func TestReviewGraphSnapshotActivatesAndSupersedes(t *testing.T) {
	db := openGraphTestDB(t)
	seedGraphKnowledgeBase(t, db, 1)
	seedGraphDocument(t, db, "d1", "dv1", "c1")
	now := time.Now().UTC()
	for _, snapshot := range []models.GraphSnapshot{
		{ID: "gs-old", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: "prj-old", Version: 1, Status: models.GraphSnapshotActive, ACLVersion: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gs-new", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: "prj-new", Version: 2, Status: models.GraphSnapshotPending, ACLVersion: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&snapshot).Error; err != nil {
			t.Fatalf("create snapshot %s: %v", snapshot.ID, err)
		}
	}
	for _, entity := range []models.GraphEntity{
		{ID: "ge-a", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs-new", CanonicalKey: "alpha", Name: "Alpha", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
		{ID: "ge-b", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs-new", CanonicalKey: "beta", Name: "Beta", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&entity).Error; err != nil {
			t.Fatalf("create entity: %v", err)
		}
	}
	if err := db.Create(&models.GraphRelation{
		ID: "gr-1", TenantID: "t1", KnowledgeBaseID: "kb1", SnapshotID: "gs-new",
		SubjectID: "ge-a", Predicate: "knows", ObjectID: "ge-b", Properties: "{}", Confidence: 1,
		ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create relation: %v", err)
	}
	if err := db.Create(&models.GraphRelationSource{
		TenantID: "t1", KnowledgeBaseID: "kb1", RelationID: "gr-1",
		DocumentID: "d1", DocumentVersionID: "dv1", ChunkID: "c1", SourcePath: "d1.md",
	}).Error; err != nil {
		t.Fatalf("create relation source: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}

	reviewed, err := service.ReviewGraphSnapshot(context.Background(), principal, "gs-new", GraphReviewInput{Action: "approve", Note: "looks traceable"})
	if err != nil {
		t.Fatalf("approve snapshot: %v", err)
	}
	if reviewed.Status != models.GraphSnapshotActive || reviewed.ReviewedBy != "u1" {
		t.Fatalf("unexpected review result: %+v", reviewed)
	}
	var old models.GraphSnapshot
	if err := db.First(&old, "id = ?", "gs-old").Error; err != nil {
		t.Fatalf("reload previous snapshot: %v", err)
	}
	if old.Status != models.GraphSnapshotSuperseded {
		t.Fatalf("approval must supersede the previous ACTIVE snapshot, got %s", old.Status)
	}
	var audit models.AuditLog
	if err := db.First(&audit, "resource_id = ?", "gs-new").Error; err != nil {
		t.Fatalf("load audit: %v", err)
	}
	if audit.ResourceType != "graph_snapshot" || !strings.Contains(audit.Reason, "graph_review_ACTIVE") {
		t.Fatalf("unexpected audit record: %+v", audit)
	}
	if _, err := service.ReviewGraphSnapshot(context.Background(), principal, "gs-new", GraphReviewInput{Action: "reject"}); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("only pending snapshots may be reviewed, got %v", err)
	}
	apiKey := authz.Principal{SubjectID: "key-1", TenantID: "t1", CredentialType: "api_key"}
	if _, err := service.ReviewGraphSnapshot(context.Background(), apiKey, "gs-new", GraphReviewInput{Action: "approve"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api keys must not review snapshots, got %v", err)
	}
}

func TestReviewGraphSnapshotRejectsACLDrift(t *testing.T) {
	db := openGraphTestDB(t)
	seedGraphKnowledgeBase(t, db, 2)
	now := time.Now().UTC()
	if err := db.Create(&models.GraphSnapshot{
		ID: "gs-stale", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: "prj-1",
		Version: 1, Status: models.GraphSnapshotPending, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.ReviewGraphSnapshot(context.Background(), principal, "gs-stale", GraphReviewInput{Action: "approve"}); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("approval under an older ACL must conflict, got %v", err)
	}
}

func TestPersistSnapshotKeepsOnlyTraceableRelations(t *testing.T) {
	db := openGraphTestDB(t)
	knowledgeBase := seedGraphKnowledgeBase(t, db, 1)
	seedGraphDocument(t, db, "d1", "dv1", "c1")
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: "prj-rel", TenantID: "t1", KnowledgeBaseID: "kb1", EvidencePackageID: "pkg-1",
		ProjectionType: ProjectionTypeRelation, Status: RelationExtracting, SourceHash: "hash",
		ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create relation run: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	packages := []ingestion.EvidencePackage{{
		ID: "pkg-1", ACLVersion: 1,
		Envelope: ingestion.Envelope{TenantID: "t1", KnowledgeBaseID: "kb1", DocumentID: "d1", DocumentVersionID: "dv1"},
		Chunks:   []ingestion.Chunk{{ID: "c1", Content: "[[Alpha|knows|Beta]]", SourcePath: "d1.md"}},
	}}
	projection := RelationProjectionPackage{
		Entities: []ExtractedEntity{
			{Key: "alpha", Name: "Alpha", Aliases: []string{"A-One"}, AliasRules: map[string]string{"a one": "declared_alias"}, SourceChunkIDs: []string{"c1"}},
			{Key: "beta", Name: "Beta", SourceChunkIDs: []string{"c1"}},
		},
		Relations: []ExtractedRelation{
			{SubjectKey: "alpha", Predicate: "knows", ObjectKey: "beta", Confidence: 0.8, SourceChunkIDs: []string{"c1", "missing"}},
			{SubjectKey: "alpha", Predicate: "ghost", ObjectKey: "beta", SourceChunkIDs: []string{"missing"}},
		},
	}
	if err := service.persistSnapshot(context.Background(), &run, knowledgeBase, packages, projection, now); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	var snapshot models.GraphSnapshot
	if err := db.First(&snapshot, "projection_run_id = ?", run.ID).Error; err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Status != models.GraphSnapshotBuilding {
		t.Fatalf("extraction must leave the snapshot in BUILDING, got %s", snapshot.Status)
	}
	var stats struct {
		Entities                 int `json:"entities"`
		Relations                int `json:"relations"`
		Aliases                  int `json:"aliases"`
		DroppedUnsourcedRelation int `json:"droppedUnsourcedRelations"`
	}
	if err := json.Unmarshal([]byte(snapshot.Stats), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Entities != 2 || stats.Relations != 1 || stats.Aliases != 1 || stats.DroppedUnsourcedRelation != 1 {
		t.Fatalf("unexpected snapshot stats: %+v", stats)
	}
	var relations []models.GraphRelation
	if err := db.Where("snapshot_id = ?", snapshot.ID).Find(&relations).Error; err != nil {
		t.Fatalf("load relations: %v", err)
	}
	if len(relations) != 1 || relations[0].Predicate != "knows" {
		t.Fatalf("the unsourced relation must never reach the snapshot: %+v", relations)
	}
	var sources []models.GraphRelationSource
	if err := db.Where("relation_id = ?", relations[0].ID).Find(&sources).Error; err != nil {
		t.Fatalf("load relation sources: %v", err)
	}
	if len(sources) != 1 || sources[0].DocumentID != "d1" || sources[0].DocumentVersionID != "dv1" || sources[0].ChunkID != "c1" {
		t.Fatalf("relation source lost document/version/chunk provenance: %+v", sources)
	}
	if err := service.validateSnapshotSources(context.Background(), snapshot.ID); err != nil {
		t.Fatalf("a fully sourced snapshot must pass validation: %v", err)
	}

	// Retrying EXTRACT for the same run replaces the snapshot instead of
	// duplicating entities, relations or aliases.
	if err := service.persistSnapshot(context.Background(), &run, knowledgeBase, packages, projection, now); err != nil {
		t.Fatalf("re-persist snapshot: %v", err)
	}
	for name, model := range map[string]any{
		"snapshots": &models.GraphSnapshot{}, "entities": &models.GraphEntity{},
		"aliases": &models.GraphEntityAlias{}, "relations": &models.GraphRelation{}, "sources": &models.GraphRelationSource{},
	} {
		var count int64
		query := db.Model(model)
		if name != "snapshots" {
			query = query.Where("knowledge_base_id = ?", "kb1")
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 1 && !(name == "entities" && count == 2) {
			t.Fatalf("re-persist duplicated %s: %d rows", name, count)
		}
	}

	if err := db.Model(&models.Chunk{}).Where("id = ?", "c1").Update("status", "PENDING").Error; err != nil {
		t.Fatalf("invalidate chunk: %v", err)
	}
	if err := service.validateSnapshotSources(context.Background(), snapshot.ID); err == nil {
		t.Fatalf("a snapshot whose source is no longer evidence-ready must fail validation")
	} else if terminal, ok := apperror.AsTerminal(err); !ok || terminal.Code != "stale_relation_source" {
		t.Fatalf("expected terminal stale_relation_source, got %v", err)
	}
}

func TestPersistSnapshotRejectsWhenEveryRelationIsUnsourced(t *testing.T) {
	db := openGraphTestDB(t)
	knowledgeBase := seedGraphKnowledgeBase(t, db, 1)
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: "prj-unsourced", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeRelation,
		Status: RelationExtracting, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	projection := RelationProjectionPackage{
		Entities:  []ExtractedEntity{{Key: "alpha", Name: "Alpha"}},
		Relations: []ExtractedRelation{{SubjectKey: "alpha", Predicate: "knows", ObjectKey: "beta", SourceChunkIDs: []string{"missing"}}},
	}
	err := service.persistSnapshot(context.Background(), &run, knowledgeBase, nil, projection, now)
	if err == nil {
		t.Fatalf("expected unsourced relations to be terminal")
	} else if terminal, ok := apperror.AsTerminal(err); !ok || terminal.Code != "unsourced_relations" {
		t.Fatalf("expected the snapshot to be rejected, got %v", err)
	}
	var count int64
	if err := db.Model(&models.GraphSnapshot{}).Count(&count).Error; err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if count != 0 {
		t.Fatalf("an unsourced snapshot must not be persisted")
	}
}

func TestFinalizeRelationCancelsWhenEvidenceDisappears(t *testing.T) {
	db := openGraphTestDB(t)
	seedGraphKnowledgeBase(t, db, 1)
	now := time.Now().UTC()
	run := models.ProjectionRun{
		ID: "prj-finalize", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeRelation,
		Status: RelationExtracting, SourceHash: "hash-from-collect", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := db.Create(&models.GraphSnapshot{
		ID: "gs-run", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionRunID: run.ID,
		Version: 1, Status: models.GraphSnapshotBuilding, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Generator: testGenerator(t, "local-extractive"), Extractor: testExtractor(t), Store: pgrelationstore.New(db)})
	if err := service.finalizeRelation(context.Background(), &run); err != nil {
		t.Fatalf("finalize relation: %v", err)
	}
	var updated models.ProjectionRun
	if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if updated.Status != ProjectionCancelled || updated.ErrorCode != "source_removed" {
		t.Fatalf("expected the run to cancel with source_removed, got %+v", updated)
	}
}

func TestGetGraphSnapshotReturnsReviewableView(t *testing.T) {
	fixture := newGraphFixture(t)
	principal := authz.Principal{SubjectID: "reviewer", TenantID: "t1", CredentialType: "session"}
	detail, err := fixture.service.GetGraphSnapshot(context.Background(), principal, "gs1")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if len(detail.Entities) != 6 || len(detail.Relations) != 7 {
		t.Fatalf("review view must expose every entity and relation, got %d/%d", len(detail.Entities), len(detail.Relations))
	}
	for _, relation := range detail.Relations {
		if len(relation.Sources) == 0 {
			t.Fatalf("review view exposed a relation without a source: %+v", relation)
		}
	}
	withAlias := false
	for _, entity := range detail.Entities {
		if entity.Name == "Alpha" && len(entity.Aliases) == 1 && entity.Aliases[0] == "A-One" {
			withAlias = true
		}
	}
	if !withAlias {
		t.Fatalf("review view must expose alias provenance: %+v", detail.Entities)
	}
}
