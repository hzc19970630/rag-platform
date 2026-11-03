package ingestion

import "strings"

// Chunker splits a normalized document block into child chunk contents. It is a
// port so the strategy (fixed window, marker aware, semantic) can change with
// configuration or implementation without touching the pipeline.
type Chunker interface {
	Split(block string) []string
}

// RuneChunkerOptions mirrors the configurable chunking policy.
type RuneChunkerOptions struct {
	// Size is the target maximum chunk length in runes.
	Size int
	// Overlap is how many runes consecutive chunks share.
	Overlap int
	// Markers are preferred split points. A chunk boundary is placed at the
	// last marker that still fits within Size, so sentences and paragraphs stay
	// intact when the document provides them.
	Markers []string
	// MinSize is the threshold under which a trailing chunk is merged into its
	// predecessor instead of being emitted on its own.
	MinSize int
}

// RuneChunker implements marker-aware fixed-window splitting. It prefers a
// natural boundary inside the window, falls back to a hard cut when a block has
// no usable marker, and drops the overlap when a marker boundary is used so
// chunks do not repeat whole paragraphs.
type RuneChunker struct {
	size     int
	overlap  int
	markers  []string
	minSize  int
	strategy string
}

func NewRuneChunker(opts RuneChunkerOptions) RuneChunker {
	if opts.Size <= 0 {
		opts.Size = 900
	}
	if opts.Overlap < 0 || opts.Overlap >= opts.Size {
		opts.Overlap = 0
	}
	if opts.MinSize < 0 || opts.MinSize >= opts.Size {
		opts.MinSize = 0
	}
	markers := make([]string, 0, len(opts.Markers))
	for _, marker := range opts.Markers {
		if marker != "" {
			markers = append(markers, marker)
		}
	}
	return RuneChunker{size: opts.Size, overlap: opts.Overlap, markers: markers, minSize: opts.MinSize, strategy: "marker_aware_window"}
}

// Strategy identifies the split policy so results can be attributed when
// chunking settings change between runs.
func (c RuneChunker) Strategy() string { return c.strategy }

func (c RuneChunker) Split(block string) []string {
	if block == "" {
		return nil
	}
	runes := []rune(block)
	if len(runes) <= c.size {
		return []string{block}
	}

	result := make([]string, 0, len(runes)/c.size+2)
	start := 0
	for start < len(runes) {
		end := start + c.size
		if end >= len(runes) {
			break
		}
		boundary := c.markerBoundary(runes, start, end)
		if boundary > start {
			result = append(result, string(runes[start:boundary]))
			// A natural boundary already provides continuity, so a full overlap
			// window would only duplicate whole paragraphs.
			start = boundary
			continue
		}
		result = append(result, string(runes[start:end]))
		start = end - c.overlap
	}
	if start < len(runes) {
		result = append(result, string(runes[start:]))
	}

	if c.minSize > 0 && len(result) > 1 {
		last := len(result) - 1
		if len([]rune(result[last])) < c.minSize {
			result[last-1] += result[last]
			result = result[:last]
		}
	}
	return result
}

// markerBoundary returns the last marker position inside (start, limit), so a
// chunk never exceeds the configured size. It reports start when no marker is
// available and the caller should fall back to a hard cut.
func (c RuneChunker) markerBoundary(runes []rune, start, limit int) int {
	best := 0
	window := runes[start:limit]
	for _, marker := range c.markers {
		needle := []rune(marker)
		if len(needle) == 0 || len(needle) >= len(window) {
			continue
		}
		index := strings.LastIndex(string(window), marker)
		if index <= 0 {
			continue
		}
		position := len([]rune(string(window)[:index])) + len(needle)
		if position > best {
			best = position
		}
	}
	return start + best
}
