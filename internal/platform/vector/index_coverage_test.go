package vector

import (
	"context"
	"strings"
	"testing"
)

func TestCheckPgvectorVersionBranches(t *testing.T) {
	tests := []struct {
		version string
		wantErr bool
	}{
		{version: "0.8.7", wantErr: false},
		{version: "1.0.0", wantErr: false},
		{version: "0.7", wantErr: false},
		{version: "0.6.9", wantErr: true},
		{version: "0.7.0-preview", wantErr: false},
		{version: "bad", wantErr: true},
		{version: "0.x", wantErr: true},
		{version: "", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			err := checkPgvectorVersion(test.version)
			if (err != nil) != test.wantErr {
				t.Fatalf("checkPgvectorVersion(%q) = %v, wantErr %v", test.version, err, test.wantErr)
			}
			if test.wantErr && err != nil && !strings.Contains(err.Error(), "pgvector") && !strings.Contains(err.Error(), "parse") {
				t.Fatalf("unexpected error text: %v", err)
			}
		})
	}
}

func TestParseAndNextChunksIndexName(t *testing.T) {
	tests := []struct {
		name          string
		wantDimension int
		wantVersion   int
		wantOK        bool
	}{
		{name: "chunks_vector_idx_8_v2", wantDimension: 8, wantVersion: 2, wantOK: true},
		{name: "chunks_vector_idx", wantOK: false},
		{name: "chunks_vector_idx_x_v1", wantOK: false},
		{name: "chunks_vector_idx_8_vx", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dimension, version, ok := parseChunksIndexName(test.name)
			if ok != test.wantOK || dimension != test.wantDimension || version != test.wantVersion {
				t.Fatalf("parseChunksIndexName(%q) = %d, %d, %v", test.name, dimension, version, ok)
			}
		})
	}

	if got := nextChunksIndexName(nil, 8); got != "chunks_vector_idx_8_v1" {
		t.Fatalf("nextChunksIndexName(empty) = %q", got)
	}
	records := []indexRecord{
		{Name: "chunks_vector_idx_8_v1", Valid: true},
		{Name: "chunks_vector_idx_8_v4", Valid: true},
		{Name: "chunks_vector_idx_1024_v9", Valid: true},
		{Name: "chunks_vector_idx_8_v7", Valid: false},
	}
	if got := nextChunksIndexName(records, 8); got != "chunks_vector_idx_8_v5" {
		t.Fatalf("nextChunksIndexName = %q, want chunks_vector_idx_8_v5", got)
	}
}

func TestIndexCoversDimension(t *testing.T) {
	definition := indexDefinition("idx", 8)
	if !indexCoversDimension(definition, 8) {
		t.Fatal("definition must cover its own dimension")
	}
	if indexCoversDimension(definition, 16) {
		t.Fatal("definition must not cover another dimension")
	}
	if indexCoversDimension("CREATE INDEX idx ON chunks (status)", 8) {
		t.Fatal("definition without a dimension predicate must not match")
	}
}

func TestPruneChunksIndexes(t *testing.T) {
	records := []indexRecord{
		{Name: "chunks_vector_idx_8_v2", Valid: true, Definition: indexDefinition("chunks_vector_idx_8_v2", 8)},
		{Name: "chunks_vector_idx_8_v1", Valid: true, Definition: indexDefinition("chunks_vector_idx_8_v1", 8)},
		{Name: "chunks_vector_idx_1024_v1", Valid: true, Definition: indexDefinition("chunks_vector_idx_1024_v1", 1024)},
		{Name: "chunks_vector_idx_512_v1", Valid: true, Definition: indexDefinition("chunks_vector_idx_512_v1", 512)},
		{Name: "chunks_vector_idx_8_v3", Valid: false, Definition: indexDefinition("chunks_vector_idx_8_v3", 8)},
	}
	session := &fakeIndexSession{}
	dropped, err := pruneChunksIndexes(context.Background(), session, records, "chunks_vector_idx_8_v2", 8, map[int]bool{1024: true})
	if err != nil {
		t.Fatalf("pruneChunksIndexes: %v", err)
	}
	if len(dropped) != 2 || dropped[0] != "chunks_vector_idx_8_v1" || dropped[1] != "chunks_vector_idx_512_v1" {
		t.Fatalf("dropped = %v", dropped)
	}
	if len(session.dropCalls) != 2 {
		t.Fatalf("drop calls = %v", session.dropCalls)
	}

	names := droppableChunksIndexNames(records, "chunks_vector_idx_8_v2", 8, map[int]bool{1024: true})
	if len(names) != 2 || names[0] != "chunks_vector_idx_8_v1" || names[1] != "chunks_vector_idx_512_v1" {
		t.Fatalf("droppable names = %v", names)
	}

	broken := []indexRecord{{Name: "broken", Valid: true, Definition: "CREATE INDEX broken"}}
	if _, err := pruneChunksIndexes(context.Background(), &fakeIndexSession{}, broken, "keep", 8, nil); err == nil {
		t.Fatal("pruneChunksIndexes must surface an unparsable definition")
	}
	if names := droppableChunksIndexNames(broken, "keep", 8, nil); len(names) != 0 {
		t.Fatalf("droppable names for broken definition = %v", names)
	}
}
