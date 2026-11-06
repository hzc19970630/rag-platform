package datasource

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/secrets"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	TypeHTTPManifest = "http_manifest"

	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusPartial   = "PARTIAL"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"

	ItemActive     = "ACTIVE"
	ItemTombstoned = "TOMBSTONED"

	ModeManual    = "manual"
	ModeScheduled = "scheduled"
	ModeResume    = "resume"

	ACLModeKnowledgeBase = "knowledge_base"

	defaultMaxItemsPerRun = 500
	hardMaxItemsPerRun    = 5000
)

// ConnectorConfig is the tenant-editable, non-secret half of a connector.
// Resolved settings (defaults applied) are what the API returns.
type ConnectorConfig struct {
	ManifestURL       string  `json:"manifestUrl"`
	RequestsPerSecond float64 `json:"requestsPerSecond,omitempty"`
	MaxItemsPerRun    int     `json:"maxItemsPerRun"`
	// ACLMode is the source-to-platform permission mapping. The HTTP manifest
	// connector does not carry per-item identities, so the only supported mode
	// is inheriting the knowledge-base ACL.
	ACLMode string `json:"aclMode"`
}

type CredentialInput struct {
	Type     string `json:"type"`
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type CreateConnectorInput struct {
	Name                string           `json:"name"`
	Type                string           `json:"type"`
	Config              ConnectorConfig  `json:"config"`
	Credential          *CredentialInput `json:"credential"`
	SyncIntervalSeconds int              `json:"syncIntervalSeconds"`
	Enabled             *bool            `json:"enabled"`
}

type UpdateConnectorInput struct {
	Name                *string          `json:"name"`
	Config              *ConnectorConfig `json:"config"`
	Credential          *CredentialInput `json:"credential"`
	SyncIntervalSeconds *int             `json:"syncIntervalSeconds"`
	Enabled             *bool            `json:"enabled"`
}

type ConnectorView struct {
	ID                  string          `json:"id"`
	TenantID            string          `json:"tenantId"`
	KnowledgeBaseID     string          `json:"knowledgeBaseId"`
	Name                string          `json:"name"`
	Type                string          `json:"type"`
	Config              ConnectorConfig `json:"config"`
	Enabled             bool            `json:"enabled"`
	SyncIntervalSeconds int             `json:"syncIntervalSeconds"`
	NextSyncAt          *time.Time      `json:"nextSyncAt,omitempty"`
	LastSyncAt          *time.Time      `json:"lastSyncAt,omitempty"`
	HasCredential       bool            `json:"hasCredential"`
	CreatedBy           string          `json:"createdBy"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type RunView struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenantId"`
	ConnectorID     string          `json:"connectorId"`
	KnowledgeBaseID string          `json:"knowledgeBaseId"`
	Mode            string          `json:"mode"`
	Status          string          `json:"status"`
	Cursor          json.RawMessage `json:"cursor"`
	ResumeOfRunID   string          `json:"resumeOfRunId,omitempty"`
	TriggeredBy     string          `json:"triggeredBy"`
	ItemsTotal      int             `json:"itemsTotal"`
	ItemsCreated    int             `json:"itemsCreated"`
	ItemsUpdated    int             `json:"itemsUpdated"`
	ItemsDeleted    int             `json:"itemsDeleted"`
	ItemsSkipped    int             `json:"itemsSkipped"`
	ItemsFailed     int             `json:"itemsFailed"`
	ErrorSummary    string          `json:"errorSummary,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	Attempts        int             `json:"attempts"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

type PermissionMemberView struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

// PermissionMappingView states explicitly that the source has no per-item ACL
// and that synced documents are governed by the knowledge-base membership. It
// is the contract operators see instead of an invented document-level ACL.
type PermissionMappingView struct {
	ConnectorID     string                 `json:"connectorId"`
	KnowledgeBaseID string                 `json:"knowledgeBaseId"`
	SourceACL       string                 `json:"sourceAcl"`
	EffectiveACL    string                 `json:"effectiveAcl"`
	ACLMode         string                 `json:"aclMode"`
	ACLVersion      int64                  `json:"aclVersion"`
	Members         []PermissionMemberView `json:"members"`
}

// Service is the dependency container shared by the datasource managers.
// Connector management, sync runs and maintenance have separate entry points
// but still share the tenant boundary, credential cipher and ingestion port.
type Service struct {
	db            *gorm.DB
	authz         *authz.Service
	cipher        *secrets.Cipher
	fetcher       *Fetcher
	ingestion     *ingestion.Service
	worker        *Worker
	maxItemsLimit int

	connectors  *ConnectorManager
	runs        *SyncRunManager
	maintenance *MaintenanceManager
}

func NewService(db *gorm.DB, authorizer *authz.Service, cipher *secrets.Cipher, ingestionService *ingestion.Service, fetchOptions FetchOptions, maxItemsPerRun int) *Service {
	if maxItemsPerRun <= 0 || maxItemsPerRun > hardMaxItemsPerRun {
		maxItemsPerRun = defaultMaxItemsPerRun
	}
	fetcher := NewFetcher(fetchOptions)
	service := &Service{db: db, authz: authorizer, cipher: cipher, fetcher: fetcher, ingestion: ingestionService, maxItemsLimit: maxItemsPerRun}
	service.worker = NewWorker(db, cipher, fetcher, ingestionService)
	service.connectors = &ConnectorManager{Service: service}
	service.runs = &SyncRunManager{Service: service}
	service.maintenance = &MaintenanceManager{Service: service}
	return service
}

// Connectors, Runs and Maintenance expose the split use-case surface. The
// accessors stay lazy so tests can construct a Service with only the
// dependencies the exercised slice needs.
func (s *Service) Connectors() *ConnectorManager {
	if s.connectors == nil {
		s.connectors = &ConnectorManager{Service: s}
	}
	return s.connectors
}

func (s *Service) Runs() *SyncRunManager {
	if s.runs == nil {
		s.runs = &SyncRunManager{Service: s}
	}
	return s.runs
}

func (s *Service) Maintenance() *MaintenanceManager {
	if s.maintenance == nil {
		s.maintenance = &MaintenanceManager{Service: s}
	}
	return s.maintenance
}

// ProcessSync runs one durable sync attempt. The worker entry point lives on
// the service so cmd/worker only needs the application container.
func (s *Service) ProcessSync(ctx context.Context, task SyncTask) error {
	return s.worker.Process(ctx, task)
}

func (s *Service) createRun(ctx context.Context, connector models.Connector, mode, resumeOf, cursor, triggeredBy string) (models.SyncRun, error) {
	if strings.TrimSpace(cursor) == "" {
		cursor = "{}"
	}
	id, err := newID("sync")
	if err != nil {
		return models.SyncRun{}, err
	}
	run := models.SyncRun{
		ID: id, TenantID: connector.TenantID, ConnectorID: connector.ID, KnowledgeBaseID: connector.KnowledgeBaseID,
		Mode: mode, Status: StatusPending, Cursor: cursor, TriggeredBy: triggeredBy,
	}
	if strings.TrimSpace(resumeOf) != "" {
		run.ResumeOfRunID = &resumeOf
	}
	task := SyncTask{
		SchemaVersion: SchemaTask, TenantID: connector.TenantID, ConnectorID: connector.ID, SyncRunID: run.ID,
		IdempotencyKey: "connector-sync:" + run.ID, Queue: QueueMaintenance, CreatedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return models.SyncRun{}, fmt.Errorf("marshal sync task: %w", err)
	}
	err = tenantdb.InTx(ctx, s.db, connector.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		return tx.Create(&models.OutboxEvent{
			TenantID: connector.TenantID, AggregateType: "connector", AggregateID: connector.ID,
			EventType: "connector.sync", IdempotencyKey: "sync-outbox:" + run.ID, Payload: string(payload),
		}).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			return models.SyncRun{}, fmt.Errorf("%w: a sync run is already active for this connector", apperror.ErrConflict)
		}
		return models.SyncRun{}, fmt.Errorf("create sync run: %w", err)
	}
	return run, nil
}

func (s *Service) cancelActiveRuns(ctx context.Context, tenantID string, connectorIDs []string) (int, error) {
	if len(connectorIDs) == 0 {
		return 0, nil
	}
	now := time.Now()
	result := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.SyncRun{}).
		Where("connector_id IN ? AND status = ?", connectorIDs, StatusPending).
		Updates(map[string]any{"status": StatusCancelled, "cancel_requested": true, "finished_at": &now, "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("cancel pending sync runs: %w", result.Error)
	}
	running := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.SyncRun{}).
		Where("connector_id IN ? AND status = ?", connectorIDs, StatusRunning).
		Updates(map[string]any{"cancel_requested": true, "updated_at": now})
	if running.Error != nil {
		return 0, fmt.Errorf("cancel running sync runs: %w", running.Error)
	}
	return int(result.RowsAffected + running.RowsAffected), nil
}

func (s *Service) sealCredential(input *CredentialInput) (string, error) {
	if input == nil {
		return "", nil
	}
	credential := Credentials{Type: strings.ToLower(strings.TrimSpace(input.Type))}
	switch credential.Type {
	case "", "none":
		return "", nil
	case "bearer":
		credential.Token = strings.TrimSpace(input.Token)
		if credential.Token == "" {
			return "", apperror.Invalidf("bearer credential token is required")
		}
	case "basic":
		credential.Username = strings.TrimSpace(input.Username)
		if credential.Username == "" || input.Password == "" {
			return "", apperror.Invalidf("basic credential username and password are required")
		}
		credential.Password = input.Password
	default:
		return "", apperror.Invalidf("unsupported credential type")
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return "", fmt.Errorf("encode connector credential: %w", err)
	}
	encrypted, err := s.cipher.Encrypt(encoded)
	if err != nil {
		return "", fmt.Errorf("encrypt connector credential: %w", err)
	}
	return encrypted, nil
}

func (s *Service) authorizedKnowledgeBase(ctx context.Context, principal authz.Principal, id, action string) (models.KnowledgeBase, error) {
	if _, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: id}); err != nil {
		return models.KnowledgeBase{}, err
	}
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, id, action)
	if err != nil {
		return models.KnowledgeBase{}, err
	}
	if !allowed {
		return models.KnowledgeBase{}, apperror.ErrForbidden
	}
	var knowledgeBase models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&knowledgeBase).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.KnowledgeBase{}, apperror.ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("load knowledge base: %w", err)
	}
	return knowledgeBase, nil
}

func (s *Service) authorizedConnector(ctx context.Context, principal authz.Principal, id, action string) (models.Connector, error) {
	connector, err := s.loadConnector(ctx, principal.TenantID, id)
	if err != nil {
		return models.Connector{}, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, connector.KnowledgeBaseID, action); err != nil {
		return models.Connector{}, err
	}
	return connector, nil
}

func (s *Service) loadConnector(ctx context.Context, tenantID, id string) (models.Connector, error) {
	var connector models.Connector
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&connector).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Connector{}, apperror.ErrNotFound
		}
		return models.Connector{}, fmt.Errorf("load connector: %w", err)
	}
	return connector, nil
}

func (s *Service) reloadRun(ctx context.Context, id string) (models.SyncRun, error) {
	var run models.SyncRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", id).Error; err != nil {
		return models.SyncRun{}, fmt.Errorf("reload sync run: %w", err)
	}
	return run, nil
}

func (s *Service) audit(ctx context.Context, principal authz.Principal, action, connectorID, reason string) error {
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action,
		ResourceType: "connector", ResourceID: connectorID, Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func connectorView(connector models.Connector) ConnectorView {
	return ConnectorView{
		ID: connector.ID, TenantID: connector.TenantID, KnowledgeBaseID: connector.KnowledgeBaseID,
		Name: connector.Name, Type: connector.Type, Config: decodeConfig(connector.Config),
		Enabled: connector.Enabled, SyncIntervalSeconds: connector.SyncIntervalSeconds,
		NextSyncAt: connector.NextSyncAt, LastSyncAt: connector.LastSyncAt,
		HasCredential: connector.CredentialRef != "", CreatedBy: connector.CreatedBy,
		CreatedAt: connector.CreatedAt, UpdatedAt: connector.UpdatedAt,
	}
}

func decodeConfig(raw string) ConnectorConfig {
	config := ConnectorConfig{ACLMode: ACLModeKnowledgeBase}
	if strings.TrimSpace(raw) == "" {
		return config
	}
	_ = json.Unmarshal([]byte(raw), &config)
	if config.ACLMode == "" {
		config.ACLMode = ACLModeKnowledgeBase
	}
	return config
}

func runView(run models.SyncRun) RunView {
	cursor := json.RawMessage("{}")
	if strings.TrimSpace(run.Cursor) != "" && json.Valid([]byte(run.Cursor)) {
		cursor = json.RawMessage(run.Cursor)
	}
	resumeOf := ""
	if run.ResumeOfRunID != nil {
		resumeOf = *run.ResumeOfRunID
	}
	return RunView{
		ID: run.ID, TenantID: run.TenantID, ConnectorID: run.ConnectorID, KnowledgeBaseID: run.KnowledgeBaseID,
		Mode: run.Mode, Status: run.Status, Cursor: cursor, ResumeOfRunID: resumeOf, TriggeredBy: run.TriggeredBy,
		ItemsTotal: run.ItemsTotal, ItemsCreated: run.ItemsCreated, ItemsUpdated: run.ItemsUpdated,
		ItemsDeleted: run.ItemsDeleted, ItemsSkipped: run.ItemsSkipped, ItemsFailed: run.ItemsFailed,
		ErrorSummary: run.ErrorSummary, CancelRequested: run.CancelRequested, Attempts: run.Attempts,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
	}
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "SQLSTATE 23505") || strings.Contains(message, "duplicate key value violates unique constraint")
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func stableDocumentID(connectorID, externalID string) string {
	digest := sha256.Sum256([]byte("connector\x00" + connectorID + "\x00" + externalID))
	return "doc-" + hex.EncodeToString(digest[:12])
}

func stableSyncKey(connectorID, externalID, contentHash string) string {
	digest := sha256.Sum256([]byte("connector-sync\x00" + connectorID + "\x00" + externalID + "\x00" + contentHash))
	return "connector:" + hex.EncodeToString(digest[:24])
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
