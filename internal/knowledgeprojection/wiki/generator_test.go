package wiki

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/contracts/projectioncache"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
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

func newTestGenerator(t *testing.T) *Generator {
	t.Helper()
	registry, err := prompts.Load("../../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	generator, err := NewGenerator(&fakeChatter{text: "generated paragraph"}, registry)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	return generator
}

func testChunk(id, parent, heading, content string, ordinal int) evidence.Chunk {
	return evidence.Chunk{ID: id, ParentID: parent, Ordinal: ordinal, Content: content, HeadingPath: heading, SourcePath: "/docs/a.md", PageNumber: 1}
}

func testPackage(id, documentID, versionID, hash string, aclVersion int64, chunks ...evidence.Chunk) evidence.Package {
	return evidence.Package{
		ID: id,
		Envelope: evidence.Envelope{
			TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: documentID, DocumentVersionID: versionID,
		},
		ContentHash: hash, ACLVersion: aclVersion, Chunks: chunks,
	}
}

func TestNewGeneratorValidatesDependencies(t *testing.T) {
	registry, err := prompts.Load("../../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	if _, err := NewGenerator(nil, registry); err == nil {
		t.Fatal("nil chatter must be rejected")
	}
	if _, err := NewGenerator(&fakeChatter{}, nil); err == nil {
		t.Fatal("nil registry must be rejected")
	}
}

func TestGenerateDocumentPagesAndOverview(t *testing.T) {
	generator := newTestGenerator(t)
	pkg1 := testPackage("pkg-1", "doc-1", "ver-1", "hash-1", 3,
		testChunk("parent-1", "", "Intro", "parent text", 0),
		testChunk("chunk-1", "parent-1", "Intro", "first child", 0),
		testChunk("chunk-2", "parent-1", "Body", "second child", 1),
	)
	pkg2 := testPackage("pkg-2", "doc-2", "ver-2", "hash-2", 3,
		testChunk("chunk-3", "parent-2", "Other", "third child", 0),
	)
	empty := testPackage("pkg-empty", "doc-3", "ver-3", "hash-3", 3,
		testChunk("parent-3", "", "Only", "parent text", 0),
	)
	pages, stats, err := generator.Generate(context.Background(), "KB Title", []evidence.Package{pkg2, pkg1, empty})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if stats.Generated != 2 || stats.Reused != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %+v", pages)
	}
	if pages[0].PageType != PageTypeDocument || pages[0].DocumentID != "doc-1" || pages[0].Title != "a.md" {
		t.Fatalf("first page = %+v", pages[0])
	}
	if len(pages[0].Blocks) != 2 || len(pages[0].Sources) != 2 {
		t.Fatalf("first page blocks/sources = %+v", pages[0])
	}
	overview := pages[2]
	if overview.PageType != PageTypeOverview || overview.Slug != "overview" || overview.Title != "KB Title" {
		t.Fatalf("overview page = %+v", overview)
	}
	if len(overview.Blocks) != 2 || len(overview.Sources) != 3 {
		t.Fatalf("overview blocks/sources = %+v", overview)
	}
}

func TestGenerateFallbackAndErrors(t *testing.T) {
	generator := newTestGenerator(t)
	generator.chatter = &fakeChatter{text: ""}
	pkg := testPackage("pkg-1", "doc-1", "ver-1", "hash-1", 1,
		testChunk("chunk-1", "parent-1", "Intro", "fallback evidence text", 0),
	)
	pages, _, err := generator.Generate(context.Background(), "", []evidence.Package{pkg})
	if err != nil {
		t.Fatalf("Generate fallback: %v", err)
	}
	if pages[0].Blocks[0].Content != "fallback evidence text" {
		t.Fatalf("fallback content = %q", pages[0].Blocks[0].Content)
	}
	if pages[1].Title != "Knowledge base" {
		t.Fatalf("default overview title = %q", pages[1].Title)
	}

	generator.chatter = &fakeChatter{err: errors.New("model down")}
	if _, _, err := generator.Generate(context.Background(), "", []evidence.Package{pkg}); err == nil {
		t.Fatal("generation error must propagate")
	}
}

func TestGenerateCapsSectionsAndChunks(t *testing.T) {
	generator := newTestGenerator(t)
	var chunks []evidence.Chunk
	for index := 0; index < 8; index++ {
		chunks = append(chunks, testChunk(fmt.Sprintf("chunk-%d", index), "parent-1", fmt.Sprintf("Heading %d", index), "text", index))
	}
	pkg := testPackage("pkg-1", "doc-1", "ver-1", "hash-1", 1, chunks...)
	pages, _, err := generator.Generate(context.Background(), "", []evidence.Package{pkg})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(pages[0].Blocks) != maxSectionsPerPage {
		t.Fatalf("blocks = %d, want %d", len(pages[0].Blocks), maxSectionsPerPage)
	}
	for _, request := range generator.chatter.(*fakeChatter).requests {
		if len(request.Evidence) > maxChunksPerBlock {
			t.Fatalf("evidence items = %d, want <= %d", len(request.Evidence), maxChunksPerBlock)
		}
	}
}

func TestGenerateUsesCache(t *testing.T) {
	generator := newTestGenerator(t)
	cache := &fakeCache{}
	generator.SetCache(cache)
	pkg := testPackage("pkg-1", "doc-1", "ver-1", "hash-1", 1,
		testChunk("chunk-1", "parent-1", "Intro", "text", 0),
	)
	packages := []evidence.Package{pkg}
	if _, stats, err := generator.Generate(context.Background(), "", packages); err != nil || stats.Generated != 1 {
		t.Fatalf("first generate = %+v, %v", stats, err)
	}
	calls := generator.chatter.(*fakeChatter).calls
	if _, stats, err := generator.Generate(context.Background(), "", packages); err != nil || stats.Reused != 1 || stats.Generated != 0 {
		t.Fatalf("cached generate = %+v, %v", stats, err)
	}
	if generator.chatter.(*fakeChatter).calls != calls {
		t.Fatal("cache hit must not call the model")
	}

	corrupt := &fakeCache{getErr: nil}
	corrupt.data = map[projectioncache.Key][]byte{}
	for key := range cache.data {
		corrupt.data[key] = []byte("{invalid")
	}
	generator.SetCache(corrupt)
	if _, stats, err := generator.Generate(context.Background(), "", packages); err != nil || stats.Generated != 1 {
		t.Fatalf("corrupt cache generate = %+v, %v", stats, err)
	}

	generator.SetCache(&fakeCache{getErr: errors.New("cache down"), putErr: errors.New("cache write down")})
	if _, _, err := generator.Generate(context.Background(), "", packages); err != nil {
		t.Fatalf("cache errors must not fail generation: %v", err)
	}
}

func TestPageHashAndHelpers(t *testing.T) {
	generator := newTestGenerator(t)
	page := GeneratedPage{PageType: PageTypeDocument, Slug: "document-doc-1", Title: "a.md", Blocks: []models.WikiBlock{{HeadingPath: "H", Content: "text", SourceChunkIDs: []string{"chunk-1"}}}}
	pkg := testPackage("pkg-1", "doc-1", "ver-1", "hash-1", 1)
	first := generator.PageHash(page, []evidence.Package{pkg})
	if first != generator.PageHash(page, []evidence.Package{pkg}) {
		t.Fatal("PageHash must be deterministic")
	}
	other := generator.WithPromptVersion("other-version")
	if other.PageHash(page, []evidence.Package{pkg}) == first {
		t.Fatal("prompt version must change the page hash")
	}
	page.Title = "b.md"
	if generator.PageHash(page, []evidence.Package{pkg}) == first {
		t.Fatal("page content must change the hash")
	}

	if title := documentTitle(testPackage("pkg-2", "doc-2", "ver-2", "", 1, testChunk("c", "p", "", "text", 0)), []evidence.Chunk{testChunk("c", "p", "", "text", 0)}); title != "a.md" {
		t.Fatalf("documentTitle = %q", title)
	}
	long := documentTitle(testPackage("pkg-3", "document-with-a-very-long-id", "ver", "", 1), []evidence.Chunk{{ID: "c", Content: "text"}})
	if long != "Document document-wit" {
		t.Fatalf("long documentTitle = %q", long)
	}
	if short := documentTitle(testPackage("pkg-4", "doc", "ver", "", 1), []evidence.Chunk{{ID: "c", Content: "text"}}); short != "Document doc" {
		t.Fatalf("short documentTitle = %q", short)
	}
	if truncated := truncateRunes("hello [1]", 10); truncated != "hello" {
		t.Fatalf("truncateRunes citation = %q", truncated)
	}
	if truncated := truncateRunes("abcdefghij", 4); truncated != "abcd…" {
		t.Fatalf("truncateRunes limit = %q", truncated)
	}
}

func TestEvidenceAndSourceHelpers(t *testing.T) {
	chunks := []evidence.Chunk{
		{ID: "c2", ParentID: "p", Ordinal: 2, Content: "two"},
		{ID: "p", ParentID: "", Ordinal: 0, Content: "parent"},
		{ID: "c1", ParentID: "p", Ordinal: 1, Content: "one"},
		{ID: "empty", ParentID: "p", Ordinal: 3, Content: "  "},
	}
	filtered := evidenceChunks(evidence.Package{Chunks: chunks})
	if len(filtered) != 2 || filtered[0].ID != "c1" || filtered[1].ID != "c2" {
		t.Fatalf("evidenceChunks = %+v", filtered)
	}

	pkg := testPackage("pkg-1", "doc-1", "ver-1", "hash", 1,
		testChunk("c1", "p", "H1", "one", 0),
		testChunk("c2", "p", "H2", "two", 1),
	)
	blocks := []models.WikiBlock{
		{SourceChunkIDs: []string{"c2", "unknown"}},
		{SourceChunkIDs: []string{"c1", "c2"}},
	}
	sources := sourcesFromBlocks(blocks, []evidence.Package{pkg})
	if len(sources) != 2 || sources[0].ChunkID != "c1" || sources[1].ChunkID != "c2" {
		t.Fatalf("sources = %+v", sources)
	}
	if len(sourcesFromBlocks(nil, nil)) != 0 {
		t.Fatal("empty blocks must produce no sources")
	}
}
