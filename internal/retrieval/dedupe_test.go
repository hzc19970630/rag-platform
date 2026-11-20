package retrieval

import "testing"

func candidate(documentID, chunkID, hash string, score float64) Candidate {
	return Candidate{DocumentID: documentID, ChunkID: chunkID, ContentHash: hash, Score: score, Content: "text"}
}

func TestDedupeWithinDocumentCollapsesRepeatedText(t *testing.T) {
	input := []Candidate{
		candidate("doc-1", "chunk-a", "hash-1", 0.9),
		candidate("doc-1", "chunk-b", "hash-1", 0.8), // same page uploaded twice
		candidate("doc-1", "chunk-c", "hash-2", 0.7),
		candidate("doc-2", "chunk-d", "hash-1", 0.6), // other document, same text
	}
	kept, dropped := dedupeWithinDocument(input)
	if dropped != 1 {
		t.Fatalf("expected 1 duplicate, got %d", dropped)
	}
	if len(kept) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(kept))
	}
	if kept[0].ChunkID != "chunk-a" {
		t.Fatalf("the highest ranked copy must survive, got %s", kept[0].ChunkID)
	}
	if kept[2].ChunkID != "chunk-d" {
		t.Fatalf("identical text in another document must be kept, got %s", kept[2].ChunkID)
	}
}

func TestDedupeWithinDocumentKeepsUnknownIdentity(t *testing.T) {
	input := []Candidate{
		candidate("", "chunk-a", "hash-1", 0.9),
		candidate("", "chunk-b", "hash-1", 0.8),
		candidate("doc-1", "chunk-c", "", 0.7),
		candidate("doc-1", "chunk-d", "", 0.6),
	}
	kept, dropped := dedupeWithinDocument(input)
	if dropped != 0 || len(kept) != 4 {
		t.Fatalf("candidates without document or hash identity must be kept, kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestCapPerDocumentLimitsEachDocument(t *testing.T) {
	input := []Candidate{
		candidate("doc-1", "chunk-a", "hash-1", 0.9),
		candidate("doc-1", "chunk-b", "hash-2", 0.8),
		candidate("doc-1", "chunk-c", "hash-3", 0.7),
		candidate("doc-1", "chunk-d", "hash-4", 0.6),
		candidate("doc-2", "chunk-e", "hash-5", 0.5),
	}
	kept, dropped := capPerDocument(input, 2)
	if dropped != 2 {
		t.Fatalf("expected 2 over-quota candidates, got %d", dropped)
	}
	if len(kept) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(kept))
	}
	counts := map[string]int{}
	for _, item := range kept {
		counts[item.DocumentID]++
	}
	if counts["doc-1"] != 2 || counts["doc-2"] != 1 {
		t.Fatalf("unexpected per-document counts: %v", counts)
	}
	if kept[0].ChunkID != "chunk-a" || kept[1].ChunkID != "chunk-b" {
		t.Fatalf("the quota must keep the highest ranked candidates, got %s and %s", kept[0].ChunkID, kept[1].ChunkID)
	}
}

func TestCapPerDocumentZeroDisablesQuota(t *testing.T) {
	input := []Candidate{
		candidate("doc-1", "chunk-a", "hash-1", 0.9),
		candidate("doc-1", "chunk-b", "hash-2", 0.8),
	}
	kept, dropped := capPerDocument(input, 0)
	if dropped != 0 || len(kept) != 2 {
		t.Fatalf("a zero quota must keep everything, kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestDedupeAndCapAreDeterministicAfterSort(t *testing.T) {
	first := []Candidate{
		candidate("doc-1", "chunk-b", "hash-1", 0.8),
		candidate("doc-1", "chunk-a", "hash-1", 0.9),
		candidate("doc-1", "chunk-c", "hash-2", 0.7),
	}
	second := []Candidate{
		first[2],
		first[0],
		first[1],
	}
	run := func(input []Candidate) []string {
		SortCandidates(input)
		kept, _ := dedupeWithinDocument(input)
		kept, _ = capPerDocument(kept, 2)
		ids := make([]string, 0, len(kept))
		for _, item := range kept {
			ids = append(ids, item.ChunkID)
		}
		return ids
	}
	left, right := run(first), run(second)
	if len(left) != len(right) {
		t.Fatalf("sort order changed the result length: %v vs %v", left, right)
	}
	for index := range left {
		if left[index] != right[index] {
			t.Fatalf("sort order changed the result: %v vs %v", left, right)
		}
	}
}
