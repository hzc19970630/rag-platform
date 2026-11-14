package ingestion

import (
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/formats"
)

func TestMimeForFilename(t *testing.T) {
	cases := map[string]string{
		"a.xlsx":    sheetMime,
		"B.PPTX":    slideMime,
		"page.html": "text/html",
		"page.htm":  "text/html",
		"a.docx":    wordMime,
		"a.pdf":     "application/pdf",
		"a.md":      "text/markdown",
		"a.txt":     "text/plain",
		"a.unknown": "text/plain",
	}
	for name, want := range cases {
		if got := formats.Default().MIMEForFilename(name); got != want {
			t.Fatalf("mimeForFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTerminalParserCode(t *testing.T) {
	for _, code := range []string{"unsupported_scanned_pdf", "unsupported_legacy_office", "unsupported_corrupt_document", "document_too_complex"} {
		if !terminalParserCode(code) {
			t.Fatalf("expected %q to be terminal", code)
		}
	}
	for _, code := range []string{"", "parser_error", "empty_document", "unsupported_mime_type"} {
		if terminalParserCode(code) {
			t.Fatalf("expected %q to be retryable", code)
		}
	}
}

func TestChunkDocumentsPreservesSearchMetadata(t *testing.T) {
	rows := []chunkRow{
		{ID: "parent-1", Content: "section body", ContentHash: "h1", HeadingPath: "H1", PageNumber: 2, SourcePath: "a.md", Ordinal: 0},
		{ID: "chunk-1", ParentID: "parent-1", Content: "child body", ContentHash: "h2", HeadingPath: "H1", PageNumber: 2, SourcePath: "a.md", Ordinal: 0},
	}
	documents := chunkDocuments(rows)
	if len(documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(documents))
	}
	if documents[0].Status != StatusParent || documents[0].ParentID != "" {
		t.Fatalf("parent document = %+v", documents[0])
	}
	child := documents[1]
	if child.Status != StatusChunked || child.ParentID != "parent-1" || child.Content != "child body" || child.ContentHash != "h2" {
		t.Fatalf("child document = %+v", child)
	}
	if child.HeadingPath != "H1" || child.PageNumber != 2 || child.SourcePath != "a.md" {
		t.Fatalf("child metadata = %+v", child)
	}
}
