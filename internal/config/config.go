package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/platform/formats"
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
	// Profile selects a deployment preset (dev/eval/on-prem/saas). It only
	// supplies defaults for unset variables; an explicit env variable always
	// wins, so adding a profile never changes an existing deployment.
	Profile     string
	LogLevel    string
	DatabaseURL string
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

func Load() Config {
	cfg := LoadFrom(os.LookupEnv)
	if err := applyRetrievalDefaults(&cfg); err != nil {
		cfg.ConfigError = errors.Join(cfg.ConfigError, err)
	}
	return cfg
}

func LoadFrom(lookup Lookup) Config {
	appEnv := strings.ToLower(strings.TrimSpace(value(lookup, "APP_ENV", "development")))
	profile := strings.ToLower(strings.TrimSpace(value(lookup, "CONFIG_PROFILE", "dev")))
	apiPublicURL := value(lookup, "API_PUBLIC_URL", "http://localhost:8082")
	signingKeys := splitValue(lookup, "JWT_SIGNING_KEYS", nil)
	signingKeysExplicit := len(signingKeys) > 0
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
		Profile:              profile,
		WebPublicURL:         value(lookup, "WEB_PUBLIC_URL", "http://localhost:5173"),
		TrustedProxies:       listValue(lookup, "HTTP_TRUSTED_PROXIES", nil),
		Auth: AuthConfig{
			SessionTTLSeconds:   int(reader.positive("AUTH_SESSION_TTL_SECONDS", 43200)),
			JWTIssuer:           value(lookup, "JWT_ISSUER", "rag-platform"),
			JWTAudience:         value(lookup, "JWT_AUDIENCE", "rag-platform-api"),
			SigningKeys:         signingKeys,
			SigningKeysExplicit: signingKeysExplicit,
			ActiveKeyID:         activeKeyID,
			CookieName:          value(lookup, "AUTH_COOKIE_NAME", "rag_session"),
			CookieSecure:        reader.boolean("AUTH_COOKIE_SECURE", profileBoolDefault(profile, "AUTH_COOKIE_SECURE", appEnv == "production")),
			DevAuthEnabled:      reader.boolean("DEV_AUTH_ENABLED", profileBoolDefault(profile, "DEV_AUTH_ENABLED", appEnv != "production")),
			APIKeyPepper:        value(lookup, "API_KEY_PEPPER", "local-api-key-pepper-change-me"),
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
		TenantRLSMode: value(lookup, "TENANT_RLS_MODE", profileStringDefault(profile, "TENANT_RLS_MODE", "shadow")),
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
			Source:                 strings.ToLower(value(lookup, "CHAT_SOURCE", profileStringDefault(profile, "CHAT_SOURCE", "local"))),
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
			AsyncEnabled:   reader.boolean("ANSWER_ASYNC_ENABLED", profileBoolDefault(profile, "ANSWER_ASYNC_ENABLED", true)),
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
			Source:     strings.ToLower(value(lookup, "EMBEDDING_SOURCE", profileStringDefault(profile, "EMBEDDING_SOURCE", "local"))),
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
	switch c.Profile {
	case "dev", "eval", "on-prem", "saas":
	default:
		return fmt.Errorf("CONFIG_PROFILE must be one of dev, eval, on-prem, saas, got %q", c.Profile)
	}
	switch c.AppEnv {
	case "development", "test", "production":
	default:
		return fmt.Errorf("APP_ENV must be one of development, test, production, got %q", c.AppEnv)
	}
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
	if c.AppEnv == "production" {
		if component == "api" || component == "worker" {
			if isRepositoryDefaultSecret(c.S3AccessKey) {
				return fmt.Errorf("S3_ACCESS_KEY must not use a repository default value in production")
			}
			if isRepositoryDefaultSecret(c.S3SecretKey) {
				return fmt.Errorf("S3_SECRET_KEY must not use a repository default value in production")
			}
		}
		for _, candidate := range []struct {
			name string
			raw  string
		}{
			{"DATABASE_URL", c.DatabaseURL},
			{"MAINTENANCE_DATABASE_URL", c.MaintenanceDatabaseURL},
			{"ADMIN_DATABASE_URL", c.AdminDatabaseURL},
		} {
			if strings.TrimSpace(candidate.raw) == "" {
				continue
			}
			password, err := databaseURLPassword(candidate.raw)
			if err != nil {
				return fmt.Errorf("%s: %w", candidate.name, err)
			}
			if isRepositoryDefaultSecret(password) {
				return fmt.Errorf("%s must not use the repository default database password in production", candidate.name)
			}
		}
	}
	if c.AppEnv == "production" && !strings.EqualFold(strings.TrimSpace(c.TenantRLSMode), "enforce") {
		return fmt.Errorf("TENANT_RLS_MODE must be enforce in production")
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
			maintenanceRole, err := databaseURLRole(c.MaintenanceDatabaseURL)
			if err != nil {
				return fmt.Errorf("MAINTENANCE_DATABASE_URL: %w", err)
			}
			if maintenanceRole == role {
				return fmt.Errorf("MAINTENANCE_DATABASE_URL must use a different role than DATABASE_URL when TENANT_RLS_MODE=enforce")
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
			host, port, err := net.SplitHostPort(metricsAddr)
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
			if c.AppEnv == "production" && !isLoopbackOrPrivateHost(host) {
				return fmt.Errorf("METRICS_ADDR must bind to a loopback or private address in production, got %q", host)
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

// isLoopbackOrPrivateHost accepts only addresses that a production metrics
// listener can bind without exposing the endpoint on a public interface. A
// hostname other than localhost is rejected because it cannot be verified
// without DNS resolution at startup.
func isLoopbackOrPrivateHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// profileStringDefault supplies a preset only when the operator did not set the
// variable; explicit environment values always win.
func profileStringDefault(profile, key, fallback string) string {
	switch key {
	case "CHAT_SOURCE", "EMBEDDING_SOURCE":
		if profile == "eval" {
			return "local"
		}
	case "TENANT_RLS_MODE":
		switch profile {
		case "eval":
			return "off"
		case "on-prem", "saas":
			return "enforce"
		}
	}
	return fallback
}

func profileBoolDefault(profile, key string, fallback bool) bool {
	switch key {
	case "DEV_AUTH_ENABLED":
		if profile == "on-prem" || profile == "saas" {
			return false
		}
	case "AUTH_COOKIE_SECURE":
		if profile == "saas" {
			return true
		}
	case "ANSWER_ASYNC_ENABLED":
		switch profile {
		case "eval":
			return false
		case "on-prem", "saas":
			return true
		}
	}
	return fallback
}
