package wiki

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/example/rag-platform/internal/contracts/evidence"
	"github.com/example/rag-platform/internal/platform/llm"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/prompts"
)

const (
	maxSectionsPerPage = 6
	maxChunksPerBlock  = 3
	maxOverviewBlocks  = 8

	PageTypeDocument = "document"
	PageTypeOverview = "overview"
)

type Generator struct {
	chatter       llm.Chatter
	template      string
	promptVersion string
}

type GeneratedPage struct {
	PageType   string
	Slug       string
	Title      string
	DocumentID string
	Blocks     []models.WikiBlock
	Sources    []models.WikiPageSource
	SourceHash string
}

func NewGenerator(chatter llm.Chatter, registry *prompts.Registry) (*Generator, error) {
	if chatter == nil {
		return nil, fmt.Errorf("wiki generation requires a chat adapter")
	}
	if registry == nil {
		return nil, fmt.Errorf("wiki generation requires prompt templates")
	}
	template, ok := registry.Raw(prompts.WikiGeneration)
	if !ok || strings.TrimSpace(template) == "" {
		return nil, fmt.Errorf("prompt template %q is required for wiki generation", prompts.WikiGeneration)
	}
	digest := sha256.Sum256([]byte(template))
	return &Generator{chatter: chatter, template: template, promptVersion: prompts.WikiGeneration + "/" + hex.EncodeToString(digest[:])[:12]}, nil
}

func (g *Generator) PromptVersion() string { return g.promptVersion }

// Model names the chat adapter that produced the page content.
func (g *Generator) Model() string { return g.chatter.Name() }

// WithPromptVersion returns a copy carrying another prompt version. It lets a
// caller evaluate a template change without rebuilding the chat adapter.
func (g *Generator) WithPromptVersion(version string) *Generator {
	clone := *g
	clone.promptVersion = version
	return &clone
}

// Generate builds document pages and one knowledge-base overview page. Every
// page block names the concrete EvidencePackage chunks it came from; the
// overview is the multi-source page that may span several documents.
func (g *Generator) Generate(ctx context.Context, kbTitle string, packages []evidence.Package) ([]GeneratedPage, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	sort.SliceStable(packages, func(i, j int) bool {
		return packages[i].DocumentID < packages[j].DocumentID
	})
	var pages []GeneratedPage
	var overview []models.WikiBlock
	overviewSeen := map[string]bool{}
	for index, pkg := range packages {
		chunks := evidenceChunks(pkg)
		if len(chunks) == 0 {
			continue
		}
		title := documentTitle(pkg, chunks)
		docPage, err := g.documentPage(ctx, pkg, title, chunks)
		if err != nil {
			return nil, err
		}
		pages = append(pages, docPage)
		if index < maxOverviewBlocks {
			block, err := g.overviewBlock(ctx, pkg, title, chunks)
			if err != nil {
				return nil, err
			}
			key := strings.Join(block.SourceChunkIDs, ",")
			if !overviewSeen[key] {
				overviewSeen[key] = true
				overview = append(overview, block)
			}
		}
	}
	if len(overview) > 0 {
		overviewTitle := strings.TrimSpace(kbTitle)
		if overviewTitle == "" {
			overviewTitle = "Knowledge base"
		}
		page := GeneratedPage{PageType: PageTypeOverview, Slug: "overview", Title: overviewTitle, Blocks: overview}
		page.Sources = sourcesFromBlocks(overview, packages)
		page.SourceHash = g.PageHash(page, packages)
		pages = append(pages, page)
	}
	return pages, nil
}

func (g *Generator) documentPage(ctx context.Context, pkg evidence.Package, title string, chunks []evidence.Chunk) (GeneratedPage, error) {
	sections := groupByHeading(chunks)
	if len(sections) > maxSectionsPerPage {
		sections = sections[:maxSectionsPerPage]
	}
	page := GeneratedPage{PageType: PageTypeDocument, Slug: "document-" + pkg.DocumentID, Title: title, DocumentID: pkg.DocumentID}
	for _, section := range sections {
		block, err := g.block(ctx, pkg, title, section.heading, section.chunks)
		if err != nil {
			return GeneratedPage{}, err
		}
		if block.Content == "" {
			continue
		}
		page.Blocks = append(page.Blocks, block)
	}
	page.Sources = sourcesFromBlocks(page.Blocks, []evidence.Package{pkg})
	page.SourceHash = g.PageHash(page, []evidence.Package{pkg})
	return page, nil
}

func (g *Generator) overviewBlock(ctx context.Context, pkg evidence.Package, title string, chunks []evidence.Chunk) (models.WikiBlock, error) {
	if len(chunks) > maxChunksPerBlock {
		chunks = chunks[:maxChunksPerBlock]
	}
	return g.block(ctx, pkg, title, title, chunks)
}

type evidenceSection struct {
	heading string
	chunks  []evidence.Chunk
}

func groupByHeading(chunks []evidence.Chunk) []evidenceSection {
	var sections []evidenceSection
	index := map[string]int{}
	for _, chunk := range chunks {
		heading := strings.TrimSpace(chunk.HeadingPath)
		key := heading
		position, ok := index[key]
		if !ok {
			position = len(sections)
			index[key] = position
			sections = append(sections, evidenceSection{heading: heading})
		}
		sections[position].chunks = append(sections[position].chunks, chunk)
	}
	return sections
}

func (g *Generator) block(ctx context.Context, pkg evidence.Package, pageTitle, heading string, chunks []evidence.Chunk) (models.WikiBlock, error) {
	selected := chunks
	if len(selected) > maxChunksPerBlock {
		selected = selected[:maxChunksPerBlock]
	}
	evidence := make([]llm.EvidenceItem, 0, len(selected))
	ids := make([]string, 0, len(selected))
	queryParts := []string{pageTitle}
	if strings.TrimSpace(heading) != "" {
		queryParts = append(queryParts, heading)
	}
	for index, chunk := range selected {
		evidence = append(evidence, llm.EvidenceItem{Index: index + 1, ChunkID: chunk.ID, HeadingPath: chunk.HeadingPath, Text: chunk.Content})
		ids = append(ids, chunk.ID)
		if index < 2 {
			queryParts = append(queryParts, chunk.Content)
		}
	}
	query := strings.TrimSpace(strings.Join(queryParts, " "))
	response, err := g.chatter.Complete(ctx, llm.ChatRequest{
		ContextTemplate: g.template,
		Query:           query,
		Evidence:        evidence,
		MaxTokens:       512,
	})
	if err != nil {
		return models.WikiBlock{}, fmt.Errorf("wiki generation call failed: %w", err)
	}
	content := strings.TrimSpace(response.Text)
	if content == "" {
		// Local/extractive adapters legitimately return nothing when no evidence
		// sentence matches. The page block must still be grounded, so fall back to
		// the leading evidence text instead of emitting an unsourced paragraph.
		content = fallbackContent(selected)
	}
	if content == "" {
		return models.WikiBlock{}, nil
	}
	return models.WikiBlock{HeadingPath: heading, Content: content, SourceChunkIDs: ids}, nil
}

func fallbackContent(chunks []evidence.Chunk) string {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		text := strings.TrimSpace(chunk.Content)
		if text == "" {
			continue
		}
		parts = append(parts, truncateRunes(text, 320))
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func sourcesFromBlocks(blocks []models.WikiBlock, packages []evidence.Package) []models.WikiPageSource {
	allowed := map[string]models.WikiPageSource{}
	for _, pkg := range packages {
		for _, chunk := range pkg.Chunks {
			allowed[chunk.ID] = models.WikiPageSource{
				TenantID: pkg.TenantID, KnowledgeBaseID: pkg.KnowledgeBaseID,
				DocumentID: pkg.DocumentID, DocumentVersionID: pkg.DocumentVersionID,
				ChunkID: chunk.ID, SourcePath: chunk.SourcePath, HeadingPath: chunk.HeadingPath, PageNumber: chunk.PageNumber,
			}
		}
	}
	seen := map[string]bool{}
	var sources []models.WikiPageSource
	for _, block := range blocks {
		for _, id := range block.SourceChunkIDs {
			source, ok := allowed[id]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			sources = append(sources, source)
		}
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].ChunkID < sources[j].ChunkID })
	return sources
}

func (g *Generator) PageHash(page GeneratedPage, packages []evidence.Package) string {
	digest := sha256.New()
	digest.Write([]byte(page.PageType))
	digest.Write([]byte{0})
	digest.Write([]byte(page.Slug))
	digest.Write([]byte{0})
	digest.Write([]byte(page.Title))
	digest.Write([]byte{0})
	// Model and prompt version are part of the page identity: changing either
	// must create a new version instead of silently keeping stale generated text.
	digest.Write([]byte(g.chatter.Name()))
	digest.Write([]byte{0})
	digest.Write([]byte(g.promptVersion))
	digest.Write([]byte{0})
	for _, pkg := range packages {
		digest.Write([]byte(pkg.ID))
		digest.Write([]byte{0})
		digest.Write([]byte(pkg.ContentHash))
		digest.Write([]byte{0})
		digest.Write([]byte(fmt.Sprint(pkg.ACLVersion)))
		digest.Write([]byte{0})
	}
	for _, block := range page.Blocks {
		digest.Write([]byte(block.HeadingPath))
		digest.Write([]byte{0})
		digest.Write([]byte(block.Content))
		digest.Write([]byte{0})
		for _, id := range block.SourceChunkIDs {
			digest.Write([]byte(id))
			digest.Write([]byte{0})
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func evidenceChunks(pkg evidence.Package) []evidence.Chunk {
	chunks := make([]evidence.Chunk, 0, len(pkg.Chunks))
	for _, chunk := range pkg.Chunks {
		// Parent blocks duplicate their children's text. Pages cite child chunks,
		// which are the unit the Evidence Index actually scored.
		if chunk.ParentID == "" || strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		chunks = append(chunks, chunk)
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Ordinal != chunks[j].Ordinal {
			return chunks[i].Ordinal < chunks[j].Ordinal
		}
		return chunks[i].ID < chunks[j].ID
	})
	return chunks
}

func documentTitle(pkg evidence.Package, chunks []evidence.Chunk) string {
	for _, chunk := range chunks {
		if name := strings.TrimSpace(path.Base(chunk.SourcePath)); name != "" && name != "." && name != "/" {
			return name
		}
	}
	if len(pkg.DocumentID) > 12 {
		return "Document " + pkg.DocumentID[:12]
	}
	return "Document " + pkg.DocumentID
}

var citationPattern = regexp.MustCompile(`\s*\[\d+\]\s*$`)

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(citationPattern.ReplaceAllString(value, ""))
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
