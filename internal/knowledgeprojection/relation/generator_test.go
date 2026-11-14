package relation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/contracts/projectioncache"
	"github.com/hzc19970630/rag-platform/internal/prompts"
)

type fakeChatter struct {
	name     string
	text     string
	err      error
	calls    int
	requests []model.ChatRequest
}

func (f *fakeChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	f.calls++
	f.requests = append(f.requests, request)
	if f.err != nil {
		return model.ChatResponse{}, f.err
	}
	return model.ChatResponse{Text: f.text}, nil
}

func (f *fakeChatter) Name() string {
	if f.name == "" {
		return "fake-chat"
	}
	return f.name
}

type fakeCache struct {
	data   map[projectioncache.Key][]byte
	getErr error
	putErr error
	gets   int
	puts   int
}

func (c *fakeCache) Get(_ context.Context, key projectioncache.Key) ([]byte, bool, error) {
	c.gets++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	payload, ok := c.data[key]
	return payload, ok, nil
}

func (c *fakeCache) Put(_ context.Context, key projectioncache.Key, payload []byte) error {
	c.puts++
	if c.putErr != nil {
		return c.putErr
	}
	if c.data == nil {
		c.data = map[projectioncache.Key][]byte{}
	}
	c.data[key] = append([]byte(nil), payload...)
	return nil
}

func (c *fakeCache) DeleteScope(context.Context, string, string, string) error { return nil }

func newTestExtractor(t *testing.T, chatter *fakeChatter) *Extractor {
	t.Helper()
	registry, err := prompts.Load("../../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	extractor, err := NewExtractor(chatter, registry)
	if err != nil {
		t.Fatalf("new extractor: %v", err)
	}
	return extractor
}

func relationChunk(id, content string, ordinal int) evidence.Chunk {
	return evidence.Chunk{ID: id, ParentID: "parent-1", Ordinal: ordinal, Content: content, HeadingPath: "H"}
}

func relationPackage(id, documentID string, chunks ...evidence.Chunk) evidence.Package {
	return evidence.Package{
		ID: id,
		Envelope: evidence.Envelope{
			TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: documentID, DocumentVersionID: "ver-" + documentID,
		},
		ContentHash: "hash-" + id, ACLVersion: 2, Chunks: chunks,
	}
}

func TestNewExtractorValidatesDependencies(t *testing.T) {
	registry, err := prompts.Load("../../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	if _, err := NewExtractor(nil, registry); err == nil {
		t.Fatal("nil chatter must be rejected")
	}
	if _, err := NewExtractor(&fakeChatter{}, nil); err == nil {
		t.Fatal("nil registry must be rejected")
	}
}

func TestDeterministicExtraction(t *testing.T) {
	chunks := []evidence.Chunk{
		{ID: "chunk-1", Content: "[[Acme|works_at|Globex|Acme Inc;ACME]] and [[|bad|x]] and [[alias|ignored|value]]"},
		{ID: "chunk-2", Content: "no triples here"},
	}
	result := DeterministicExtraction(chunks)
	if len(result.Entities) != 2 || len(result.Relations) != 1 {
		t.Fatalf("deterministic result = %+v", result)
	}
	entity := result.Entities[0]
	if entity.Name != "Acme" || len(entity.Aliases) != 2 || len(entity.SourceChunkIDs) != 1 {
		t.Fatalf("subject entity = %+v", entity)
	}
	relation := result.Relations[0]
	if relation.SubjectKey != "Acme" || relation.Predicate != "works_at" || relation.ObjectKey != "Globex" || relation.Confidence != 1 {
		t.Fatalf("relation = %+v", relation)
	}
	if len(DeterministicExtraction(nil).Relations) != 0 {
		t.Fatal("empty chunks must produce no relations")
	}
}

func TestExtractMergesModelAndDeterministicResults(t *testing.T) {
	modelOutput := RelationProjectionPackage{
		Entities: []ExtractedEntity{{Name: "Acme", Type: "company"}},
		Relations: []ExtractedRelation{{
			SubjectKey: "Acme", Predicate: "works_at", ObjectKey: "Globex", Confidence: 0.7,
		}},
	}
	text, _ := json.Marshal(modelOutput)
	chatter := &fakeChatter{text: "prefix " + string(text) + " suffix"}
	extractor := newTestExtractor(t, chatter)
	packages := []evidence.Package{relationPackage("pkg-1", "doc-1",
		relationChunk("chunk-1", "Acme works at Globex [[Acme|works_at|Globex]]", 0),
	)}
	result, stats, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 2, packages)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if stats.Generated != 1 || stats.Reused != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if result.TenantID != "tenant-a" || result.KnowledgeBaseID != "kb-a" || result.ACLVersion != 2 {
		t.Fatalf("package envelope = %+v", result)
	}
	if len(result.Relations) != 1 {
		t.Fatalf("relations = %+v", result.Relations)
	}
	relation := result.Relations[0]
	if relation.SubjectKey != "acme" || relation.ObjectKey != "globex" || len(relation.SourceChunkIDs) == 0 {
		t.Fatalf("merged relation = %+v", relation)
	}
	for _, entity := range result.Entities {
		if len(entity.SourceChunkIDs) == 0 {
			t.Fatalf("entity without sources = %+v", entity)
		}
	}
}

func TestExtractBatchesAndHandlesFailures(t *testing.T) {
	chatter := &fakeChatter{text: "not json"}
	extractor := newTestExtractor(t, chatter)
	var chunks []evidence.Chunk
	for index := 0; index < maxRelationChunksPerCall+1; index++ {
		chunks = append(chunks, relationChunk(fmt.Sprintf("chunk-%d", index), "text", index))
	}
	_, stats, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, []evidence.Package{relationPackage("pkg-1", "doc-1", chunks...)})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if stats.Generated != 1 || chatter.calls != 2 {
		t.Fatalf("batches = %d calls, stats = %+v", chatter.calls, stats)
	}
	for _, request := range chatter.requests {
		if len(request.Evidence) > maxRelationChunksPerCall {
			t.Fatalf("batch size = %d", len(request.Evidence))
		}
	}

	chatter.err = errors.New("model down")
	if _, _, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, []evidence.Package{relationPackage("pkg-2", "doc-2", relationChunk("c", "text", 0))}); err == nil {
		t.Fatal("model failure must propagate")
	}
}

func TestExtractSkipsPackagesWithoutChunksAndUsesCache(t *testing.T) {
	output := RelationProjectionPackage{Entities: []ExtractedEntity{{Name: "Acme", SourceChunkIDs: []string{"chunk-1"}}}}
	payload, _ := json.Marshal(output)
	chatter := &fakeChatter{text: string(payload)}
	extractor := newTestExtractor(t, chatter)
	cache := &fakeCache{}
	extractor.SetCache(cache)
	packages := []evidence.Package{
		relationPackage("pkg-parent", "doc-parent", evidence.Chunk{ID: "parent", ParentID: "", Content: "parent"}),
		relationPackage("pkg-1", "doc-1", relationChunk("chunk-1", "Acme", 0)),
	}
	if _, stats, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, packages); err != nil || stats.Generated != 1 {
		t.Fatalf("first extract = %+v, %v", stats, err)
	}
	calls := chatter.calls
	if _, stats, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, packages); err != nil || stats.Reused != 1 || stats.Generated != 0 {
		t.Fatalf("cached extract = %+v, %v", stats, err)
	}
	if chatter.calls != calls {
		t.Fatal("cache hit must not call the model")
	}

	corrupt := &fakeCache{data: map[projectioncache.Key][]byte{}}
	for key := range cache.data {
		corrupt.data[key] = []byte("{invalid")
	}
	extractor.SetCache(corrupt)
	if _, stats, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, packages); err != nil || stats.Generated != 1 {
		t.Fatalf("corrupt cache extract = %+v, %v", stats, err)
	}
	extractor.SetCache(&fakeCache{getErr: errors.New("get down"), putErr: errors.New("put down")})
	if _, _, err := extractor.Extract(context.Background(), "tenant-a", "kb-a", 1, packages); err != nil {
		t.Fatalf("cache errors must not fail extraction: %v", err)
	}
}

func TestParseRelationJSONAndNormalize(t *testing.T) {
	if _, ok := parseRelationJSON("no json here"); ok {
		t.Fatal("text without JSON must be rejected")
	}
	if _, ok := parseRelationJSON("{invalid}"); ok {
		t.Fatal("invalid JSON must be rejected")
	}
	parsed, ok := parseRelationJSON(`prefix {"entities":[{"name":"Acme"}],"relations":[]} suffix`)
	if !ok || len(parsed.Entities) != 1 || parsed.Entities[0].Name != "Acme" {
		t.Fatalf("parseRelationJSON = %+v, %v", parsed, ok)
	}

	cases := map[string]string{
		"  Acme Corp. ": "acme corp",
		"ACME_Corp":     "acme corp",
		"a-b.c_d":       "a b c d",
		"  --  ":        "",
		"北京 公司":         "北京 公司",
	}
	for input, want := range cases {
		if got := NormalizeEntityKey(input); got != want {
			t.Fatalf("NormalizeEntityKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMergeExtractionRules(t *testing.T) {
	raw := RelationProjectionPackage{
		Entities: []ExtractedEntity{
			{Name: "Acme Corp", Type: "company", Aliases: []string{"ACME"}, Properties: map[string]string{"country": "US"}, SourceChunkIDs: []string{"chunk-1"}},
			{Name: "Acme-Corp", Properties: map[string]string{"founded": "1999"}, SourceChunkIDs: []string{"chunk-2"}},
			{Name: "", Key: "Globex", SourceChunkIDs: []string{"chunk-2"}},
			{Name: "  ", SourceChunkIDs: []string{"chunk-3"}},
		},
		Relations: []ExtractedRelation{
			{SubjectKey: "Acme Corp", Predicate: "works_at", ObjectKey: "Globex", Confidence: 0.9, SourceChunkIDs: []string{"chunk-1"}, Properties: map[string]string{"since": "2020"}},
			{SubjectKey: "acme corp", Predicate: "WORKS_AT", ObjectKey: "globex", Confidence: 0.5, SourceChunkIDs: []string{"chunk-2", "chunk-1"}},
			{SubjectKey: "Acme", Predicate: "", ObjectKey: "Globex"},
			{SubjectKey: "Solo", Predicate: "knows", ObjectKey: "Nobody"},
		},
	}
	merged := MergeExtraction(raw)
	if merged.SchemaVersion != SchemaRelationProjection {
		t.Fatalf("schema version = %q", merged.SchemaVersion)
	}
	if len(merged.Entities) != 4 {
		t.Fatalf("entities = %+v", merged.Entities)
	}
	byKey := map[string]ExtractedEntity{}
	for _, entity := range merged.Entities {
		byKey[entity.Key] = entity
	}
	var acme ExtractedEntity
	for _, entity := range merged.Entities {
		if entity.Properties["country"] == "US" {
			acme = entity
		}
	}
	if acme.Key == "" {
		t.Fatalf("merged acme entity missing: %+v", merged.Entities)
	}
	if len(acme.SourceChunkIDs) != 2 || acme.Properties["founded"] != "1999" {
		t.Fatalf("merged acme = %+v", acme)
	}
	if _, ok := byKey["globex"]; !ok {
		t.Fatalf("relation endpoint must create an entity: %+v", merged.Entities)
	}
	if len(merged.Relations) != 1 {
		t.Fatalf("relations = %+v", merged.Relations)
	}
	worksAt := merged.Relations[0]
	if worksAt.Predicate != "works_at" || worksAt.ObjectKey != "globex" || worksAt.SubjectKey != acme.Key {
		t.Fatalf("merged relation = %+v", worksAt)
	}
	if worksAt.Confidence != 0.5 || len(worksAt.SourceChunkIDs) != 2 || worksAt.Properties["since"] != "2020" {
		t.Fatalf("merged relation details = %+v", worksAt)
	}
}

func TestExtractorMetadata(t *testing.T) {
	extractor := newTestExtractor(t, &fakeChatter{name: "model-a"})
	if extractor.Model() != "model-a" {
		t.Fatalf("model = %q", extractor.Model())
	}
	if !strings.HasPrefix(extractor.PromptVersion(), prompts.RelationExtraction+"/") {
		t.Fatalf("prompt version = %q", extractor.PromptVersion())
	}
	pkg := relationPackage("pkg-1", "doc-1", relationChunk("chunk-1", "text", 0))
	first := extractor.cacheKey(pkg)
	if first != extractor.cacheKey(pkg) {
		t.Fatal("cache key must be deterministic")
	}
	pkg.ACLVersion++
	if first == extractor.cacheKey(pkg) {
		t.Fatal("ACL version must change the cache key")
	}
}
