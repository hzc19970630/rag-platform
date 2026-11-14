package ingestion

import (
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/formats"
)

const (
	wordMime  = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	sheetMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	slideMime = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

func TestSupportedMime(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		mimeType string
		want     string
		ok       bool
	}{
		{"pdf exact", "a.pdf", "application/pdf", "application/pdf", true},
		{"pdf empty mime", "a.pdf", "", "application/pdf", true},
		{"pdf wrong mime", "a.pdf", "image/png", "", false},
		{"docx parameters trimmed", "a.docx", "  APPLICATION/VND.OPENXMLFORMATS-OFFICEDOCUMENT.WORDPROCESSINGML.DOCUMENT ; charset=x", wordMime, true},
		{"xlsx exact", "a.xlsx", sheetMime, sheetMime, true},
		{"xlsx empty mime", "A.XLSX", "", sheetMime, true},
		{"xlsx octet stream rejected", "a.xlsx", "application/octet-stream", "", false},
		{"pptx exact", "a.pptx", slideMime, slideMime, true},
		{"pptx empty mime", "a.pptx", "", slideMime, true},
		{"pptx octet stream rejected", "a.pptx", "application/octet-stream", "", false},
		{"html empty mime", "page.html", "", "text/html", true},
		{"html text plain upload", "page.htm", "text/plain", "text/html", true},
		{"html xhtml upload", "page.HTML", "application/xhtml+xml", "text/html", true},
		{"html wrong mime", "page.html", "image/png", "", false},
		{"markdown", "a.md", "text/plain", "text/markdown", true},
		{"text", "a.txt", "text/plain", "text/plain", true},
		{"legacy xls rejected", "a.xls", "application/vnd.ms-excel", "", false},
		{"legacy ppt rejected", "a.ppt", "application/vnd.ms-powerpoint", "", false},
		{"image rejected", "a.png", "image/png", "", false},
		{"extensionless rejected", "a", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := formats.Default().Canonicalize(tc.filename, tc.mimeType)
			if ok != tc.ok {
				t.Fatalf("supportedMime(%q, %q) accepted = %v; want %v", tc.filename, tc.mimeType, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("supportedMime(%q, %q) = %q; want %q", tc.filename, tc.mimeType, got, tc.want)
			}
		})
	}
}
