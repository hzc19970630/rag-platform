package configschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func rawMap(t *testing.T, values map[string]any) map[string]json.RawMessage {
	t.Helper()
	result := map[string]json.RawMessage{}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode %s: %v", key, err)
		}
		result[key] = encoded
	}
	return result
}

func TestDefaultsComeFromSchema(t *testing.T) {
	if got := IntDefault("top_k"); got != 8 {
		t.Fatalf("top_k default = %d", got)
	}
	if got := IntDefault("size"); got != 900 {
		t.Fatalf("chunk size default = %d", got)
	}
	if got := BoolDefault("vector_iterative_scan"); !got {
		t.Fatal("vector_iterative_scan should default to true")
	}
	if got := len(StringsDefault("split_markers")); got != 8 {
		t.Fatalf("split marker count = %d", got)
	}
	if got := len(FieldsFor(DomainRetrieval)); got != 16 {
		t.Fatalf("retrieval field count = %d", got)
	}
	if got := len(FieldsFor(DomainChunking)); got != 4 {
		t.Fatalf("chunking field count = %d", got)
	}
	topK, _ := FieldByKey(DomainRetrieval, "top_k")
	if topK.Impact != ImpactHot || topK.RebuildScope != RebuildNone || topK.Risk != RiskNormal {
		t.Fatalf("top_k impact metadata = %+v", topK)
	}
	size, _ := FieldByKey(DomainChunking, "size")
	if size.Impact != ImpactNextIngest || size.RebuildScope != RebuildRechunk || size.Risk != RiskNormal {
		t.Fatalf("size impact metadata = %+v", size)
	}
}

func TestValidateRetrievalAcceptsPartialTier(t *testing.T) {
	if err := Validate(DomainRetrieval, rawMap(t, map[string]any{"top_k": 12, "rerank_enabled": true})); err != nil {
		t.Fatalf("partial tier rejected: %v", err)
	}
}

func TestValidateRetrievalRejectsUnknownAndOutOfRange(t *testing.T) {
	err := Validate(DomainRetrieval, rawMap(t, map[string]any{"unknown_key": 1}))
	if err == nil || !strings.Contains(err.Error(), "unknown retrieval parameter") {
		t.Fatalf("unknown key error = %v", err)
	}
	err = Validate(DomainRetrieval, rawMap(t, map[string]any{"top_k": 900}))
	if err == nil || !strings.Contains(err.Error(), "top_k must be between 0 and 200, got 900") {
		t.Fatalf("range error = %v", err)
	}
	err = Validate(DomainRetrieval, rawMap(t, map[string]any{"vector_threshold": 1.5}))
	if err == nil || !strings.Contains(err.Error(), "vector_threshold must be between 0 and 1, got 1.5") {
		t.Fatalf("threshold error = %v", err)
	}
	err = Validate(DomainRetrieval, rawMap(t, map[string]any{"adjacent_expansion": -1}))
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative error = %v", err)
	}
}

func TestValidateChunkingRejectsRelations(t *testing.T) {
	if err := Validate(DomainChunking, rawMap(t, map[string]any{"size": 100, "overlap": 20})); err != nil {
		t.Fatalf("valid chunking rejected: %v", err)
	}
	err := Validate(DomainChunking, rawMap(t, map[string]any{"size": 100, "overlap": 100}))
	if err == nil || !strings.Contains(err.Error(), "overlap must be smaller than size") {
		t.Fatalf("overlap relation error = %v", err)
	}
	err = Validate(DomainChunking, rawMap(t, map[string]any{"size": 100, "min_size": 150}))
	if err == nil || !strings.Contains(err.Error(), "min_size must be smaller than size") {
		t.Fatalf("min_size relation error = %v", err)
	}
}

func TestResolveReportsSourceAndApplyMode(t *testing.T) {
	deployment := NewTier(map[string]any{"top_k": 10, "rrf_k": 60}, map[string]bool{"top_k": true})
	tenant := rawMap(t, map[string]any{"rrf_k": 30})
	knowledgeBase := rawMap(t, map[string]any{"top_k": 4})
	session := rawMap(t, map[string]any{"top_k": 2})

	states := Resolve(DomainRetrieval, deployment, tenant, knowledgeBase, session)
	if states["top_k"].Source != SourceSession || string(states["top_k"].Value) != "2" {
		t.Fatalf("top_k state = %+v", states["top_k"])
	}
	if states["rrf_k"].Source != SourceTenant || string(states["rrf_k"].Value) != "30" {
		t.Fatalf("rrf_k state = %+v", states["rrf_k"])
	}
	if states["keyword_top_k"].Source != SourceDefault {
		t.Fatalf("keyword_top_k state = %+v", states["keyword_top_k"])
	}
	if string(states["keyword_top_k"].Value) != "20" || string(states["keyword_top_k"].Default) != "20" {
		t.Fatalf("keyword_top_k values = %+v", states["keyword_top_k"])
	}
	if states["top_k"].ApplyMode != ApplyNextRetrieval {
		t.Fatalf("apply mode = %q", states["top_k"].ApplyMode)
	}

	chunking := Resolve(DomainChunking, NewTier(map[string]any{"size": 900}, nil), nil, nil, nil)
	if chunking["size"].Source != SourceDeployment {
		t.Fatalf("chunking source = %+v", chunking["size"])
	}
	if chunking["size"].ApplyMode != ApplyNextIngest {
		t.Fatalf("chunking apply mode = %q", chunking["size"].ApplyMode)
	}
}

func TestNewTierRespectsExplicitSet(t *testing.T) {
	tier := NewTier(map[string]any{"top_k": 8, "rrf_k": 60}, map[string]bool{"rrf_k": true})
	if !tier.Explicit["rrf_k"] {
		t.Fatal("rrf_k should be explicit")
	}
	if tier.Explicit["top_k"] {
		t.Fatal("top_k should not be explicit when it equals the default and was not stated")
	}
}
