package ingestion

import (
	"strings"
)

// TableSplitter splits a table block without losing its header. Implementations
// repeat the header line in every child chunk so retrieval never sees a row
// without column names.
type TableSplitter interface {
	SplitTable(text string) []string
}

func (c RuneChunker) SplitTable(text string) []string {
	lines := cleanTableLines(text)
	if len(lines) == 0 {
		return nil
	}
	// A table block is already a bounded row window with its header (the
	// docreader emits at most MAX_BLOCK_CHARS). Keeping it as one retrieval
	// unit avoids 10x chunk inflation and keeps the header on every chunk.
	return []string{strings.Join(lines, "\n")}
}

func cleanTableText(value string) string {
	return strings.Join(cleanTableLines(value), "\n")
}

func cleanTableLines(value string) []string {
	raw := strings.Split(value, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		cleaned := cleanText(line)
		if cleaned != "" {
			lines = append(lines, cleaned)
		}
	}
	return lines
}
