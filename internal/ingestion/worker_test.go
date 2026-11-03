package ingestion

import "testing"

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
		if got := mimeForFilename(name); got != want {
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
