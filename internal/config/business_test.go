package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetrievalTierMarksOnlyStatedYAMLKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("retrieval:\n  top_k: 12\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := Config{Retrieval: defaultRetrievalConfig(), RetrievalConfigFile: path}
	if err := applyRetrievalDefaults(&cfg); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if cfg.Retrieval.TopK != 12 {
		t.Fatalf("top_k = %d", cfg.Retrieval.TopK)
	}
	tier := cfg.RetrievalTier()
	if !tier.Explicit["top_k"] {
		t.Fatal("top_k should be explicit")
	}
	if tier.Explicit["rrf_k"] {
		t.Fatal("rrf_k was not stated and must stay a schema default")
	}
	if string(tier.Values["top_k"]) != "12" {
		t.Fatalf("deployment value = %s", tier.Values["top_k"])
	}
}

func TestRetrievalFileRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("retrieval:\n  typo_key: 1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := Config{Retrieval: defaultRetrievalConfig(), RetrievalConfigFile: path}
	if err := applyRetrievalDefaults(&cfg); err == nil {
		t.Fatal("expected unknown retrieval key to fail startup")
	}
}

func TestChunkingTierMarksOnlyExplicitEnvKeys(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{"CHUNK_SIZE": "640"}))
	tier := cfg.ChunkingTier()
	if !tier.Explicit["size"] {
		t.Fatal("size should be explicit")
	}
	if tier.Explicit["overlap"] {
		t.Fatal("overlap was not set and must stay a schema default")
	}
	if cfg.Chunk.Size != 640 {
		t.Fatalf("chunk size = %d", cfg.Chunk.Size)
	}
}
