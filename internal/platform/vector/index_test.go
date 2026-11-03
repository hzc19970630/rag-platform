package vector

import "testing"

func TestDimensionFromIndexDefParsesHalfvecDimension(t *testing.T) {
	definition := "CREATE INDEX chunks_vector_idx ON public.chunks USING hnsw (((embedding)::halfvec(768)) halfvec_cosine_ops) WITH (m='16', ef_construction='64') WHERE (embedding_dimension = 768)"
	dimension, err := dimensionFromIndexDef(definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dimension != 768 {
		t.Fatalf("expected 768, got %d", dimension)
	}
}

func TestDimensionFromIndexDefRejectsMissingHalfvec(t *testing.T) {
	if _, err := dimensionFromIndexDef("CREATE INDEX chunks_vector_idx ON public.chunks USING bm25 (id)"); err == nil {
		t.Fatalf("expected error for definition without halfvec dimension")
	}
}
