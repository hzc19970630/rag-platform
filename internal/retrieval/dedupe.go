package retrieval

// dedupeWithinDocument collapses identical text that a single document
// contributed more than once: repeated pages, copied blocks, or a parent block
// whose text equals its only child. The caller sorts candidates first, so the
// highest ranked copy survives and the result is deterministic.
//
// Identical text in different documents is intentionally kept: a second file
// stating the same thing is corroboration the answer may cite, not noise.
func dedupeWithinDocument(candidates []Candidate) ([]Candidate, int) {
	seen := make(map[string]bool, len(candidates))
	kept := make([]Candidate, 0, len(candidates))
	dropped := 0
	for _, candidate := range candidates {
		if candidate.DocumentID == "" || candidate.ContentHash == "" {
			kept = append(kept, candidate)
			continue
		}
		key := candidate.DocumentID + "\x00" + candidate.ContentHash
		if seen[key] {
			dropped++
			continue
		}
		seen[key] = true
		kept = append(kept, candidate)
	}
	return kept, dropped
}

// capPerDocument keeps at most limit candidates per document. A single
// document with many near-matches can otherwise fill every evidence slot and
// starve the rest of the knowledge base. limit <= 0 disables the quota.
func capPerDocument(candidates []Candidate, limit int) ([]Candidate, int) {
	if limit <= 0 {
		return candidates, 0
	}
	counts := make(map[string]int, len(candidates))
	kept := make([]Candidate, 0, len(candidates))
	dropped := 0
	for _, candidate := range candidates {
		if candidate.DocumentID == "" {
			kept = append(kept, candidate)
			continue
		}
		if counts[candidate.DocumentID] >= limit {
			dropped++
			continue
		}
		counts[candidate.DocumentID]++
		kept = append(kept, candidate)
	}
	return kept, dropped
}
