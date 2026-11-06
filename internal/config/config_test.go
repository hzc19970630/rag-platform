package config

import (
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) Lookup {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	if cfg.APIAddr != ":8080" {
		t.Fatalf("APIAddr default: %q", cfg.APIAddr)
	}
	if cfg.MetricsAddr != "" {
		t.Fatalf("MetricsAddr must default to disabled, got %q", cfg.MetricsAddr)
	}
	if cfg.AppEnv != "development" {
		t.Fatalf("AppEnv default: %q", cfg.AppEnv)
	}
	if cfg.RedisAddr != "redis:6379" || cfg.DocreaderAddr != "docreader:50051" {
		t.Fatalf("service defaults: %+v", cfg)
	}
	if cfg.S3UseTLS {
		t.Fatalf("S3UseTLS should default to false")
	}
	if !cfg.ParserSandbox {
		t.Fatalf("ParserSandbox should default to true")
	}
	if cfg.MaxFileBytes != 100*1024*1024 {
		t.Fatalf("MaxFileBytes default: %d", cfg.MaxFileBytes)
	}
	if cfg.TaskTimeoutSeconds != 120 {
		t.Fatalf("TaskTimeoutSeconds default: %d", cfg.TaskTimeoutSeconds)
	}
	wantFormats := map[string]bool{"pdf-text": true, "docx": true, "xlsx": true, "pptx": true, "markdown": true, "txt": true, "html": true}
	if len(cfg.SupportedFormats) != len(wantFormats) {
		t.Fatalf("SupportedFormats default: %v", cfg.SupportedFormats)
	}
	for _, format := range cfg.SupportedFormats {
		if !wantFormats[format] {
			t.Fatalf("unexpected supported format %q in %v", format, cfg.SupportedFormats)
		}
	}
}

func TestLoadFromReadsOverridesAndIgnoresEmpty(t *testing.T) {
	env := map[string]string{
		"API_ADDR":                       ":9090",
		"APP_LOG_LEVEL":                  "debug",
		"S3_USE_TLS":                     "TRUE",
		"DOCREADER_MAX_FILE_MB":          "20",
		"DOCREADER_TASK_TIMEOUT_SECONDS": "45",
		"REDIS_ADDR":                     "",
	}
	cfg := LoadFrom(lookupFrom(env))
	if cfg.APIAddr != ":9090" || cfg.LogLevel != "debug" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if !cfg.S3UseTLS {
		t.Fatalf("S3_USE_TLS should be case-insensitive")
	}
	if cfg.MaxFileBytes != 20*1024*1024 || cfg.TaskTimeoutSeconds != 45 {
		t.Fatalf("numeric overrides not applied: %+v", cfg)
	}
	if cfg.RedisAddr != "redis:6379" {
		t.Fatalf("empty value must fall back to default, got %q", cfg.RedisAddr)
	}
}

// productionAPIEnv satisfies every production guard so a test can mutate one
// value and assert the specific rejection.
func productionAPIEnv() map[string]string {
	return map[string]string{
		"APP_ENV":                  "production",
		"DATABASE_URL":             "postgres://rag_app:app-secret@localhost:5432/rag",
		"MAINTENANCE_DATABASE_URL": "postgres://rag_maintenance:maintenance-secret@localhost:5432/rag",
		"ADMIN_DATABASE_URL":       "postgres://rag_platform:owner-secret@localhost:5432/rag",
		"TENANT_RLS_MODE":          "enforce",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "prod-access-key",
		"S3_SECRET_KEY":            "prod-secret-key",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"EMBEDDING_SOURCE":         "remote",
		"LLM_EMBEDDING_MODEL":      "bge-m3",
		"EMBEDDING_BASE_URL":       "https://api.example.com/v1",
		"CHAT_SOURCE":              "remote",
		"CHAT_BASE_URL":            "https://api.example.com/v1",
		"LLM_CHAT_MODEL":           "gpt-4o-mini",
		"JWT_SIGNING_KEYS":         "prod:0123456789abcdef0123456789abcdef",
		"API_KEY_PEPPER":           "production-api-key-pepper",
		"OIDC_ENABLED":             "true",
		"OIDC_ISSUER":              "https://idp.example.com",
		"OIDC_CLIENT_ID":           "rag-platform",
		"OIDC_CLIENT_SECRET":       "client-secret",
		"OIDC_REDIRECT_URL":        "https://rag.example.com/api/auth/callback",
	}
}

func TestValidateRequiresInfrastructureForAPI(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{"DATABASE_URL": ""}))
	if err := cfg.Validate("api"); err == nil {
		t.Fatalf("expected api validation to fail without DATABASE_URL")
	}

	valid := map[string]string{
		"DATABASE_URL":             "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	cfg = LoadFrom(lookupFrom(valid))
	if err := cfg.Validate("api"); err != nil {
		t.Fatalf("expected api validation to pass, got %v", err)
	}
}

func TestValidateEnforceRequiresSplitRoles(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL":             "postgres://rag_app:app@localhost:5432/rag",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TENANT_RLS_MODE":          "enforce",
	}
	with := func(key, value string) Config {
		env := make(map[string]string, len(base)+1)
		for k, v := range base {
			env[k] = v
		}
		env[key] = value
		return LoadFrom(lookupFrom(env))
	}

	if err := with("REDIS_ADDR", "redis:6379").Validate("api"); err == nil || !strings.Contains(err.Error(), "MAINTENANCE_DATABASE_URL") {
		t.Fatalf("expected a missing maintenance URL to be rejected, got %v", err)
	}
	maintenance := "postgres://rag_maintenance:maintenance@localhost:5432/rag"
	if err := with("MAINTENANCE_DATABASE_URL", maintenance).Validate("api"); err != nil {
		t.Fatalf("expected enforce api config to pass, got %v", err)
	}
	if err := with("MAINTENANCE_DATABASE_URL", maintenance).Validate("worker"); err != nil {
		t.Fatalf("expected enforce worker config to pass, got %v", err)
	}
	sameRole := with("MAINTENANCE_DATABASE_URL", "postgres://rag_app:maintenance@localhost:5432/rag")
	if err := sameRole.Validate("api"); err == nil || !strings.Contains(err.Error(), "different role") {
		t.Fatalf("expected the maintenance role to differ from rag_app, got %v", err)
	}
	owner := with("MAINTENANCE_DATABASE_URL", maintenance)
	owner.DatabaseURL = "postgres://rag_platform:owner@localhost:5432/rag"
	if err := owner.Validate("api"); err == nil || !strings.Contains(err.Error(), "rag_app") {
		t.Fatalf("expected an owner runtime role to be rejected, got %v", err)
	}
	if err := with("MAINTENANCE_DATABASE_URL", maintenance).Validate("migrate"); err == nil || !strings.Contains(err.Error(), "ADMIN_DATABASE_URL") {
		t.Fatalf("expected a missing admin URL to be rejected, got %v", err)
	}
	admin := "postgres://rag_platform:owner@localhost:5432/rag"
	if err := with("ADMIN_DATABASE_URL", admin).Validate("migrate"); err != nil {
		t.Fatalf("expected enforce migrate config to pass, got %v", err)
	}
	shadow := LoadFrom(lookupFrom(map[string]string{"DATABASE_URL": "postgres://user:pass@localhost:5432/rag"}))
	if shadow.AdminDBURL() != shadow.DatabaseURL {
		t.Fatalf("shadow AdminDBURL must fall back to DATABASE_URL, got %q", shadow.AdminDBURL())
	}
}

func TestValidateMetricsAddr(t *testing.T) {
	valid := map[string]string{
		"DATABASE_URL":             "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}

	withMetrics := func(address string) Config {
		env := make(map[string]string, len(valid)+1)
		for key, value := range valid {
			env[key] = value
		}
		env["METRICS_ADDR"] = address
		return LoadFrom(lookupFrom(env))
	}

	if err := withMetrics("127.0.0.1:9091").Validate("api"); err != nil {
		t.Fatalf("valid METRICS_ADDR rejected: %v", err)
	}
	for _, address := range []string{"9091", "127.0.0.1", "127.0.0.1:not-a-port"} {
		if err := withMetrics(address).Validate("api"); err == nil {
			t.Fatalf("expected METRICS_ADDR %q to be rejected", address)
		}
	}

	env := make(map[string]string, len(valid)+2)
	for key, value := range valid {
		env[key] = value
	}
	env["API_ADDR"] = ":9091"
	env["METRICS_ADDR"] = ":9091"
	if err := LoadFrom(lookupFrom(env)).Validate("api"); err == nil {
		t.Fatalf("METRICS_ADDR must not reuse API_ADDR")
	}
}

func TestValidateRejectsBadDatabaseURL(t *testing.T) {
	valid := map[string]string{
		"DATABASE_URL":             "not-a-url",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	cfg := LoadFrom(lookupFrom(valid))
	if err := cfg.Validate("api"); err == nil {
		t.Fatalf("expected invalid DATABASE_URL to be rejected")
	}
}

func TestValidateProductionGuards(t *testing.T) {
	base := productionAPIEnv()

	noSandbox := map[string]string{}
	for key, value := range base {
		noSandbox[key] = value
	}
	noSandbox["PARSER_SANDBOX_ENABLED"] = "false"
	if err := LoadFrom(lookupFrom(noSandbox)).Validate("api"); err == nil {
		t.Fatalf("production must require parser sandbox")
	}

	localEmbedding := map[string]string{}
	for key, value := range base {
		localEmbedding[key] = value
	}
	localEmbedding["EMBEDDING_SOURCE"] = "local"
	if err := LoadFrom(lookupFrom(localEmbedding)).Validate("api"); err == nil {
		t.Fatalf("production must reject local embedding adapter")
	}

	if err := LoadFrom(lookupFrom(base)).Validate("api"); err != nil {
		t.Fatalf("valid production config must pass: %v", err)
	}
}

func TestProductionRejectsRepositoryDefaultSecrets(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"jwt-example-placeholder", "JWT_SIGNING_KEYS", "prod:replace-with-a-random-secret-at-least-32-bytes", "JWT_SIGNING_KEYS"},
		{"pepper-compiled-default", "API_KEY_PEPPER", "local-api-key-pepper-change-me", "API_KEY_PEPPER"},
		{"pepper-example-placeholder", "API_KEY_PEPPER", "replace-with-a-random-api-key-pepper", "API_KEY_PEPPER"},
		{"s3-access-default", "S3_ACCESS_KEY", "rag-local", "S3_ACCESS_KEY"},
		{"s3-secret-default", "S3_SECRET_KEY", "change-me-local-only", "S3_SECRET_KEY"},
		{"database-password-default", "DATABASE_URL", "postgres://rag_app:change-me-local-only@localhost:5432/rag", "DATABASE_URL"},
		{"maintenance-password-default", "MAINTENANCE_DATABASE_URL", "postgres://rag_maintenance:change-me-maintenance@localhost:5432/rag", "MAINTENANCE_DATABASE_URL"},
		{"admin-password-default", "ADMIN_DATABASE_URL", "postgres://rag_platform:change-me-local-only@localhost:5432/rag", "ADMIN_DATABASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := productionAPIEnv()
			env[tc.key] = tc.value
			err := LoadFrom(lookupFrom(env)).Validate("api")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestProductionRequiresExplicitSigningKeys(t *testing.T) {
	env := productionAPIEnv()
	delete(env, "JWT_SIGNING_KEYS")
	err := LoadFrom(lookupFrom(env)).Validate("api")
	if err == nil || !strings.Contains(err.Error(), "JWT_SIGNING_KEYS must be set explicitly") {
		t.Fatalf("expected the development JWT fallback to be rejected, got %v", err)
	}

	env = productionAPIEnv()
	cfg := LoadFrom(lookupFrom(env))
	if !cfg.Auth.SigningKeysExplicit {
		t.Fatalf("explicit JWT_SIGNING_KEYS must be recorded")
	}
	if err := cfg.Validate("api"); err != nil {
		t.Fatalf("explicit production signing keys must pass, got %v", err)
	}
}

func TestDevelopmentAllowsRepositoryDefaultSecrets(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{
		"DATABASE_URL":             "postgres://user:change-me-local-only@localhost:5432/rag",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}))
	if cfg.Auth.SigningKeysExplicit {
		t.Fatalf("development fallback must not be recorded as explicit")
	}
	if err := cfg.Validate("api"); err != nil {
		t.Fatalf("development defaults must keep working, got %v", err)
	}
}

func TestProductionRequiresEnforceRLS(t *testing.T) {
	for _, mode := range []string{"", "shadow", "off"} {
		env := productionAPIEnv()
		if mode == "" {
			delete(env, "TENANT_RLS_MODE")
		} else {
			env["TENANT_RLS_MODE"] = mode
		}
		err := LoadFrom(lookupFrom(env)).Validate("api")
		if err == nil || !strings.Contains(err.Error(), "TENANT_RLS_MODE must be enforce") {
			t.Fatalf("production mode %q must require enforce, got %v", mode, err)
		}
	}

	env := productionAPIEnv()
	delete(env, "MAINTENANCE_DATABASE_URL")
	if err := LoadFrom(lookupFrom(env)).Validate("api"); err == nil || !strings.Contains(err.Error(), "MAINTENANCE_DATABASE_URL") {
		t.Fatalf("production enforce must require the maintenance URL, got %v", err)
	}

	env = productionAPIEnv()
	env["MAINTENANCE_DATABASE_URL"] = env["DATABASE_URL"]
	if err := LoadFrom(lookupFrom(env)).Validate("api"); err == nil || !strings.Contains(err.Error(), "different role") {
		t.Fatalf("production enforce must reject a shared runtime/maintenance role, got %v", err)
	}

	if err := LoadFrom(lookupFrom(productionAPIEnv())).Validate("api"); err != nil {
		t.Fatalf("valid production enforce config must pass, got %v", err)
	}
}

func TestModelRegistryConfig(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{
		"CHAT_MODELS":                      " chat-a , chat-b@2025-01 ",
		"RERANK_MODELS":                    "rerank-a@v2",
		"TENANT_MODEL_REQUESTS_PER_MINUTE": "30",
	}))
	if len(cfg.Chat.Models) != 2 || cfg.Chat.Models[0] != "chat-a" || cfg.Chat.Models[1] != "chat-b@2025-01" {
		t.Fatalf("CHAT_MODELS parsed as %v", cfg.Chat.Models)
	}
	if len(cfg.Rerank.Models) != 1 || cfg.Rerank.Models[0] != "rerank-a@v2" {
		t.Fatalf("RERANK_MODELS parsed as %v", cfg.Rerank.Models)
	}
	if cfg.Limits.TenantModelRequestsPerMinute != 30 {
		t.Fatalf("per-tenant model budget = %d", cfg.Limits.TenantModelRequestsPerMinute)
	}
	if err := cfg.Chat.Validate("test"); err != nil {
		t.Fatalf("valid chat model config must pass: %v", err)
	}
	if err := cfg.Rerank.Validate(); err != nil {
		t.Fatalf("valid rerank model config must pass: %v", err)
	}

	// A version separator without a version is a configuration error, not a
	// model literally named "chat-a@".
	broken := LoadFrom(lookupFrom(map[string]string{"CHAT_MODELS": "chat-a@"}))
	if err := broken.Chat.Validate("test"); err == nil || !strings.Contains(err.Error(), "CHAT_MODELS") {
		t.Fatalf("expected CHAT_MODELS validation error, got %v", err)
	}
	brokenRerank := LoadFrom(lookupFrom(map[string]string{"RERANK_MODELS": "@v1"}))
	if err := brokenRerank.Rerank.Validate(); err == nil || !strings.Contains(err.Error(), "RERANK_MODELS") {
		t.Fatalf("expected RERANK_MODELS validation error, got %v", err)
	}

	negative := LoadFrom(lookupFrom(map[string]string{"TENANT_MODEL_REQUESTS_PER_MINUTE": "-1"}))
	if negative.ConfigError == nil || !strings.Contains(negative.ConfigError.Error(), "TENANT_MODEL_REQUESTS_PER_MINUTE") {
		t.Fatalf("expected negative model budget rejection, got %v", negative.ConfigError)
	}
}

func TestProjectionIncrementalKillSwitch(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	if !cfg.Projection.IncrementalEnabled {
		t.Fatalf("incremental projection must default to enabled")
	}
	cfg = LoadFrom(lookupFrom(map[string]string{"PROJECTION_INCREMENTAL_ENABLED": "false"}))
	if cfg.Projection.IncrementalEnabled {
		t.Fatalf("PROJECTION_INCREMENTAL_ENABLED=false must disable the cache")
	}
}

func TestConnectorConfigDefaultsAndOverrides(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	if cfg.Connector.EncryptionKey != "" {
		t.Fatalf("connector key must have no default")
	}
	if cfg.Connector.AllowPrivateNetworks {
		t.Fatalf("private networks must default to false")
	}
	if cfg.Connector.RequestTimeoutSec != 30 || cfg.Connector.MaxManifestBytes != 2*1024*1024 || cfg.Connector.DefaultRatePerSecond != 2 {
		t.Fatalf("unexpected connector defaults: %+v", cfg.Connector)
	}

	overrides := map[string]string{
		"CONNECTOR_ENCRYPTION_KEY":          "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"CONNECTOR_ALLOW_PRIVATE_NETWORKS":  "true",
		"CONNECTOR_REQUEST_TIMEOUT_SECONDS": "5",
		"CONNECTOR_MAX_MANIFEST_BYTES":      "1048576",
		"CONNECTOR_REQUESTS_PER_SECOND":     "10",
	}
	cfg = LoadFrom(lookupFrom(overrides))
	if !cfg.Connector.AllowPrivateNetworks || cfg.Connector.RequestTimeoutSec != 5 ||
		cfg.Connector.MaxManifestBytes != 1048576 || cfg.Connector.DefaultRatePerSecond != 10 {
		t.Fatalf("connector overrides not applied: %+v", cfg.Connector)
	}
}

func TestConnectorConfigValidation(t *testing.T) {
	valid := ConnectorConfig{
		EncryptionKey:     "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		RequestTimeoutSec: 30, MaxManifestBytes: 1 << 20, DefaultRatePerSecond: 2,
	}
	if err := valid.Validate("development"); err != nil {
		t.Fatalf("valid connector config rejected: %v", err)
	}

	missingKey := valid
	missingKey.EncryptionKey = " "
	if err := missingKey.Validate("development"); err == nil {
		t.Fatalf("connector key must be required")
	}

	productionPrivate := valid
	productionPrivate.AllowPrivateNetworks = true
	if err := productionPrivate.Validate("production"); err == nil {
		t.Fatalf("production must reject private connector networks")
	}
	if err := productionPrivate.Validate("development"); err != nil {
		t.Fatalf("development must allow private connector networks: %v", err)
	}

	badTimeout := valid
	badTimeout.RequestTimeoutSec = 0
	if err := badTimeout.Validate("development"); err == nil {
		t.Fatalf("timeout must be positive")
	}

	badManifest := valid
	badManifest.MaxManifestBytes = 0
	if err := badManifest.Validate("development"); err == nil {
		t.Fatalf("manifest size limit must be positive")
	}

	for _, rate := range []float64{0, 51, -1} {
		badRate := valid
		badRate.DefaultRatePerSecond = rate
		if err := badRate.Validate("development"); err == nil {
			t.Fatalf("rate %v must be rejected", rate)
		}
	}

	keyring := valid
	keyring.EncryptionKey = ""
	keyring.EncryptionKeys = "k1:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	if err := keyring.Validate("development"); err == nil || !strings.Contains(err.Error(), "CONNECTOR_ENCRYPTION_ACTIVE_KID") {
		t.Fatalf("keyring without an active kid must be rejected, got %v", err)
	}
	keyring.ActiveKeyID = "k1"
	if err := keyring.Validate("development"); err != nil {
		t.Fatalf("keyring with an active kid must pass: %v", err)
	}
	noKeys := valid
	noKeys.EncryptionKey = ""
	if err := noKeys.Validate("development"); err == nil || !strings.Contains(err.Error(), "CONNECTOR_ENCRYPTION_KEYS") {
		t.Fatalf("missing connector keys must be rejected, got %v", err)
	}
}

func TestValidateEmbedctlRequiresDatabaseAndEmbedding(t *testing.T) {
	if err := LoadFrom(lookupFrom(map[string]string{"EMBEDDING_DIMENSION": "768"})).Validate("embedctl"); err == nil {
		t.Fatalf("embedctl must require DATABASE_URL")
	}

	// The rebuild command validates the same embedding contract as the worker,
	// so a remote source without an endpoint is rejected here too.
	remoteWithoutEndpoint := map[string]string{
		"DATABASE_URL":        "postgres://user:pass@localhost:5432/rag",
		"EMBEDDING_SOURCE":    "remote",
		"LLM_EMBEDDING_MODEL": "bge-m3",
	}
	if err := LoadFrom(lookupFrom(remoteWithoutEndpoint)).Validate("embedctl"); err == nil {
		t.Fatalf("embedctl must reject a remote source without EMBEDDING_BASE_URL")
	}

	valid := map[string]string{
		"DATABASE_URL":        "postgres://user:pass@localhost:5432/rag",
		"EMBEDDING_DIMENSION": "768",
	}
	if err := LoadFrom(lookupFrom(valid)).Validate("embedctl"); err != nil {
		t.Fatalf("valid embedctl config must pass, got %v", err)
	}
}

func TestValidateUnknownComponent(t *testing.T) {
	if err := LoadFrom(lookupFrom(map[string]string{})).Validate("unknown"); err == nil {
		t.Fatalf("expected unknown component error")
	}
}

func TestValidateWorkerChecksEmbeddingAndChunk(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{"EMBEDDING_SOURCE": "remote", "EMBEDDING_DIMENSION": "0"}))
	if err := cfg.Validate("worker"); err == nil {
		t.Fatalf("worker validation must reject non-positive embedding dimension")
	}

	cfg = LoadFrom(lookupFrom(map[string]string{"CHUNK_SIZE": "100", "CHUNK_OVERLAP": "200"}))
	if err := cfg.Validate("worker"); err == nil {
		t.Fatalf("worker validation must reject overlap larger than size")
	}
}

func TestChunkConfigDefaults(t *testing.T) {
	cfg := LoadFrom(func(string) (string, bool) { return "", false })
	if cfg.Chunk.Size != 900 || cfg.Chunk.Overlap != 120 {
		t.Fatalf("unexpected defaults: %+v", cfg.Chunk)
	}
	if len(cfg.Chunk.Markers) == 0 {
		t.Fatalf("expected default split markers")
	}
	if err := cfg.Chunk.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestChunkConfigReadsOverrides(t *testing.T) {
	env := map[string]string{
		"CHUNK_SIZE":     "512",
		"CHUNK_OVERLAP":  "50",
		"CHUNK_MIN_SIZE": "20",
		// A single backslash is what an operator writes in .env; the loader
		// unescapes it into a real newline marker.
		"CHUNK_SPLIT_MARKERS": `\n\n,\n,。`,
	}
	cfg := LoadFrom(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if cfg.Chunk.Size != 512 || cfg.Chunk.Overlap != 50 || cfg.Chunk.MinSize != 20 {
		t.Fatalf("unexpected overrides: %+v", cfg.Chunk)
	}
	if len(cfg.Chunk.Markers) != 3 {
		t.Fatalf("expected 3 markers, got %v", cfg.Chunk.Markers)
	}
	if cfg.Chunk.Markers[0] != "\n\n" || cfg.Chunk.Markers[1] != "\n" || cfg.Chunk.Markers[2] != "。" {
		t.Fatalf("markers not unescaped: %q", cfg.Chunk.Markers)
	}
}

func TestChunkConfigAllowsZeroOverlap(t *testing.T) {
	env := map[string]string{"CHUNK_OVERLAP": "0"}
	cfg := LoadFrom(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if cfg.Chunk.Overlap != 0 {
		t.Fatalf("explicit zero overlap must be preserved, got %d", cfg.Chunk.Overlap)
	}
}

func TestChunkConfigRejectsInvalidRanges(t *testing.T) {
	cases := []map[string]string{
		{"CHUNK_SIZE": "0"},
		{"CHUNK_SIZE": "100", "CHUNK_OVERLAP": "100"},
		{"CHUNK_SIZE": "100", "CHUNK_MIN_SIZE": "150"},
	}
	for _, env := range cases {
		current := map[string]string{}
		for key, value := range env {
			current[key] = value
		}
		cfg := LoadFrom(func(key string) (string, bool) { value, ok := current[key]; return value, ok })
		if err := cfg.Chunk.Validate(); err == nil {
			t.Fatalf("expected validation error for %v", env)
		}
	}
}

func TestWorkerRejectsLocalAdaptersInProduction(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL":             "postgres://rag_app:app-secret@localhost:5432/rag",
		"MAINTENANCE_DATABASE_URL": "postgres://rag_maintenance:maintenance-secret@localhost:5432/rag",
		"ADMIN_DATABASE_URL":       "postgres://rag_platform:owner-secret@localhost:5432/rag",
		"TENANT_RLS_MODE":          "enforce",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "prod-access-key",
		"S3_SECRET_KEY":            "prod-secret-key",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"APP_ENV":                  "production",
		"EMBEDDING_SOURCE":         "remote",
		"LLM_EMBEDDING_MODEL":      "bge-m3",
		"EMBEDDING_BASE_URL":       "https://api.example.com/v1",
		"CHAT_SOURCE":              "remote",
		"CHAT_BASE_URL":            "https://api.example.com/v1",
		"LLM_CHAT_MODEL":           "gpt-4o-mini",
	}
	if err := LoadFrom(lookupFrom(base)).Validate("worker"); err != nil {
		t.Fatalf("valid production worker config must pass: %v", err)
	}
	for _, key := range []string{"EMBEDDING_SOURCE", "CHAT_SOURCE"} {
		env := make(map[string]string, len(base))
		for name, value := range base {
			env[name] = value
		}
		env[key] = "local"
		if err := LoadFrom(lookupFrom(env)).Validate("worker"); err == nil {
			t.Fatalf("production worker must reject %s=local", key)
		}
	}
}

func TestInvalidTypedSettingsSurfaceAsConfigError(t *testing.T) {
	cases := map[string]string{
		"OUTBOX_BATCH_SIZE":             "abc",
		"AUDIT_RETENTION_DAYS":          "-1",
		"OIDC_ENABLED":                  "ture",
		"CONNECTOR_REQUESTS_PER_SECOND": "fast",
		"DATABASE_MAX_OPEN_CONNS":       "0",
	}
	for key, raw := range cases {
		cfg := LoadFrom(lookupFrom(map[string]string{key: raw}))
		if cfg.ConfigError == nil {
			t.Fatalf("%s=%q must be reported instead of silently using the default", key, raw)
		}
		if !strings.Contains(cfg.ConfigError.Error(), key) {
			t.Fatalf("error %q must name %s", cfg.ConfigError, key)
		}
	}

	// Explicit zero stays meaningful for settings where zero is documented.
	zero := LoadFrom(lookupFrom(map[string]string{
		"AUDIT_RETENTION_DAYS":        "0",
		"STREAM_EVENT_RETENTION_DAYS": "0",
		"CHUNK_OVERLAP":               "0",
		"RATE_LIMIT_PER_MINUTE":       "0",
	}))
	if zero.ConfigError != nil {
		t.Fatalf("documented zero values must be accepted: %v", zero.ConfigError)
	}
	if zero.Limits.AuditRetentionDays != 0 || zero.Limits.StreamEventRetentionDays != 0 ||
		zero.Limits.RateLimitPerMinute != 0 || zero.Chunk.Overlap != 0 {
		t.Fatalf("zero values were not preserved: %+v", zero)
	}
}

func TestTaskHistoryRetentionConfig(t *testing.T) {
	defaults := LoadFrom(lookupFrom(map[string]string{}))
	if defaults.Limits.TaskHistoryRetentionDays != 90 {
		t.Fatalf("default task history retention = %d, want 90 days", defaults.Limits.TaskHistoryRetentionDays)
	}
	disabled := LoadFrom(lookupFrom(map[string]string{"TASK_HISTORY_RETENTION_DAYS": "0"}))
	if disabled.ConfigError != nil {
		t.Fatalf("zero must disable retention: %v", disabled.ConfigError)
	}
	if disabled.Limits.TaskHistoryRetentionDays != 0 {
		t.Fatalf("retention = %d, want 0", disabled.Limits.TaskHistoryRetentionDays)
	}
	negative := LoadFrom(lookupFrom(map[string]string{"TASK_HISTORY_RETENTION_DAYS": "-1"}))
	if negative.ConfigError == nil || !strings.Contains(negative.ConfigError.Error(), "TASK_HISTORY_RETENTION_DAYS") {
		t.Fatalf("negative retention error = %v", negative.ConfigError)
	}
}

func TestDatabasePoolDefaultsAndValidation(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{}))
	if cfg.DatabaseMaxOpenConns != 20 || cfg.DatabaseMaxIdleConns != 10 || cfg.DatabaseConnMaxLifetimeSeconds != 1800 {
		t.Fatalf("unexpected pool defaults: %d/%d/%d",
			cfg.DatabaseMaxOpenConns, cfg.DatabaseMaxIdleConns, cfg.DatabaseConnMaxLifetimeSeconds)
	}

	base := map[string]string{"DATABASE_URL": "postgres://user:pass@localhost:5432/rag"}
	withPool := func(open, idle string) Config {
		env := map[string]string{
			"DATABASE_URL":            base["DATABASE_URL"],
			"DATABASE_MAX_OPEN_CONNS": open,
			"DATABASE_MAX_IDLE_CONNS": idle,
		}
		return LoadFrom(lookupFrom(env))
	}
	if err := withPool("5", "5").Validate("migrate"); err != nil {
		t.Fatalf("valid pool config rejected: %v", err)
	}
	if err := withPool("5", "6").Validate("migrate"); err == nil {
		t.Fatalf("idle connections must not exceed open connections")
	}
}

func TestValidateMetricsAddrForWorker(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL":             "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	withMetrics := func(address string) Config {
		env := make(map[string]string, len(base)+1)
		for key, value := range base {
			env[key] = value
		}
		env["METRICS_ADDR"] = address
		return LoadFrom(lookupFrom(env))
	}
	if err := withMetrics("0.0.0.0:9091").Validate("worker"); err != nil {
		t.Fatalf("worker must accept a valid METRICS_ADDR: %v", err)
	}
	if err := withMetrics("9091").Validate("worker"); err == nil {
		t.Fatalf("worker must reject a METRICS_ADDR without a host:port")
	}
}

func TestLoadFromReadsRedisPasswordAndS3PathStyle(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{
		"REDIS_PASSWORD":      "s3cret",
		"S3_FORCE_PATH_STYLE": "true",
	}))
	if cfg.RedisPassword != "s3cret" {
		t.Fatalf("REDIS_PASSWORD was not applied: %q", cfg.RedisPassword)
	}
	if !cfg.S3ForcePathStyle {
		t.Fatalf("S3_FORCE_PATH_STYLE was not applied")
	}

	// Unset values stay permissive: no Redis auth, and the storage client keeps
	// its own endpoint detection instead of being forced to path style.
	unset := LoadFrom(lookupFrom(map[string]string{}))
	if unset.RedisPassword != "" {
		t.Fatalf("expected an empty Redis password by default, got %q", unset.RedisPassword)
	}
	if unset.S3ForcePathStyle {
		t.Fatalf("path style must not be forced unless asked for")
	}
}

func TestLoadFromReadsTrustedProxies(t *testing.T) {
	cfg := LoadFrom(lookupFrom(map[string]string{
		"HTTP_TRUSTED_PROXIES": "10.0.0.0/8, 192.0.2.10",
	}))
	want := []string{"10.0.0.0/8", "192.0.2.10"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("HTTP_TRUSTED_PROXIES was not applied: %v", cfg.TrustedProxies)
	}
	for index := range want {
		if cfg.TrustedProxies[index] != want[index] {
			t.Fatalf("HTTP_TRUSTED_PROXIES[%d] = %q, want %q", index, cfg.TrustedProxies[index], want[index])
		}
	}

	if unset := LoadFrom(lookupFrom(map[string]string{})); unset.TrustedProxies != nil {
		t.Fatalf("no proxy may be trusted by default, got %v", unset.TrustedProxies)
	}
}

func TestValidateRejectsMalformedTrustedProxies(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":             "postgres://user:pass@localhost:5432/rag",
		"REDIS_ADDR":               "redis:6379",
		"DOCREADER_GRPC_ADDR":      "docreader:50051",
		"S3_ENDPOINT":              "minio:9000",
		"S3_BUCKET":                "bucket",
		"S3_ACCESS_KEY":            "key",
		"S3_SECRET_KEY":            "secret",
		"CONNECTOR_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	for _, proxy := range []string{"not-an-ip", "10.0.0.0/99", "0.0.0.0/0"} {
		env["HTTP_TRUSTED_PROXIES"] = proxy
		if err := LoadFrom(lookupFrom(env)).Validate("api"); err == nil {
			t.Fatalf("expected HTTP_TRUSTED_PROXIES %q to be rejected", proxy)
		}
	}
}

func TestAnswerConfigDefaultsAndValidation(t *testing.T) {
	defaults := LoadFrom(lookupFrom(map[string]string{}))
	if !defaults.Answer.AsyncEnabled || defaults.Answer.TailTimeoutSec != 180 ||
		defaults.Answer.PollIntervalMS != 250 || defaults.Answer.LeaseSeconds != 300 {
		t.Fatalf("answer defaults: %+v", defaults.Answer)
	}

	overrides := LoadFrom(lookupFrom(map[string]string{
		"ANSWER_ASYNC_ENABLED":          "false",
		"ANSWER_TAIL_TIMEOUT_SECONDS":   "60",
		"ANSWER_ASYNC_POLL_INTERVAL_MS": "100",
		"ANSWER_ASYNC_LEASE_SECONDS":    "120",
	}))
	if overrides.Answer.AsyncEnabled || overrides.Answer.TailTimeoutSec != 60 ||
		overrides.Answer.PollIntervalMS != 100 || overrides.Answer.LeaseSeconds != 120 {
		t.Fatalf("answer overrides: %+v", overrides.Answer)
	}

	if err := (AnswerConfig{}).Validate(); err != nil {
		t.Fatalf("a disabled async path must not require tail settings: %v", err)
	}
	if err := (AnswerConfig{AsyncEnabled: true}).Validate(); err == nil {
		t.Fatalf("an enabled async path with zero bounds must fail startup")
	}
	if err := (AnswerConfig{AsyncEnabled: true, TailTimeoutSec: 1, PollIntervalMS: 1, LeaseSeconds: 1}).Validate(); err != nil {
		t.Fatalf("valid async settings rejected: %v", err)
	}
}
