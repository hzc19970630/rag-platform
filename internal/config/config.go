package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/example/rag-platform/internal/platform/formats"
	"github.com/example/rag-platform/internal/platform/param"
	"gopkg.in/yaml.v3"
)

// DefaultParserVersion must stay in sync with services/docreader/main.py.
const DefaultParserVersion = "docreader-m2-v2"

type Config struct {
	APIAddr string
	// MetricsAddr enables the dedicated Prometheus listener in the API process
	// when non-empty. It serves only /metrics and is deliberately not tenant
	// authenticated, so it must bind an internal or loopback address.
	MetricsAddr        string
	AppEnv             string
	LogLevel           string
	DatabaseURL        string
	RedisAddr          string
	RedisPassword      string
	DocreaderAddr      string
	S3Endpoint         string
	S3Region           string
	S3Bucket           string
	S3AccessKey        string
	S3SecretKey        string
	S3UseTLS           bool
	S3ForcePathStyle   bool
	MaxFileBytes       int64
	TaskTimeoutSeconds int
	ParserSandbox      bool
	// ParserVersion identifies the bundled docreader parser. It is read-only
	// configuration in the workbench and must match services/docreader.
	ParserVersion    string
	SupportedFormats []string
	Embedding        EmbeddingConfig
	Chunk            ChunkConfig
	Retrieval        RetrievalConfig
	Chat             ChatConfig
	Rerank           RerankConfig
	Auth             AuthConfig
	Limits           LimitsConfig
	Connector        ConnectorConfig
	WebPublicURL     string
	// RetrievalConfigFile is the YAML file holding the global retrieval
	// parameter defaults (the first tier of the override chain).
	RetrievalConfigFile string
	// PromptTemplateDir holds the externalized prompt templates. Template wording
	// is configuration, so Go code must not carry prompt literals.
	PromptTemplateDir string
	// ConfigError reports a malformed configuration file discovered at load
	// time. Validate surfaces it so a bad file fails startup instead of being
	// silently replaced by code defaults.
	ConfigError error
}

// RetrievalConfig is the global tier of retrieval parameters. The values are
// overridable per knowledge base (database, audited) and per session (request),
// so they are policy rather than constants.
type RetrievalConfig struct {
	TopK              int     `yaml:"top_k"`
	EmbeddingTopK     int     `yaml:"embedding_top_k"`
	KeywordTopK       int     `yaml:"keyword_top_k"`
	VectorThreshold   float64 `yaml:"vector_threshold"`
	KeywordThreshold  float64 `yaml:"keyword_threshold"`
	RerankEnabled     bool    `yaml:"rerank_enabled"`
	RerankTopK        int     `yaml:"rerank_top_k"`
	RerankThreshold   float64 `yaml:"rerank_threshold"`
	ContextTokenLimit int     `yaml:"context_token_limit"`
	AdjacentExpansion int     `yaml:"adjacent_expansion"`
	MaxAdjacentChunks int     `yaml:"max_adjacent_chunks"`
	// RRFK is the reciprocal rank fusion constant. It damps the influence of the
	// top ranks so neither recall channel dominates purely because its score
	// scale differs, which also fixes the band the fused score can land in.
	RRFK int `yaml:"rrf_k"`
	// ExpansionScoreFactor keeps expanded context ranked below the chunk that
	// pulled it in, so a parent paragraph never outranks a precise match.
	ExpansionScoreFactor float64 `yaml:"expansion_score_factor"`
}

type retrievalFile struct {
	Retrieval RetrievalConfig `yaml:"retrieval"`
}

// Validate enforces the documented ranges. Every tier goes through this check,
// so a session override cannot escape the same bounds as a knowledge-base one.
func (r RetrievalConfig) Validate() error {
	for name, value := range map[string]int{
		"top_k":           r.TopK,
		"embedding_top_k": r.EmbeddingTopK,
		"keyword_top_k":   r.KeywordTopK,
		"rerank_top_k":    r.RerankTopK,
	} {
		if err := param.IntRange(name, value, 0, 200); err != nil {
			return err
		}
	}
	if err := param.FloatRange("vector_threshold", r.VectorThreshold, 0, 1); err != nil {
		return err
	}
	if err := param.NonNegativeFloat("keyword_threshold", r.KeywordThreshold); err != nil {
		return err
	}
	if err := param.FloatRange("rerank_threshold", r.RerankThreshold, -10, 10); err != nil {
		return err
	}
	if err := param.PositiveInt("context_token_limit", r.ContextTokenLimit); err != nil {
		return err
	}
	if err := param.NonNegativeInt("adjacent_expansion", r.AdjacentExpansion); err != nil {
		return err
	}
	if err := param.NonNegativeInt("max_adjacent_chunks", r.MaxAdjacentChunks); err != nil {
		return err
	}
	if err := param.IntRange("rrf_k", r.RRFK, 1, 200); err != nil {
		return err
	}
	if err := param.FloatRange("expansion_score_factor", r.ExpansionScoreFactor, 0, 1); err != nil {
		return err
	}
	return nil
}

// ChatConfig describes the generation model. Like the embedding model it is
// configuration, not code, and it uses the same local/remote split: the local
// adapter is a deterministic, offline stand-in for development and acceptance.
type ChatConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	TimeoutSec int
	MaxTokens  int
}

func (c ChatConfig) Validate() error {
	switch c.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only.
	case "remote":
		if strings.TrimSpace(c.BaseURL) == "" {
			return fmt.Errorf("CHAT_BASE_URL is required when CHAT_SOURCE=remote")
		}
		if strings.TrimSpace(c.Model) == "" {
			return fmt.Errorf("LLM_CHAT_MODEL is required when CHAT_SOURCE=remote")
		}
		parsed, err := url.Parse(c.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("CHAT_BASE_URL must be a valid base URL")
		}
	default:
		return fmt.Errorf("CHAT_SOURCE must be local or remote, got %q", c.Source)
	}
	return nil
}

// RerankConfig describes the reranking adapter. Rerank is a configurable
// capability, not a mandatory dependency, so the default source is none
// (pass-through): turning rerank on has to be an explicit, evaluated decision.
// lexical keeps the offline deterministic reranker reachable for evaluation;
// remote calls an OpenAI-compatible /rerank endpoint.
type RerankConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	TimeoutSec int
}

func (r RerankConfig) Validate() error {
	switch r.Source {
	case "none", "lexical":
		// Offline adapters: pass-through and overlap scoring. No endpoint needed.
	case "remote":
		if strings.TrimSpace(r.BaseURL) == "" {
			return fmt.Errorf("RERANK_BASE_URL is required when RERANK_SOURCE=remote")
		}
		if strings.TrimSpace(r.Model) == "" {
			return fmt.Errorf("LLM_RERANK_MODEL is required when RERANK_SOURCE=remote")
		}
		parsed, err := url.Parse(r.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("RERANK_BASE_URL must be a valid base URL")
		}
	default:
		return fmt.Errorf("RERANK_SOURCE must be none, lexical or remote, got %q", r.Source)
	}
	return nil
}

// ChunkConfig describes how normalized document blocks are split. Documents
// differ in shape (manuals, policies, FAQ), so chunk granularity is policy and
// belongs in configuration rather than in pipeline constants. Changing these
// values does not require a rebuild; already-ingested documents keep their
// chunks until they are explicitly reprocessed.
type ChunkConfig struct {
	// Size is the target maximum chunk length in runes.
	Size int
	// Overlap is how many runes each child chunk shares with its predecessor.
	Overlap int
	// Markers are preferred split points, tried before the fixed-size window.
	Markers []string
	// MinSize is the threshold under which a trailing chunk is merged back into
	// its predecessor instead of being indexed on its own.
	MinSize int
}

func (c ChunkConfig) Validate() error {
	if c.Size <= 0 {
		return fmt.Errorf("CHUNK_SIZE must be a positive integer")
	}
	if c.Overlap < 0 || c.Overlap >= c.Size {
		return fmt.Errorf("CHUNK_OVERLAP must be at least 0 and smaller than CHUNK_SIZE")
	}
	if c.MinSize < 0 || c.MinSize >= c.Size {
		return fmt.Errorf("CHUNK_MIN_SIZE must be at least 0 and smaller than CHUNK_SIZE")
	}
	return nil
}

// EmbeddingConfig describes the embedding model used by the ingest pipeline.
// The model is configuration, not code: swapping providers or models must not
// require a rebuild. Dimension is part of the contract because the vector
// index is built for exactly one dimension at a time.
type EmbeddingConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	Dimension  int
	BatchSize  int
	TimeoutSec int
}

func (e EmbeddingConfig) Validate() error {
	switch e.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only.
	case "remote":
		if strings.TrimSpace(e.BaseURL) == "" {
			return fmt.Errorf("EMBEDDING_BASE_URL is required when EMBEDDING_SOURCE=remote")
		}
		if strings.TrimSpace(e.Model) == "" {
			return fmt.Errorf("LLM_EMBEDDING_MODEL is required when EMBEDDING_SOURCE=remote")
		}
		parsed, err := url.Parse(e.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("EMBEDDING_BASE_URL must be a valid base URL")
		}
	default:
		return fmt.Errorf("EMBEDDING_SOURCE must be local or remote, got %q", e.Source)
	}
	if e.Dimension <= 0 {
		return fmt.Errorf("EMBEDDING_DIMENSION must be a positive integer")
	}
	return nil
}

// AuthConfig holds authentication policy. Signing keys are a ring so a key can
// be rotated without invalidating every live session: the active key signs new
// tokens, the old keys verify tokens issued before the rotation until they
// expire.
type AuthConfig struct {
	SessionTTLSeconds int
	JWTIssuer         string
	JWTAudience       string
	SigningKeys       []string
	ActiveKeyID       string
	CookieName        string
	CookieSecure      bool
	DevAuthEnabled    bool
	APIKeyPepper      string
	OIDC              OIDCConfig
}

type OIDCConfig struct {
	Enabled       bool
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	TenantClaim   string
	EmailClaim    string
	NameClaim     string
	AutoProvision bool
	DefaultTenant string
}

func (a AuthConfig) Validate(appEnv string) error {
	if len(a.SigningKeys) == 0 {
		return fmt.Errorf("JWT_SIGNING_KEYS must contain at least one kid:secret entry")
	}
	active := false
	for _, entry := range a.SigningKeys {
		kid, secret, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(kid) == "" || len(secret) < 16 {
			return fmt.Errorf("each JWT_SIGNING_KEYS entry must be kid:secret with a secret of at least 16 characters")
		}
		if kid == a.ActiveKeyID {
			active = true
		}
	}
	if !active {
		return fmt.Errorf("JWT_ACTIVE_KID must name one of JWT_SIGNING_KEYS")
	}
	if a.SessionTTLSeconds <= 0 {
		return fmt.Errorf("AUTH_SESSION_TTL_SECONDS must be positive")
	}
	if strings.TrimSpace(a.APIKeyPepper) == "" {
		return fmt.Errorf("API_KEY_PEPPER must not be empty")
	}
	if a.OIDC.Enabled {
		if strings.TrimSpace(a.OIDC.Issuer) == "" || strings.TrimSpace(a.OIDC.ClientID) == "" || strings.TrimSpace(a.OIDC.RedirectURL) == "" {
			return fmt.Errorf("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_REDIRECT_URL are required when OIDC_ENABLED=true")
		}
		parsed, err := url.Parse(a.OIDC.Issuer)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("OIDC_ISSUER must be a valid URL")
		}
	}
	if appEnv == "production" {
		if !a.OIDC.Enabled {
			return fmt.Errorf("OIDC_ENABLED must be true in production")
		}
		if a.DevAuthEnabled {
			return fmt.Errorf("DEV_AUTH_ENABLED must be false in production")
		}
		if !a.CookieSecure {
			return fmt.Errorf("AUTH_COOKIE_SECURE must be true in production")
		}
		if a.OIDC.ClientSecret == "" {
			return fmt.Errorf("OIDC_CLIENT_SECRET is required in production")
		}
		for _, entry := range a.SigningKeys {
			_, secret, _ := strings.Cut(entry, ":")
			if len(secret) < 32 {
				return fmt.Errorf("JWT signing secrets must be at least 32 characters in production")
			}
		}
	}
	return nil
}

// LimitsConfig is the operational policy surface: request and tenant limits,
// retention, retry budget and worker isolation. Zero disables a numeric limit.
type LimitsConfig struct {
	RateLimitPerMinute       int
	TenantMaxDocuments       int
	TenantMaxStorageBytes    int64
	TenantChatRequestsPerDay int
	DocumentRetentionDays    int
	AuditRetentionDays       int
	StreamEventRetentionDays int
	OutboxMaxAttempts        int
	OutboxBatchSize          int
	ReconcileIntervalSeconds int
	WorkerConcurrency        int
	ShutdownTimeoutSeconds   int
}

// ConnectorConfig is the M5 external-source baseline. Credentials are sealed
// with AES-GCM using EncryptionKey; URL fetching is SSRF-guarded, and private
// networks are never reachable in production.
type ConnectorConfig struct {
	EncryptionKey        string
	AllowPrivateNetworks bool
	RequestTimeoutSec    int
	MaxManifestBytes     int64
	DefaultRatePerSecond float64
}

func (c ConnectorConfig) Validate(appEnv string) error {
	if strings.TrimSpace(c.EncryptionKey) == "" {
		return fmt.Errorf("CONNECTOR_ENCRYPTION_KEY is required")
	}
	if appEnv == "production" && c.AllowPrivateNetworks {
		return fmt.Errorf("CONNECTOR_ALLOW_PRIVATE_NETWORKS must be false in production")
	}
	if c.RequestTimeoutSec <= 0 {
		return fmt.Errorf("CONNECTOR_REQUEST_TIMEOUT_SECONDS must be positive")
	}
	if c.MaxManifestBytes <= 0 {
		return fmt.Errorf("CONNECTOR_MAX_MANIFEST_BYTES must be positive")
	}
	if c.DefaultRatePerSecond <= 0 || c.DefaultRatePerSecond > 50 {
		return fmt.Errorf("CONNECTOR_REQUESTS_PER_SECOND must be between 0 and 50")
	}
	return nil
}

func (l LimitsConfig) Validate() error {
	for name, value := range map[string]int{
		"RATE_LIMIT_PER_MINUTE":        l.RateLimitPerMinute,
		"DOCUMENT_RETENTION_DAYS":      l.DocumentRetentionDays,
		"AUDIT_RETENTION_DAYS":         l.AuditRetentionDays,
		"STREAM_EVENT_RETENTION_DAYS":  l.StreamEventRetentionDays,
		"OUTBOX_MAX_ATTEMPTS":          l.OutboxMaxAttempts,
		"OUTBOX_BATCH_SIZE":            l.OutboxBatchSize,
		"RECONCILE_INTERVAL_SECONDS":   l.ReconcileIntervalSeconds,
		"WORKER_CONCURRENCY":           l.WorkerConcurrency,
		"SHUTDOWN_TIMEOUT_SECONDS":     l.ShutdownTimeoutSeconds,
		"TENANT_MAX_DOCUMENTS":         l.TenantMaxDocuments,
		"TENANT_CHAT_REQUESTS_PER_DAY": l.TenantChatRequestsPerDay,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if l.DocumentRetentionDays == 0 {
		return fmt.Errorf("DOCUMENT_RETENTION_DAYS must be positive")
	}
	if l.OutboxMaxAttempts == 0 {
		return fmt.Errorf("OUTBOX_MAX_ATTEMPTS must be positive")
	}
	if l.OutboxBatchSize == 0 {
		return fmt.Errorf("OUTBOX_BATCH_SIZE must be positive")
	}
	if l.WorkerConcurrency == 0 {
		return fmt.Errorf("WORKER_CONCURRENCY must be positive")
	}
	if l.ShutdownTimeoutSeconds == 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT_SECONDS must be positive")
	}
	return nil
}

type Lookup func(string) (string, bool)

func Load() Config {
	cfg := LoadFrom(os.LookupEnv)
	if err := applyRetrievalDefaults(&cfg); err != nil {
		cfg.ConfigError = err
	}
	return cfg
}

// defaultRetrievalConfig mirrors config/config.yaml. The file is the operator
// facing tier; these values are only the fallback used when the file is absent
// so a development stack still starts.
func defaultRetrievalConfig() RetrievalConfig {
	return RetrievalConfig{
		TopK:              8,
		EmbeddingTopK:     20,
		KeywordTopK:       20,
		VectorThreshold:   0.2,
		KeywordThreshold:  0,
		RerankEnabled:     false,
		RerankTopK:        8,
		RerankThreshold:   -10,
		ContextTokenLimit: 3000,
		AdjacentExpansion: 1,
		MaxAdjacentChunks: 2,
		// With rrf_k = 60 a chunk ranked first by both channels fuses to
		// 2/(60+1) ~ 0.033, which is the band the API reports as `score`.
		RRFK:                 60,
		ExpansionScoreFactor: 0.5,
	}
}

// applyRetrievalDefaults reads the global retrieval parameter file. A missing
// file keeps the code defaults; a malformed one is reported rather than
// silently ignored.
func applyRetrievalDefaults(cfg *Config) error {
	path := strings.TrimSpace(cfg.RetrievalConfigFile)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read retrieval config %s: %w", path, err)
	}
	var parsed retrievalFile
	// Seed with the code defaults so a partial file only overrides the keys it
	// actually states, instead of zeroing the rest.
	parsed.Retrieval = defaultRetrievalConfig()
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse retrieval config %s: %w", path, err)
	}
	cfg.Retrieval = parsed.Retrieval
	return cfg.Retrieval.Validate()
}

func LoadFrom(lookup Lookup) Config {
	appEnv := value(lookup, "APP_ENV", "development")
	apiPublicURL := value(lookup, "API_PUBLIC_URL", "http://localhost:8082")
	signingKeys := splitValue(lookup, "JWT_SIGNING_KEYS", nil)
	if len(signingKeys) == 0 {
		signingKeys = []string{"dev:" + value(lookup, "JWT_SIGNING_KEY", "local-development-signing-key-change-me")}
	}
	activeKeyID := value(lookup, "JWT_ACTIVE_KID", "")
	if activeKeyID == "" {
		if firstKid, _, ok := strings.Cut(signingKeys[0], ":"); ok {
			activeKeyID = firstKid
		}
	}
	return Config{
		APIAddr:      value(lookup, "API_ADDR", ":8080"),
		MetricsAddr:  value(lookup, "METRICS_ADDR", ""),
		AppEnv:       appEnv,
		WebPublicURL: value(lookup, "WEB_PUBLIC_URL", "http://localhost:5173"),
		Auth: AuthConfig{
			SessionTTLSeconds: int(int64Value(lookup, "AUTH_SESSION_TTL_SECONDS", 43200)),
			JWTIssuer:         value(lookup, "JWT_ISSUER", "rag-platform"),
			JWTAudience:       value(lookup, "JWT_AUDIENCE", "rag-platform-api"),
			SigningKeys:       signingKeys,
			ActiveKeyID:       activeKeyID,
			CookieName:        value(lookup, "AUTH_COOKIE_NAME", "rag_session"),
			CookieSecure:      boolValue(lookup, "AUTH_COOKIE_SECURE", appEnv == "production"),
			DevAuthEnabled:    boolValue(lookup, "DEV_AUTH_ENABLED", appEnv != "production"),
			APIKeyPepper:      value(lookup, "API_KEY_PEPPER", "local-api-key-pepper-change-me"),
			OIDC: OIDCConfig{
				Enabled:       boolValue(lookup, "OIDC_ENABLED", false),
				Issuer:        strings.TrimSuffix(value(lookup, "OIDC_ISSUER", ""), "/"),
				ClientID:      value(lookup, "OIDC_CLIENT_ID", ""),
				ClientSecret:  value(lookup, "OIDC_CLIENT_SECRET", ""),
				RedirectURL:   value(lookup, "OIDC_REDIRECT_URL", apiPublicURL+"/api/v1/auth/callback"),
				Scopes:        listValue(lookup, "OIDC_SCOPES", []string{"openid", "email", "profile"}),
				TenantClaim:   value(lookup, "OIDC_TENANT_CLAIM", "tenant_id"),
				EmailClaim:    value(lookup, "OIDC_EMAIL_CLAIM", "email"),
				NameClaim:     value(lookup, "OIDC_NAME_CLAIM", "name"),
				AutoProvision: boolValue(lookup, "OIDC_AUTO_PROVISION", false),
				DefaultTenant: value(lookup, "OIDC_DEFAULT_TENANT", ""),
			},
		},
		Limits: LimitsConfig{
			RateLimitPerMinute:       int(int64Value(lookup, "RATE_LIMIT_PER_MINUTE", 600)),
			TenantMaxDocuments:       int(nonNegativeValue(lookup, "TENANT_MAX_DOCUMENTS", 0)),
			TenantMaxStorageBytes:    nonNegativeValue(lookup, "TENANT_MAX_STORAGE_MB", 0) * 1024 * 1024,
			TenantChatRequestsPerDay: int(nonNegativeValue(lookup, "TENANT_CHAT_REQUESTS_PER_DAY", 0)),
			DocumentRetentionDays:    int(int64Value(lookup, "DOCUMENT_RETENTION_DAYS", 30)),
			AuditRetentionDays:       int(nonNegativeValue(lookup, "AUDIT_RETENTION_DAYS", 180)),
			StreamEventRetentionDays: int(nonNegativeValue(lookup, "STREAM_EVENT_RETENTION_DAYS", 7)),
			OutboxMaxAttempts:        int(int64Value(lookup, "OUTBOX_MAX_ATTEMPTS", 10)),
			OutboxBatchSize:          int(int64Value(lookup, "OUTBOX_BATCH_SIZE", 64)),
			ReconcileIntervalSeconds: int(int64Value(lookup, "RECONCILE_INTERVAL_SECONDS", 60)),
			WorkerConcurrency:        int(int64Value(lookup, "WORKER_CONCURRENCY", 4)),
			ShutdownTimeoutSeconds:   int(int64Value(lookup, "SHUTDOWN_TIMEOUT_SECONDS", 10)),
		},
		Connector: ConnectorConfig{
			EncryptionKey:        value(lookup, "CONNECTOR_ENCRYPTION_KEY", ""),
			AllowPrivateNetworks: boolValue(lookup, "CONNECTOR_ALLOW_PRIVATE_NETWORKS", false),
			RequestTimeoutSec:    int(int64Value(lookup, "CONNECTOR_REQUEST_TIMEOUT_SECONDS", 30)),
			MaxManifestBytes:     int64Value(lookup, "CONNECTOR_MAX_MANIFEST_BYTES", 2*1024*1024),
			DefaultRatePerSecond: floatValue(lookup, "CONNECTOR_REQUESTS_PER_SECOND", 2),
		},
		LogLevel:            value(lookup, "APP_LOG_LEVEL", "info"),
		DatabaseURL:         value(lookup, "DATABASE_URL", ""),
		RedisAddr:           value(lookup, "REDIS_ADDR", "redis:6379"),
		RedisPassword:       value(lookup, "REDIS_PASSWORD", ""),
		DocreaderAddr:       value(lookup, "DOCREADER_GRPC_ADDR", "docreader:50051"),
		S3Endpoint:          value(lookup, "S3_ENDPOINT", "minio:9000"),
		S3Region:            value(lookup, "S3_REGION", "us-east-1"),
		S3Bucket:            value(lookup, "S3_BUCKET", "rag-platform-local"),
		S3AccessKey:         value(lookup, "S3_ACCESS_KEY", value(lookup, "MINIO_ROOT_USER", "rag-local")),
		S3SecretKey:         value(lookup, "S3_SECRET_KEY", value(lookup, "MINIO_ROOT_PASSWORD", "change-me-local-only")),
		S3UseTLS:            strings.EqualFold(value(lookup, "S3_USE_TLS", "false"), "true"),
		S3ForcePathStyle:    strings.EqualFold(value(lookup, "S3_FORCE_PATH_STYLE", "false"), "true"),
		MaxFileBytes:        int64Value(lookup, "DOCREADER_MAX_FILE_MB", 100) * 1024 * 1024,
		TaskTimeoutSeconds:  int(int64Value(lookup, "DOCREADER_TASK_TIMEOUT_SECONDS", 120)),
		ParserSandbox:       strings.EqualFold(value(lookup, "PARSER_SANDBOX_ENABLED", "true"), "true"),
		ParserVersion:       value(lookup, "PARSER_VERSION", DefaultParserVersion),
		SupportedFormats:    formats.Names(),
		RetrievalConfigFile: value(lookup, "RETRIEVAL_CONFIG_FILE", "config/config.yaml"),
		PromptTemplateDir:   value(lookup, "PROMPT_TEMPLATE_DIR", "config/prompt_templates"),
		Retrieval:           defaultRetrievalConfig(),
		Chat: ChatConfig{
			Source:     strings.ToLower(value(lookup, "CHAT_SOURCE", "local")),
			BaseURL:    value(lookup, "CHAT_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:     value(lookup, "CHAT_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:      value(lookup, "LLM_CHAT_MODEL", "local-extractive"),
			TimeoutSec: int(int64Value(lookup, "CHAT_TIMEOUT_SECONDS", int64Value(lookup, "LLM_REQUEST_TIMEOUT_SECONDS", 60))),
			MaxTokens:  int(int64Value(lookup, "CHAT_MAX_TOKENS", 1024)),
		},
		Rerank: RerankConfig{
			Source:     strings.ToLower(value(lookup, "RERANK_SOURCE", "none")),
			BaseURL:    value(lookup, "RERANK_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:     value(lookup, "RERANK_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:      value(lookup, "LLM_RERANK_MODEL", ""),
			TimeoutSec: int(int64Value(lookup, "RERANK_TIMEOUT_SECONDS", int64Value(lookup, "LLM_REQUEST_TIMEOUT_SECONDS", 60))),
		},
		Embedding: EmbeddingConfig{
			Source:     strings.ToLower(value(lookup, "EMBEDDING_SOURCE", "local")),
			BaseURL:    value(lookup, "EMBEDDING_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:     value(lookup, "EMBEDDING_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:      value(lookup, "LLM_EMBEDDING_MODEL", "local-deterministic"),
			Dimension:  int(int64Value(lookup, "EMBEDDING_DIMENSION", 3)),
			BatchSize:  int(int64Value(lookup, "EMBEDDING_BATCH_SIZE", 32)),
			TimeoutSec: int(int64Value(lookup, "EMBEDDING_TIMEOUT_SECONDS", 60)),
		},
		Chunk: ChunkConfig{
			Size:    int(nonNegativeValue(lookup, "CHUNK_SIZE", 900)),
			Overlap: int(nonNegativeValue(lookup, "CHUNK_OVERLAP", 120)),
			Markers: listValue(lookup, "CHUNK_SPLIT_MARKERS", []string{"\n\n", "\n", "。", ".", "！", "!", "？", "?"}),
			MinSize: int(nonNegativeValue(lookup, "CHUNK_MIN_SIZE", 0)),
		},
	}
}

func (c Config) Validate(component string) error {
	required := map[string]string{}
	switch component {
	case "api":
		required = map[string]string{
			"API_ADDR":            c.APIAddr,
			"DATABASE_URL":        c.DatabaseURL,
			"REDIS_ADDR":          c.RedisAddr,
			"DOCREADER_GRPC_ADDR": c.DocreaderAddr,
			"S3_ENDPOINT":         c.S3Endpoint,
			"S3_BUCKET":           c.S3Bucket,
			"S3_ACCESS_KEY":       c.S3AccessKey,
			"S3_SECRET_KEY":       c.S3SecretKey,
		}
	case "worker":
		required = map[string]string{
			"DATABASE_URL":        c.DatabaseURL,
			"REDIS_ADDR":          c.RedisAddr,
			"DOCREADER_GRPC_ADDR": c.DocreaderAddr,
			"S3_ENDPOINT":         c.S3Endpoint,
			"S3_BUCKET":           c.S3Bucket,
			"S3_ACCESS_KEY":       c.S3AccessKey,
			"S3_SECRET_KEY":       c.S3SecretKey,
		}
	case "migrate", "seed", "embedctl":
		required = map[string]string{"DATABASE_URL": c.DatabaseURL}
	default:
		return fmt.Errorf("unknown configuration component %q", component)
	}

	for key, current := range required {
		if strings.TrimSpace(current) == "" {
			return fmt.Errorf("missing required configuration %s for %s", key, component)
		}
	}
	if c.ConfigError != nil {
		return c.ConfigError
	}
	if component == "api" {
		parsed, err := url.Parse(c.DatabaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("DATABASE_URL must be a valid database URL")
		}
		if metricsAddr := strings.TrimSpace(c.MetricsAddr); metricsAddr != "" {
			_, port, err := net.SplitHostPort(metricsAddr)
			if err != nil {
				return fmt.Errorf("METRICS_ADDR must be a host:port listen address: %w", err)
			}
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return fmt.Errorf("METRICS_ADDR port must be a number between 1 and 65535")
			}
			if metricsAddr == strings.TrimSpace(c.APIAddr) {
				return fmt.Errorf("METRICS_ADDR must differ from API_ADDR")
			}
		}
		if c.AppEnv == "production" && !c.ParserSandbox {
			return fmt.Errorf("PARSER_SANDBOX_ENABLED must be true in production")
		}
		if c.AppEnv == "production" && c.Embedding.Source == "local" {
			return fmt.Errorf("EMBEDDING_SOURCE=local is a development adapter and must not run in production")
		}
		if err := c.Retrieval.Validate(); err != nil {
			return err
		}
		if err := c.Chat.Validate(); err != nil {
			return err
		}
		if err := c.Rerank.Validate(); err != nil {
			return err
		}
		if c.AppEnv == "production" && c.Chat.Source == "local" {
			return fmt.Errorf("CHAT_SOURCE=local is a development adapter and must not run in production")
		}
		if strings.TrimSpace(c.PromptTemplateDir) == "" {
			return fmt.Errorf("PROMPT_TEMPLATE_DIR is required")
		}
		if err := c.Auth.Validate(c.AppEnv); err != nil {
			return err
		}
		if err := c.Limits.Validate(); err != nil {
			return err
		}
		if err := c.Connector.Validate(c.AppEnv); err != nil {
			return err
		}
	}
	if component == "worker" {
		if err := c.Embedding.Validate(); err != nil {
			return err
		}
		if err := c.Chunk.Validate(); err != nil {
			return err
		}
		if err := c.Limits.Validate(); err != nil {
			return err
		}
		if err := c.Connector.Validate(c.AppEnv); err != nil {
			return err
		}
	}
	if component == "embedctl" {
		if err := c.Embedding.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func value(lookup Lookup, key, fallback string) string {
	if current, ok := lookup(key); ok && current != "" {
		return current
	}
	return fallback
}

func boolValue(lookup Lookup, key string, fallback bool) bool {
	current, ok := lookup(key)
	if !ok || strings.TrimSpace(current) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

// splitValue reads a comma-separated list without marker unescaping. Signing
// secrets and OIDC scopes must be taken literally.
func splitValue(lookup Lookup, key string, fallback []string) []string {
	current, ok := lookup(key)
	if !ok || strings.TrimSpace(current) == "" {
		return fallback
	}
	parts := strings.Split(current, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func int64Value(lookup Lookup, key string, fallback int64) int64 {
	current := value(lookup, key, "")
	if current == "" {
		return fallback
	}
	var parsed int64
	if _, err := fmt.Sscan(current, &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func listValue(lookup Lookup, key string, fallback []string) []string {
	current, ok := lookup(key)
	if !ok || strings.TrimSpace(current) == "" {
		return fallback
	}
	parts := strings.Split(current, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// Markers may legitimately contain leading or trailing whitespace, so
		// only the surrounding comma padding is removed here. Unescaping happens
		// after trimming, otherwise a "\n" marker would be trimmed to nothing.
		item := unescapeMarkers(strings.TrimSpace(part))
		if item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func floatValue(lookup Lookup, key string, fallback float64) float64 {
	current := value(lookup, key, "")
	if current == "" {
		return fallback
	}
	var parsed float64
	if _, err := fmt.Sscan(current, &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// nonNegativeValue reads an integer that is allowed to be zero, unlike
// int64Value which treats zero as "unset". Overlap and minimum size are
// meaningful at zero, so they need their own parser.
func nonNegativeValue(lookup Lookup, key string, fallback int64) int64 {
	current := value(lookup, key, "")
	if current == "" {
		return fallback
	}
	var parsed int64
	if _, err := fmt.Sscan(current, &parsed); err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

// unescapeMarkers lets a marker list be written in .env as "\n\n,\n"
// where a literal newline would be awkward to express.
func unescapeMarkers(value string) string {
	value = strings.ReplaceAll(value, "\\r", "\r")
	value = strings.ReplaceAll(value, "\\t", "\t")
	return strings.ReplaceAll(value, "\\n", "\n")
}
