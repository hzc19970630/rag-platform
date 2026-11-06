package config

import (
	"fmt"
	"strings"
)

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
