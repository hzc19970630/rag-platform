package ingestion

import (
	"strings"
	"testing"
)

func joinRunes(parts []string) string { return strings.Join(parts, "") }

func runeLen(value string) int { return len([]rune(value)) }

func TestRuneChunkerKeepsShortBlockIntact(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 100, Overlap: 10})
	block := strings.Repeat("短", 50)
	parts := chunker.Split(block)
	if len(parts) != 1 || parts[0] != block {
		t.Fatalf("expected single unchanged chunk, got %d parts", len(parts))
	}
}

func TestRuneChunkerRespectsSizeWithoutMarkers(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 100, Overlap: 20})
	block := strings.Repeat("a", 250)
	parts := chunker.Split(block)
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 parts for 250 runes at size 100, got %d", len(parts))
	}
	for index, part := range parts {
		if runeLen(part) > 100 {
			t.Fatalf("part %d exceeds size: %d", index, runeLen(part))
		}
	}
	// Overlapping windows must still cover the original content.
	if !strings.HasSuffix(block, parts[len(parts)-1]) {
		t.Fatalf("last part is not the tail of the block")
	}
}

func TestRuneChunkerPrefersMarkerBoundary(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 100, Overlap: 20, Markers: []string{"\n\n"}})
	paragraph := strings.Repeat("x", 40)
	block := paragraph + "\n\n" + strings.Repeat("y", 200)
	parts := chunker.Split(block)
	if len(parts) < 2 {
		t.Fatalf("expected marker to produce a boundary, got %d parts", len(parts))
	}
	if !strings.HasSuffix(parts[0], "\n\n") {
		t.Fatalf("first chunk should end at the marker, got %q tail", parts[0][len(parts[0])-4:])
	}
}

func TestRuneChunkerMergesSmallTail(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 100, Overlap: 0, MinSize: 40})
	block := strings.Repeat("a", 100) + strings.Repeat("b", 10)
	parts := chunker.Split(block)
	if len(parts) != 1 {
		t.Fatalf("expected the 10-rune tail to merge, got %d parts", len(parts))
	}
	if joinRunes(parts) != block {
		t.Fatalf("merge lost content")
	}
}

func TestRuneChunkerDropsOverlapWhenMarkerUsed(t *testing.T) {
	// With a marker at every boundary, whole paragraphs must not be duplicated
	// by the fixed overlap window.
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 50, Overlap: 20, Markers: []string{"\n\n"}})
	block := strings.Repeat("x", 40) + "\n\n" + strings.Repeat("y", 40) + "\n\n" + strings.Repeat("z", 40)
	parts := chunker.Split(block)
	if joined := joinRunes(parts); strings.Count(joined, "\n\n") != 2 {
		t.Fatalf("marker should appear exactly twice, got %d", strings.Count(joined, "\n\n"))
	}
}

func TestRuneChunkerEmptyInput(t *testing.T) {
	chunker := NewRuneChunker(RuneChunkerOptions{Size: 100})
	if parts := chunker.Split(""); parts != nil {
		t.Fatalf("expected nil for empty input")
	}
}
