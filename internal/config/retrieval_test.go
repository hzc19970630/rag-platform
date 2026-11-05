package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromProvidesM3Defaults(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	if cfg.PromptTemplateDir == "" {
		t.Fatalf("prompt template directory must have a default")
	}
	if cfg.RetrievalConfigFile == "" {
		t.Fatalf("retrieval config file must have a default")
	}
	if cfg.Chat.Source != "local" {
		t.Fatalf("local generation adapter must be the development default, got %q", cfg.Chat.Source)
	}
	if err := cfg.Retrieval.Validate(); err != nil {
		t.Fatalf("default retrieval parameters must validate: %v", err)
	}
	if cfg.Retrieval.RerankEnabled {
		t.Fatalf("rerank must stay off until an offline evaluation enables it")
	}
	if cfg.Retrieval.RRFK != 60 || cfg.Retrieval.ExpansionScoreFactor != 0.5 {
		t.Fatalf("fusion and expansion defaults must match the shipped file: %+v", cfg.Retrieval)
	}
	if cfg.Rerank.Source != "none" {
		t.Fatalf("rerank adapter must default to the pass-through, got %q", cfg.Rerank.Source)
	}
}

func TestRetrievalConfigRejectsOutOfRangeValues(t *testing.T) {
	base := defaultRetrievalConfig()
	bad := []func(*RetrievalConfig){
		func(r *RetrievalConfig) { r.TopK = 201 },
		func(r *RetrievalConfig) { r.VectorThreshold = 1.2 },
		func(r *RetrievalConfig) { r.KeywordThreshold = -1 },
		func(r *RetrievalConfig) { r.RerankThreshold = -11 },
		func(r *RetrievalConfig) { r.ContextTokenLimit = 0 },
		func(r *RetrievalConfig) { r.EmbeddingTopK = -1 },
		func(r *RetrievalConfig) { r.RRFK = 0 },
		func(r *RetrievalConfig) { r.RRFK = 201 },
		func(r *RetrievalConfig) { r.ExpansionScoreFactor = 1.5 },
		func(r *RetrievalConfig) { r.ExpansionScoreFactor = -0.1 },
	}
	for index, mutate := range bad {
		current := base
		mutate(&current)
		if err := current.Validate(); err == nil {
			t.Fatalf("case %d must be rejected", index)
		}
	}
}

func TestRerankConfigValidation(t *testing.T) {
	ok := RerankConfig{Source: "none"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the pass-through source must validate: %v", err)
	}
	if err := (RerankConfig{Source: "lexical"}).Validate(); err != nil {
		t.Fatalf("the offline source must validate: %v", err)
	}
	if err := (RerankConfig{Source: "remote"}).Validate(); err == nil {
		t.Fatalf("remote without a base URL must be rejected")
	}
	if err := (RerankConfig{Source: "remote", BaseURL: "https://rerank.example/v1"}).Validate(); err == nil {
		t.Fatalf("remote without a model must be rejected")
	}
	if err := (RerankConfig{Source: "remote", BaseURL: "not-a-url", Model: "m"}).Validate(); err == nil {
		t.Fatalf("a malformed base URL must be rejected")
	}
	if err := (RerankConfig{Source: "remote", BaseURL: "https://rerank.example/v1", Model: "qwen3.7-text-rerank"}).Validate(); err != nil {
		t.Fatalf("a complete remote configuration must validate: %v", err)
	}
	if err := (RerankConfig{Source: "model"}).Validate(); err == nil {
		t.Fatalf("an unknown source must be rejected")
	}
}

func TestApplyRetrievalDefaultsReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	contents := "retrieval:\n  top_k: 4\n  vector_threshold: 0.75\n  rerank_enabled: true\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	cfg.RetrievalConfigFile = path
	if err := applyRetrievalDefaults(&cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.Retrieval.TopK != 4 || cfg.Retrieval.VectorThreshold != 0.75 || !cfg.Retrieval.RerankEnabled {
		t.Fatalf("file values were not applied: %+v", cfg.Retrieval)
	}
	if cfg.Retrieval.ContextTokenLimit != defaultRetrievalConfig().ContextTokenLimit {
		t.Fatalf("keys the file omits must keep the code default, got %+v", cfg.Retrieval)
	}
	if cfg.Retrieval.KeywordTopK != defaultRetrievalConfig().KeywordTopK {
		t.Fatalf("partial files must not zero the remaining parameters: %+v", cfg.Retrieval)
	}
}

func TestApplyRetrievalDefaultsRejectsOutOfRangeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("retrieval:\n  top_k: 900\n  vector_threshold: 0.5\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	cfg.RetrievalConfigFile = path
	if err := applyRetrievalDefaults(&cfg); err == nil {
		t.Fatalf("expected an out-of-range value to be rejected")
	}
}

func TestApplyRetrievalDefaultsIgnoresMissingFile(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	cfg.RetrievalConfigFile = filepath.Join(t.TempDir(), "absent.yaml")
	if err := applyRetrievalDefaults(&cfg); err != nil {
		t.Fatalf("a missing file must keep the code defaults: %v", err)
	}
	if cfg.Retrieval.TopK == 0 {
		t.Fatalf("expected the default retrieval parameters to survive")
	}
}

func TestShippedRetrievalConfigIsValid(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	cfg.RetrievalConfigFile = filepath.Join("..", "..", "config", "config.yaml")
	if err := applyRetrievalDefaults(&cfg); err != nil {
		t.Fatalf("the shipped retrieval config must be valid: %v", err)
	}
	if cfg.Retrieval.TopK <= 0 || cfg.Retrieval.EmbeddingTopK <= 0 || cfg.Retrieval.KeywordTopK <= 0 {
		t.Fatalf("shipped config must set positive candidate depths: %+v", cfg.Retrieval)
	}
	if cfg.Retrieval.VectorEFSearch != 40 || cfg.Retrieval.VectorOversample != 1 || !cfg.Retrieval.VectorIterativeScan {
		t.Fatalf("shipped vector defaults must match the recall probe: %+v", cfg.Retrieval)
	}
}

func TestChatConfigValidation(t *testing.T) {
	if err := (ChatConfig{Source: "local"}).Validate("development"); err != nil {
		t.Fatalf("local adapter must validate in development: %v", err)
	}
	if err := (ChatConfig{Source: "local"}).Validate("production"); err == nil {
		t.Fatalf("local adapter must be rejected in production")
	}
	if err := (ChatConfig{Source: "remote"}).Validate("development"); err == nil {
		t.Fatalf("remote adapter must require an endpoint and model")
	}
	if err := (ChatConfig{Source: "remote", BaseURL: "https://api.example.com/v1", Model: "gpt"}).Validate("development"); err != nil {
		t.Fatalf("valid remote config must pass: %v", err)
	}
	if err := (ChatConfig{Source: "other"}).Validate("development"); err == nil {
		t.Fatalf("unknown source must be rejected")
	}
}

func TestValidateProductionRejectsLocalGenerationAndMissingPrompts(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL":        "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":          "redis:6379",
		"DOCREADER_GRPC_ADDR": "docreader:50051",
		"S3_ENDPOINT":         "minio:9000",
		"S3_BUCKET":           "bucket",
		"S3_ACCESS_KEY":       "key",
		"S3_SECRET_KEY":       "secret",
		"APP_ENV":             "production",
		"CHAT_SOURCE":         "local",
		// The M2 production guard still applies to the embedding adapter.
		"EMBEDDING_SOURCE":    "remote",
		"EMBEDDING_BASE_URL":  "https://api.example.com/v1",
		"LLM_EMBEDDING_MODEL": "bge-m3",
		// M5 requires a connector credential key whenever the API or worker starts.
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		// M4 requires OIDC in production.
		"OIDC_ENABLED":       "true",
		"OIDC_ISSUER":        "https://idp.example.com",
		"OIDC_CLIENT_ID":     "rag-platform",
		"OIDC_CLIENT_SECRET": "client-secret",
		"OIDC_REDIRECT_URL":  "https://rag.example.com/api/auth/callback",
	}
	if err := LoadFrom(lookupFrom(base)).Validate("api"); err == nil {
		t.Fatalf("production must reject the local generation adapter")
	}

	base["CHAT_SOURCE"] = "remote"
	base["CHAT_BASE_URL"] = "https://api.example.com/v1"
	base["LLM_CHAT_MODEL"] = "gpt-4o-mini"
	if err := LoadFrom(lookupFrom(base)).Validate("api"); err != nil {
		t.Fatalf("valid production config must pass: %v", err)
	}

	base["PROMPT_TEMPLATE_DIR"] = "  "
	if err := LoadFrom(lookupFrom(base)).Validate("api"); err == nil {
		t.Fatalf("an empty prompt template directory must be rejected")
	}
}

func TestValidateSurfacesConfigError(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{
		"DATABASE_URL":        "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":          "redis:6379",
		"DOCREADER_GRPC_ADDR": "docreader:50051",
		"S3_ENDPOINT":         "minio:9000",
		"S3_BUCKET":           "bucket",
		"S3_ACCESS_KEY":       "key",
		"S3_SECRET_KEY":       "secret",
	}))
	cfg.ConfigError = os.ErrInvalid
	if err := cfg.Validate("api"); err == nil {
		t.Fatalf("a recorded config file error must fail validation")
	}
}
