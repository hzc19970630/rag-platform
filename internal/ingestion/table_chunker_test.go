package ingestion

import (
	"strings"
	"testing"
)

func TestSplitTableKeepsWindowAndHeader(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 20})
	text := "customer | amount\nA | 100\nB | 200\nC | 300"
	chunks := chunker.SplitTable(text)
	if len(chunks) != 1 {
		t.Fatalf("table window should stay one chunk, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "customer | amount\n") {
		t.Fatalf("chunk lost the header: %q", chunks[0])
	}
	if !strings.Contains(chunks[0], "C | 300") {
		t.Fatalf("rows were lost: %v", chunks)
	}
}

func TestCleanTableTextPreservesRows(t *testing.T) {
	got := cleanTableText("  customer | amount \n\n A | 100 \n B | 200 \n")
	want := "customer | amount\nA | 100\nB | 200"
	if got != want {
		t.Fatalf("cleanTableText = %q, want %q", got, want)
	}
}
