package config

import (
	"errors"
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
	// MetricsAddr enables the dedicated Prometheus listener in the api and worker
	// processes when non-empty. It serves only /metrics and is deliberately not
	// tenant authenticated, so it must bind an internal or loopback address.
	MetricsAddr string
	// OTELExporterEndpoint enables OTLP/HTTP trace export when non-empty, for
	// example http://otel-collector:4318. Empty keeps tracing local/no-op.
	OTELExporterEndpoint string
	OTELServiceName      string
	AppEnv               string
	LogLevel             string
	DatabaseURL          string
	// MaintenanceDatabaseURL points at the BYPASSRLS maintenance role. The API
	// uses it for pre-tenant authentication lookups, and in enforce mode the
	// worker runs its cross-tenant jobs on it. Empty keeps DatabaseURL, which is
	// only safe while RLS policies are permissive.
	MaintenanceDatabaseURL string
	// AdminDatabaseURL is the table-owner role for migrate, seed and embedctl.
	// DDL and bulk maintenance must not depend on the runtime role's grants.
	AdminDatabaseURL string
	// Pool limits bound one process's share of PostgreSQL connections. They are
	// applied after opening the database, not through the DSN.
	DatabaseMaxOpenConns           int
	DatabaseMaxIdleConns           int
	DatabaseConnMaxLifetimeSeconds int
	RedisAddr                      string
	RedisPassword                  string
	DocreaderAddr                  string
	S3Endpoint                     string
	S3Region                       string
	S3Bucket                       string
	S3AccessKey                    string
	S3SecretKey                    string
	S3UseTLS                       bool
	S3ForcePathStyle               bool
	MaxFileBytes                   int64
	TaskTimeoutSeconds             int
	ParserSandbox                  bool
	// ParserVersion identifies the bundled docreader parser. It is read-only
	// configuration in the workbench and must match services/docreader.
	ParserVersion    string
	SupportedFormats []string
	Embedding        EmbeddingConfig
	Chunk            ChunkConfig
	Retrieval        RetrievalConfig
	Chat             ChatConfig
	Answer           AnswerConfig
	Rerank           RerankConfig
	Auth             AuthConfig
	Limits           LimitsConfig
	Connector        ConnectorConfig
	Projection       ProjectionConfig
	WebPublicURL     string
	// TrustedProxies lists the IPs or CIDRs of reverse proxies whose forwarded
	// client-IP headers may be trusted. Empty trusts no proxy: the audit IP is
	// the TCP peer, so a caller cannot choose it with X-Forwarded-For.
	TrustedProxies []string
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
	// TenantRLSMode selects how tenantdb marks the PostgreSQL session for
	// row-level security: off skips the app.tenant_id GUC, shadow (default)
	// sets it so triggers can audit writes, and enforce requires strict
	// policies. Enforcement is a deployment decision; the default never blocks.
	TenantRLSMode string
}

// RetrievalConfig is the global tier of retrieval parameters. The values are
// overridable per knowledge base (database, audited) and per session (request),
// so they are policy rather than constants.
type RetrievalConfig struct {
	TopK             int     `yaml:"top_k"`
	EmbeddingTopK    int     `yaml:"embedding_top_k"`
	KeywordTopK      int     `yaml:"keyword_top_k"`
	VectorThreshold  float64 `yaml:"vector_threshold"`
	KeywordThreshold float64 `yaml:"keyword_threshold"`
	RerankEnabled    bool    `yaml:"rerank_enabled"`
	RerankTopK       int     `yaml:"rerank_top_k"`
	RerankThreshold  float64 `yaml:"rerank_threshold"`
	// VectorEFSearch is the pgvector HNSW candidate-list size. Zero leaves the
	// server default (40 on pgvector 0.8) in place.
	VectorEFSearch int `yaml:"vector_ef_search"`
	// VectorOversample multiplies the SQL LIMIT so a selective tenant filter
	// can still fill embedding_top_k after filtering.
	VectorOversample int `yaml:"vector_oversample"`
	// VectorIterativeScan enables pgvector's relaxed-order iterative scan. It
	// only affects filtered HNSW scans; exact scans ignore it.
	VectorIterativeScan bool `yaml:"vector_iterative_scan"`
	ContextTokenLimit   int  `yaml:"context_token_limit"`
	AdjacentExpansion   int  `yaml:"adjacent_expansion"`
	MaxAdjacentChunks   int  `yaml:"max_adjacent_chunks"`
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
	return param.ValidateRetrieval(param.RetrievalValues{
		TopK:                 &r.TopK,
		EmbeddingTopK:        &r.EmbeddingTopK,
		KeywordTopK:          &r.KeywordTopK,
		VectorThreshold:      &r.VectorThreshold,
		KeywordThreshold:     &r.KeywordThreshold,
		RerankTopK:           &r.RerankTopK,
		RerankThreshold:      &r.RerankThreshold,
		VectorEFSearch:       &r.VectorEFSearch,
		VectorOversample:     &r.VectorOversample,
		ContextTokenLimit:    &r.ContextTokenLimit,
		AdjacentExpansion:    &r.AdjacentExpansion,
		MaxAdjacentChunks:    &r.MaxAdjacentChunks,
		RRFK:                 &r.RRFK,
		ExpansionScoreFactor: &r.ExpansionScoreFactor,
	})
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
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/models); a non-empty value must be a URL.
	HealthURL string
	// Models lists additional routable chat models as name[@version]. The
	// deployment default (LLM_CHAT_MODEL) is always registered. Empty keeps a
	// single-model deployment.
	Models []string
	// HistoryTurns is how many completed user/assistant turns are replayed to
	// the model. Zero disables multi-turn context.
	HistoryTurns int
	// HistoryTokenLimit bounds the replayed history independently of the
	// evidence context budget.
	HistoryTokenLimit int
	// QueryRewriteEnabled enables the retrieval-side follow-up rewriter. It is a
	// separate model call only when history exists and the chat adapter is real.
	QueryRewriteEnabled bool
	// QueryRewriteTimeoutSec bounds one rewrite call.
	QueryRewriteTimeoutSec int
}

// AnswerConfig controls the async answer path: the API enqueues a job and tails
// the persisted stream events instead of generating inside the HTTP request.
type AnswerConfig struct {
	// AsyncEnabled routes POST /sessions/:id/messages through the worker queue.
	// False keeps the synchronous path as the rollback switch.
	AsyncEnabled bool
	// TailTimeoutSec bounds how long one HTTP request tails a job before it
	// gives up and relies on replay after reconnect.
	TailTimeoutSec int
	// PollIntervalMS is the replay polling interval for an open tail.
	PollIntervalMS int
	// LeaseSeconds is how long a claimed job is reserved before the maintenance
	// sweep treats the worker as lost.
	LeaseSeconds int
}

func (c AnswerConfig) Validate() error {
	if !c.AsyncEnabled {
		return nil
	}
	if c.TailTimeoutSec <= 0 {
		return fmt.Errorf("ANSWER_TAIL_TIMEOUT_SECONDS must be a positive integer")
	}
	if c.PollIntervalMS <= 0 {
		return fmt.Errorf("ANSWER_ASYNC_POLL_INTERVAL_MS must be a positive integer")
	}
	if c.LeaseSeconds <= 0 {
		return fmt.Errorf("ANSWER_ASYNC_LEASE_SECONDS must be a positive integer")
	}
	return nil
}

func (c ChatConfig) Validate(appEnv string) error {
	if err := validateModelList("CHAT_MODELS", c.Models); err != nil {
		return err
	}
	if c.HistoryTurns < 0 {
		return fmt.Errorf("CHAT_HISTORY_TURNS must be zero or positive")
	}
	if c.HistoryTokenLimit < 0 {
		return fmt.Errorf("CHAT_HISTORY_TOKEN_LIMIT must be zero or positive")
	}
	if c.QueryRewriteTimeoutSec < 0 {
		return fmt.Errorf("CHAT_QUERY_REWRITE_TIMEOUT_SECONDS must be zero or positive")
	}
	switch c.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only.
		if appEnv == "production" {
			return fmt.Errorf("CHAT_SOURCE=local is a development adapter and must not run in production")
		}
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
		if strings.TrimSpace(c.HealthURL) != "" {
			health, err := url.Parse(c.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("CHAT_HEALTH_URL must be a valid URL")
			}
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
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/health); a non-empty value must be a URL.
	HealthURL string
	// Models lists additional routable rerank models as name[@version].
	Models []string
}

func (r RerankConfig) Validate() error {
	if err := validateModelList("RERANK_MODELS", r.Models); err != nil {
		return err
	}
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
		if strings.TrimSpace(r.HealthURL) != "" {
			health, err := url.Parse(r.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("RERANK_HEALTH_URL must be a valid URL")
			}
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
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/models); a non-empty value must be a URL.
	HealthURL string
	// IndexLockTimeoutSec bounds how long embedctl waits for locks while it
	// builds the chunks HNSW index CONCURRENTLY. Zero disables the timeout.
	IndexLockTimeoutSec int
	// IndexStatementTimeoutSec bounds one CREATE INDEX CONCURRENTLY statement.
	// Zero leaves it unlimited, which suits long builds on large tables.
	IndexStatementTimeoutSec int
}

func (e EmbeddingConfig) Validate(appEnv string) error {
	switch e.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only. A
		// worker running it in production would write non-semantic vectors.
		if appEnv == "production" {
			return fmt.Errorf("EMBEDDING_SOURCE=local is a development adapter and must not run in production")
		}
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
		if strings.TrimSpace(e.HealthURL) != "" {
			health, err := url.Parse(e.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("EMBEDDING_HEALTH_URL must be a valid URL")
			}
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
	// TenantModelRequestsPerMinute bounds model registry resolutions per
	// tenant and capability. Zero disables the limit.
	TenantModelRequestsPerMinute int
	DocumentRetentionDays        int
	AuditRetentionDays           int
	StreamEventRetentionDays     int
	// TaskHistoryRetentionDays bounds finished ingestion pipeline runs and
	// resolved dead letters. Zero keeps them forever.
	TaskHistoryRetentionDays int
	OutboxMaxAttempts        int
	OutboxBatchSize          int
	ReconcileIntervalSeconds int
	WorkerConcurrency        int
	ShutdownTimeoutSeconds   int
}

// ConnectorConfig is the M5 external-source baseline. Credentials are sealed
// with AES-GCM using the active connector key (single EncryptionKey or the
// EncryptionKeys keyring); URL fetching is SSRF-guarded, and private networks
// are never reachable in production.
type ConnectorConfig struct {
	EncryptionKey string
	// EncryptionKeys is a comma-separated kid:key keyring. Keys stay usable for
	// decrypting existing envelopes after ActiveKeyID moves to a new kid.
	EncryptionKeys       string
	ActiveKeyID          string
	AllowPrivateNetworks bool
	RequestTimeoutSec    int
	MaxManifestBytes     int64
	DefaultRatePerSecond float64
}

// ProjectionConfig controls Pipeline B generation behavior. IncrementalEnabled
// wires the per-package generation cache; false is the exact pre-cache
// full-regeneration behavior and is the rollback switch.
type ProjectionConfig struct {
	IncrementalEnabled bool
}

func (c ConnectorConfig) Validate(appEnv string) error {
	if strings.TrimSpace(c.EncryptionKey) == "" && strings.TrimSpace(c.EncryptionKeys) == "" {
		return fmt.Errorf("CONNECTOR_ENCRYPTION_KEY or CONNECTOR_ENCRYPTION_KEYS is required")
	}
	if strings.TrimSpace(c.EncryptionKeys) != "" && strings.TrimSpace(c.ActiveKeyID) == "" {
		return fmt.Errorf("CONNECTOR_ENCRYPTION_ACTIVE_KID is required when CONNECTOR_ENCRYPTION_KEYS is set")
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

// validateTrustedProxies accepts the same forms gin does: a bare IP or a CIDR.
// Invalid entries fail startup rather than silently leaving forwarding headers
// untrusted, which would attribute every request to the proxy.
func validateTrustedProxies(entries []string) error {
	for _, entry := range entries {
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q is not a valid CIDR", entry)
			}
			if ones, _ := network.Mask.Size(); ones == 0 {
				return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q trusts every peer; list the proxy network instead", entry)
			}
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q is not a valid IP address", entry)
		}
	}
	return nil
}

func (l LimitsConfig) Validate() error {
	for name, value := range map[string]int{
		"RATE_LIMIT_PER_MINUTE":        l.RateLimitPerMinute,
		"DOCUMENT_RETENTION_DAYS":      l.DocumentRetentionDays,
		"AUDIT_RETENTION_DAYS":         l.AuditRetentionDays,
		"STREAM_EVENT_RETENTION_DAYS":  l.StreamEventRetentionDays,
		"TASK_HISTORY_RETENTION_DAYS":  l.TaskHistoryRetentionDays,
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
		cfg.ConfigError = errors.Join(cfg.ConfigError, err)
	}
	return cfg
}

// defaultRetrievalConfig mirrors config/config.yaml. The file is the operator
// facing tier; these values are only the fallback used when the file is absent
// so a development stack still starts.
func defaultRetrievalConfig() RetrievalConfig {
	return RetrievalConfig{
		TopK:             8,
		EmbeddingTopK:    20,
		KeywordTopK:      20,
		VectorThreshold:  0.2,
		KeywordThreshold: 0,
		RerankEnabled:    false,
		RerankTopK:       8,
		RerankThreshold:  -10,
		// The recall probe (scripts/vector_recall_probe.py) measured that
		// over-sampling alone cannot fill a small tenant's HNSW result; relaxed
		// iterative scan can. Zero ef_search keeps pgvector's default (40).
		VectorEFSearch:      0,
		VectorOversample:    1,
		VectorIterativeScan: true,
		ContextTokenLimit:   3000,
		AdjacentExpansion:   1,
		MaxAdjacentChunks:   2,
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
	reader := &envReader{lookup: lookup}
	cfg := Config{
		APIAddr:              value(lookup, "API_ADDR", ":8080"),
		MetricsAddr:          value(lookup, "METRICS_ADDR", ""),
		OTELExporterEndpoint: value(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTELServiceName:      value(lookup, "OTEL_SERVICE_NAME", "rag-platform"),
		AppEnv:               appEnv,
		WebPublicURL:         value(lookup, "WEB_PUBLIC_URL", "http://localhost:5173"),
		TrustedProxies:       listValue(lookup, "HTTP_TRUSTED_PROXIES", nil),
		Auth: AuthConfig{
			SessionTTLSeconds: int(reader.positive("AUTH_SESSION_TTL_SECONDS", 43200)),
			JWTIssuer:         value(lookup, "JWT_ISSUER", "rag-platform"),
			JWTAudience:       value(lookup, "JWT_AUDIENCE", "rag-platform-api"),
			SigningKeys:       signingKeys,
			ActiveKeyID:       activeKeyID,
			CookieName:        value(lookup, "AUTH_COOKIE_NAME", "rag_session"),
			CookieSecure:      reader.boolean("AUTH_COOKIE_SECURE", appEnv == "production"),
			DevAuthEnabled:    reader.boolean("DEV_AUTH_ENABLED", appEnv != "production"),
			APIKeyPepper:      value(lookup, "API_KEY_PEPPER", "local-api-key-pepper-change-me"),
			OIDC: OIDCConfig{
				Enabled:       reader.boolean("OIDC_ENABLED", false),
				Issuer:        strings.TrimSuffix(value(lookup, "OIDC_ISSUER", ""), "/"),
				ClientID:      value(lookup, "OIDC_CLIENT_ID", ""),
				ClientSecret:  value(lookup, "OIDC_CLIENT_SECRET", ""),
				RedirectURL:   value(lookup, "OIDC_REDIRECT_URL", apiPublicURL+"/api/v1/auth/callback"),
				Scopes:        listValue(lookup, "OIDC_SCOPES", []string{"openid", "email", "profile"}),
				TenantClaim:   value(lookup, "OIDC_TENANT_CLAIM", "tenant_id"),
				EmailClaim:    value(lookup, "OIDC_EMAIL_CLAIM", "email"),
				NameClaim:     value(lookup, "OIDC_NAME_CLAIM", "name"),
				AutoProvision: reader.boolean("OIDC_AUTO_PROVISION", false),
				DefaultTenant: value(lookup, "OIDC_DEFAULT_TENANT", ""),
			},
		},
		Limits: LimitsConfig{
			// Zero disables the in-process limiter, so this is non-negative
			// rather than positive.
			RateLimitPerMinute:           int(reader.nonNegative("RATE_LIMIT_PER_MINUTE", 600)),
			TenantMaxDocuments:           int(reader.nonNegative("TENANT_MAX_DOCUMENTS", 0)),
			TenantMaxStorageBytes:        reader.nonNegative("TENANT_MAX_STORAGE_MB", 0) * 1024 * 1024,
			TenantChatRequestsPerDay:     int(reader.nonNegative("TENANT_CHAT_REQUESTS_PER_DAY", 0)),
			TenantModelRequestsPerMinute: int(reader.nonNegative("TENANT_MODEL_REQUESTS_PER_MINUTE", 0)),
			DocumentRetentionDays:        int(reader.positive("DOCUMENT_RETENTION_DAYS", 30)),
			AuditRetentionDays:           int(reader.nonNegative("AUDIT_RETENTION_DAYS", 180)),
			StreamEventRetentionDays:     int(reader.nonNegative("STREAM_EVENT_RETENTION_DAYS", 7)),
			TaskHistoryRetentionDays:     int(reader.nonNegative("TASK_HISTORY_RETENTION_DAYS", 90)),
			OutboxMaxAttempts:            int(reader.positive("OUTBOX_MAX_ATTEMPTS", 10)),
			OutboxBatchSize:              int(reader.positive("OUTBOX_BATCH_SIZE", 64)),
			ReconcileIntervalSeconds:     int(reader.positive("RECONCILE_INTERVAL_SECONDS", 60)),
			WorkerConcurrency:            int(reader.positive("WORKER_CONCURRENCY", 4)),
			ShutdownTimeoutSeconds:       int(reader.positive("SHUTDOWN_TIMEOUT_SECONDS", 10)),
		},
		Projection: ProjectionConfig{
			IncrementalEnabled: reader.boolean("PROJECTION_INCREMENTAL_ENABLED", true),
		},
		Connector: ConnectorConfig{
			EncryptionKey:        value(lookup, "CONNECTOR_ENCRYPTION_KEY", ""),
			EncryptionKeys:       value(lookup, "CONNECTOR_ENCRYPTION_KEYS", ""),
			ActiveKeyID:          value(lookup, "CONNECTOR_ENCRYPTION_ACTIVE_KID", ""),
			AllowPrivateNetworks: reader.boolean("CONNECTOR_ALLOW_PRIVATE_NETWORKS", false),
			RequestTimeoutSec:    int(reader.positive("CONNECTOR_REQUEST_TIMEOUT_SECONDS", 30)),
			MaxManifestBytes:     reader.positive("CONNECTOR_MAX_MANIFEST_BYTES", 2*1024*1024),
			DefaultRatePerSecond: reader.rate("CONNECTOR_REQUESTS_PER_SECOND", 2),
		},
		LogLevel:      value(lookup, "APP_LOG_LEVEL", "info"),
		TenantRLSMode: value(lookup, "TENANT_RLS_MODE", "shadow"),
		DatabaseURL:   value(lookup, "DATABASE_URL", ""),
		// The role-split URLs have no default: an unset value means the process
		// keeps running on DATABASE_URL, which only enforce mode rejects.
		MaintenanceDatabaseURL: value(lookup, "MAINTENANCE_DATABASE_URL", ""),
		AdminDatabaseURL:       value(lookup, "ADMIN_DATABASE_URL", ""),
		// Pool limits keep one replica from exhausting PostgreSQL connections.
		DatabaseMaxOpenConns:           int(reader.positive("DATABASE_MAX_OPEN_CONNS", 20)),
		DatabaseMaxIdleConns:           int(reader.nonNegative("DATABASE_MAX_IDLE_CONNS", 10)),
		DatabaseConnMaxLifetimeSeconds: int(reader.positive("DATABASE_CONN_MAX_LIFETIME_SECONDS", 1800)),
		RedisAddr:                      value(lookup, "REDIS_ADDR", "redis:6379"),
		RedisPassword:                  value(lookup, "REDIS_PASSWORD", ""),
		DocreaderAddr:                  value(lookup, "DOCREADER_GRPC_ADDR", "docreader:50051"),
		S3Endpoint:                     value(lookup, "S3_ENDPOINT", "minio:9000"),
		S3Region:                       value(lookup, "S3_REGION", "us-east-1"),
		S3Bucket:                       value(lookup, "S3_BUCKET", "rag-platform-local"),
		S3AccessKey:                    value(lookup, "S3_ACCESS_KEY", value(lookup, "MINIO_ROOT_USER", "rag-local")),
		S3SecretKey:                    value(lookup, "S3_SECRET_KEY", value(lookup, "MINIO_ROOT_PASSWORD", "change-me-local-only")),
		S3UseTLS:                       reader.boolean("S3_USE_TLS", false),
		S3ForcePathStyle:               reader.boolean("S3_FORCE_PATH_STYLE", false),
		MaxFileBytes:                   reader.positive("DOCREADER_MAX_FILE_MB", 100) * 1024 * 1024,
		TaskTimeoutSeconds:             int(reader.positive("DOCREADER_TASK_TIMEOUT_SECONDS", 120)),
		ParserSandbox:                  reader.boolean("PARSER_SANDBOX_ENABLED", true),
		ParserVersion:                  value(lookup, "PARSER_VERSION", DefaultParserVersion),
		SupportedFormats:               formats.Names(),
		RetrievalConfigFile:            value(lookup, "RETRIEVAL_CONFIG_FILE", "config/config.yaml"),
		PromptTemplateDir:              value(lookup, "PROMPT_TEMPLATE_DIR", "config/prompt_templates"),
		Retrieval:                      defaultRetrievalConfig(),
		Chat: ChatConfig{
			Source:                 strings.ToLower(value(lookup, "CHAT_SOURCE", "local")),
			BaseURL:                value(lookup, "CHAT_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:                 value(lookup, "CHAT_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:                  value(lookup, "LLM_CHAT_MODEL", "local-extractive"),
			TimeoutSec:             int(reader.positive("CHAT_TIMEOUT_SECONDS", reader.positive("LLM_REQUEST_TIMEOUT_SECONDS", 60))),
			MaxTokens:              int(reader.positive("CHAT_MAX_TOKENS", 1024)),
			HealthURL:              value(lookup, "CHAT_HEALTH_URL", ""),
			Models:                 listValue(lookup, "CHAT_MODELS", nil),
			HistoryTurns:           int(reader.nonNegative("CHAT_HISTORY_TURNS", 3)),
			HistoryTokenLimit:      int(reader.nonNegative("CHAT_HISTORY_TOKEN_LIMIT", 1200)),
			QueryRewriteEnabled:    reader.boolean("CHAT_QUERY_REWRITE_ENABLED", true),
			QueryRewriteTimeoutSec: int(reader.positive("CHAT_QUERY_REWRITE_TIMEOUT_SECONDS", 8)),
		},
		Answer: AnswerConfig{
			AsyncEnabled:   reader.boolean("ANSWER_ASYNC_ENABLED", true),
			TailTimeoutSec: int(reader.positive("ANSWER_TAIL_TIMEOUT_SECONDS", 180)),
			PollIntervalMS: int(reader.positive("ANSWER_ASYNC_POLL_INTERVAL_MS", 250)),
			LeaseSeconds:   int(reader.positive("ANSWER_ASYNC_LEASE_SECONDS", 300)),
		},
		Rerank: RerankConfig{
			Source:     strings.ToLower(value(lookup, "RERANK_SOURCE", "none")),
			BaseURL:    value(lookup, "RERANK_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:     value(lookup, "RERANK_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:      value(lookup, "LLM_RERANK_MODEL", ""),
			TimeoutSec: int(reader.positive("RERANK_TIMEOUT_SECONDS", reader.positive("LLM_REQUEST_TIMEOUT_SECONDS", 60))),
			HealthURL:  value(lookup, "RERANK_HEALTH_URL", ""),
			Models:     listValue(lookup, "RERANK_MODELS", nil),
		},
		Embedding: EmbeddingConfig{
			Source:     strings.ToLower(value(lookup, "EMBEDDING_SOURCE", "local")),
			BaseURL:    value(lookup, "EMBEDDING_BASE_URL", value(lookup, "LLM_BASE_URL", "")),
			APIKey:     value(lookup, "EMBEDDING_API_KEY", value(lookup, "LLM_API_KEY", "")),
			Model:      value(lookup, "LLM_EMBEDDING_MODEL", "local-deterministic"),
			Dimension:  int(reader.positive("EMBEDDING_DIMENSION", 3)),
			BatchSize:  int(reader.positive("EMBEDDING_BATCH_SIZE", 32)),
			TimeoutSec: int(reader.positive("EMBEDDING_TIMEOUT_SECONDS", 60)),
			HealthURL:  value(lookup, "EMBEDDING_HEALTH_URL", ""),

			IndexLockTimeoutSec:      int(reader.nonNegative("EMBED_INDEX_LOCK_TIMEOUT_SECONDS", 10)),
			IndexStatementTimeoutSec: int(reader.nonNegative("EMBED_INDEX_STATEMENT_TIMEOUT_SECONDS", 0)),
		},
		Chunk: ChunkConfig{
			Size:    int(reader.nonNegative("CHUNK_SIZE", 900)),
			Overlap: int(reader.nonNegative("CHUNK_OVERLAP", 120)),
			Markers: listValue(lookup, "CHUNK_SPLIT_MARKERS", []string{"\n\n", "\n", "。", ".", "！", "!", "？", "?"}),
			MinSize: int(reader.nonNegative("CHUNK_MIN_SIZE", 0)),
		},
	}
	cfg.ConfigError = reader.err
	return cfg
}

func (c Config) Validate(component string) error {
	if endpoint := strings.TrimSpace(c.OTELExporterEndpoint); endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT must be a valid URL")
		}
	}
	if (component == "api" || component == "worker") && strings.TrimSpace(c.OTELServiceName) == "" {
		return fmt.Errorf("OTEL_SERVICE_NAME must not be empty")
	}
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
	switch strings.ToLower(strings.TrimSpace(c.TenantRLSMode)) {
	case "off", "shadow", "enforce":
	default:
		return fmt.Errorf("TENANT_RLS_MODE must be one of off, shadow, enforce")
	}
	if strings.EqualFold(strings.TrimSpace(c.TenantRLSMode), "enforce") {
		switch component {
		case "api", "worker":
			if strings.TrimSpace(c.MaintenanceDatabaseURL) == "" {
				return fmt.Errorf("MAINTENANCE_DATABASE_URL is required for %s when TENANT_RLS_MODE=enforce", component)
			}
			role, err := databaseURLRole(c.DatabaseURL)
			if err != nil {
				return err
			}
			if role != "rag_app" {
				return fmt.Errorf("DATABASE_URL must use the rag_app role when TENANT_RLS_MODE=enforce, got %q", role)
			}
			if _, err := databaseURLRole(c.MaintenanceDatabaseURL); err != nil {
				return fmt.Errorf("MAINTENANCE_DATABASE_URL: %w", err)
			}
		case "migrate", "seed", "embedctl":
			if strings.TrimSpace(c.AdminDatabaseURL) == "" {
				return fmt.Errorf("ADMIN_DATABASE_URL is required for %s when TENANT_RLS_MODE=enforce", component)
			}
			if _, err := databaseURLRole(c.AdminDatabaseURL); err != nil {
				return fmt.Errorf("ADMIN_DATABASE_URL: %w", err)
			}
		}
	}
	// Pool limits apply to every process that opens the database, so validate
	// them before the component split.
	if c.DatabaseMaxOpenConns <= 0 {
		return fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be positive")
	}
	if c.DatabaseMaxIdleConns < 0 || c.DatabaseMaxIdleConns > c.DatabaseMaxOpenConns {
		return fmt.Errorf("DATABASE_MAX_IDLE_CONNS must be between 0 and DATABASE_MAX_OPEN_CONNS")
	}
	if c.DatabaseConnMaxLifetimeSeconds <= 0 {
		return fmt.Errorf("DATABASE_CONN_MAX_LIFETIME_SECONDS must be positive")
	}
	// Both the api and the worker can expose process metrics on a dedicated
	// listener; the port must be a real host:port in either process.
	if component == "api" || component == "worker" {
		if metricsAddr := strings.TrimSpace(c.MetricsAddr); metricsAddr != "" {
			_, port, err := net.SplitHostPort(metricsAddr)
			if err != nil {
				return fmt.Errorf("METRICS_ADDR must be a host:port listen address: %w", err)
			}
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return fmt.Errorf("METRICS_ADDR port must be a number between 1 and 65535")
			}
			if component == "api" && metricsAddr == strings.TrimSpace(c.APIAddr) {
				return fmt.Errorf("METRICS_ADDR must differ from API_ADDR")
			}
		}
	}
	if component == "api" {
		parsed, err := url.Parse(c.DatabaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("DATABASE_URL must be a valid database URL")
		}
		if c.AppEnv == "production" && !c.ParserSandbox {
			return fmt.Errorf("PARSER_SANDBOX_ENABLED must be true in production")
		}
		if err := validateTrustedProxies(c.TrustedProxies); err != nil {
			return err
		}
		if err := c.Embedding.Validate(c.AppEnv); err != nil {
			return err
		}
		if err := c.Retrieval.Validate(); err != nil {
			return err
		}
		if err := c.Chat.Validate(c.AppEnv); err != nil {
			return err
		}
		if err := c.Answer.Validate(); err != nil {
			return err
		}
		if err := c.Rerank.Validate(); err != nil {
			return err
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
		if err := c.Embedding.Validate(c.AppEnv); err != nil {
			return err
		}
		if err := c.Chat.Validate(c.AppEnv); err != nil {
			return err
		}
		if err := c.Answer.Validate(); err != nil {
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
		if err := c.Embedding.Validate(c.AppEnv); err != nil {
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

// databaseURLRole returns the login role from a database URL. Enforce mode
// checks it here so a swapped role fails before any connection is opened; the
// role's actual RLS attributes are still verified against pg_roles at startup.
func databaseURLRole(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("must be a valid database URL")
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return "", fmt.Errorf("must name a login role")
	}
	return parsed.User.Username(), nil
}

// AdminDBURL is the connection migrate, seed and embedctl use. Without a split
// role it stays on DATABASE_URL, which is the only mode where DDL and runtime
// traffic sharing one owner role is acceptable.
func (c Config) AdminDBURL() string {
	if admin := strings.TrimSpace(c.AdminDatabaseURL); admin != "" {
		return admin
	}
	return c.DatabaseURL
}

// envReader parses typed settings and records every malformed value. Falling
// back silently would make a typo indistinguishable from an unset variable:
// the operator would see the default in effect while believing the configured
// value was applied.
type envReader struct {
	lookup Lookup
	err    error
}

func (r *envReader) failf(format string, args ...any) {
	r.err = errors.Join(r.err, fmt.Errorf(format, args...))
}

func (r *envReader) raw(key string) string {
	current, ok := r.lookup(key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(current)
}

func (r *envReader) boolean(key string, fallback bool) bool {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	switch strings.ToLower(current) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		r.failf("%s must be a boolean (true/false), got %q", key, current)
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

// validateModelList accepts entries in name or name@version form. The version
// is recorded in binding audits so an in-place provider upgrade is visible.
func validateModelList(key string, entries []string) error {
	for _, entry := range entries {
		name, version, hasVersion := strings.Cut(entry, "@")
		if strings.TrimSpace(name) == "" || (hasVersion && strings.TrimSpace(version) == "") {
			return fmt.Errorf("%s entries must be model or model@version, got %q", key, entry)
		}
	}
	return nil
}

// positive reads an integer that must be strictly greater than zero.
func (r *envReader) positive(key string, fallback int64) int64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(current, 10, 64)
	if err != nil || parsed <= 0 {
		r.failf("%s must be a positive integer, got %q", key, current)
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

func (r *envReader) rate(key string, fallback float64) float64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(current, 64)
	if err != nil || parsed <= 0 {
		r.failf("%s must be a positive number, got %q", key, current)
		return fallback
	}
	return parsed
}

// nonNegative reads an integer that is allowed to be zero. Overlap, minimum
// size and retention windows are meaningful at zero, so they need their own
// parser.
func (r *envReader) nonNegative(key string, fallback int64) int64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(current, 10, 64)
	if err != nil || parsed < 0 {
		r.failf("%s must be a non-negative integer, got %q", key, current)
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
