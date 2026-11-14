package formats

import (
	"reflect"
	"testing"
)

func TestNamesMatchThePublishedFormatList(t *testing.T) {
	want := []string{"pdf-text", "docx", "xlsx", "pptx", "html", "markdown", "txt"}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected format order: %v", got)
	}
}

func TestCanonicalizeKeepsTheUploadPolicy(t *testing.T) {
	registry := Default()
	cases := []struct {
		filename string
		mime     string
		want     string
		ok       bool
	}{
		{"a.pdf", "", "application/pdf", true},
		{"a.pdf", "image/png", "", false},
		{"a.htm", "text/plain", "text/html", true},
		{"a.md", "text/x-markdown", "text/markdown", true},
		{"a.xlsx", "application/octet-stream", "", false},
		{"a", "", "", false},
	}
	for _, tc := range cases {
		got, ok := registry.Canonicalize(tc.filename, tc.mime)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("Canonicalize(%q, %q) = %q/%v, want %q/%v", tc.filename, tc.mime, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMIMEForFilenameDefaultsToText(t *testing.T) {
	registry := Default()
	if got := registry.MIMEForFilename("a.PPTX"); got != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		t.Fatalf("unexpected pptx mime %q", got)
	}
	if got := registry.MIMEForFilename("unknown.bin"); got != "text/plain" {
		t.Fatalf("unknown extension must fall back to text/plain, got %q", got)
	}
}
