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

type Service struct {
	db            *gorm.DB
	authz         *authz.Service
	cipher        *secrets.Cipher
	fetcher       *Fetcher
	ingestion     *ingestion.Service
	worker        *Worker
	maxItemsLimit int
}

func NewService(db *gorm.DB, authorizer *authz.Service, cipher *secrets.Cipher, ingestionService *ingestion.Service, fetchOptions FetchOptions, maxItemsPerRun int) *Service {
	if maxItemsPerRun <= 0 || maxItemsPerRun > hardMaxItemsPerRun {
		maxItemsPerRun = defaultMaxItemsPerRun
	}
	fetcher := NewFetcher(fetchOptions)
	service := &Service{db: db, authz: authorizer, cipher: cipher, fetcher: fetcher, ingestion: ingestionService, maxItemsLimit: maxItemsPerRun}
	service.worker = NewWorker(db, cipher, fetcher, ingestionService)
	return service
}

// ProcessSync runs one durable sync attempt. The worker entry point lives on
// the service so cmd/worker only needs the application container.
func (s *Service) ProcessSync(ctx context.Context, task SyncTask) error {
	return s.worker.Process(ctx, task)
}

func (s *Service) Create(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input CreateConnectorInput) (ConnectorView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return ConnectorView{}, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return ConnectorView{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ConnectorView{}, apperror.Invalidf("connector name is required")
	}
	if len(name) > 255 {
		return ConnectorView{}, apperror.Invalidf("connector name must be at most 255 characters")
	}
	connectorType := strings.TrimSpace(input.Type)
	if connectorType == "" {
		connectorType = TypeHTTPManifest
	}
	if connectorType != TypeHTTPManifest {
		return ConnectorView{}, apperror.Invalidf("unsupported connector type")
	}
	config, err := s.normalizeConfig(input.Config)
	if err != nil {
		return ConnectorView{}, err
	}
	if input.SyncIntervalSeconds < 0 || input.SyncIntervalSeconds > 30*24*60*60 {
		return ConnectorView{}, apperror.Invalidf("sync interval must be between 0 and 30 days")
	}
	credentialRef, err := s.sealCredential(input.Credential)
	if err != nil {
		return ConnectorView{}, err
	}
	id, err := newID("conn")
	if err != nil {
		return ConnectorView{}, err
	}
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return ConnectorView{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now()
	connector := models.Connector{
		ID: id, TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID, Name: name, Type: connectorType,
		Config: string(encodedConfig), CredentialRef: credentialRef, Enabled: enabled,
		SyncIntervalSeconds: input.SyncIntervalSeconds, CreatedBy: principal.SubjectID,
	}
	if input.SyncIntervalSeconds > 0 {
		next := now.Add(time.Duration(input.SyncIntervalSeconds) * time.Second)
		connector.NextSyncAt = &next
	}
	if err := s.db.WithContext(ctx).Create(&connector).Error; err != nil {
		if isUniqueViolation(err) {
			return ConnectorView{}, fmt.Errorf("%w: a connector with this name already exists", apperror.ErrConflict)
		}
		return ConnectorView{}, fmt.Errorf("create connector: %w", err)
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "connector_created"); err != nil {
		return ConnectorView{}, err
	}
	return connectorView(connector), nil
}

func (s *Service) List(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]ConnectorView, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var rows []models.Connector
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, knowledgeBaseID).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	views := make([]ConnectorView, 0, len(rows))
	for _, row := range rows {
		views = append(views, connectorView(row))
	}
	return views, nil
}

func (s *Service) Get(ctx context.Context, principal authz.Principal, connectorID string) (ConnectorView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return ConnectorView{}, err
	}
	return connectorView(connector), nil
}

func (s *Service) Update(ctx context.Context, principal authz.Principal, connectorID string, input UpdateConnectorInput) (ConnectorView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return ConnectorView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return ConnectorView{}, err
	}
	updates := map[string]any{"updated_at": time.Now()}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return ConnectorView{}, apperror.Invalidf("connector name cannot be empty")
		}
		if len(name) > 255 {
			return ConnectorView{}, apperror.Invalidf("connector name must be at most 255 characters")
		}
		updates["name"] = name
	}
	if input.Config != nil {
		config, err := s.normalizeConfig(*input.Config)
		if err != nil {
			return ConnectorView{}, err
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return ConnectorView{}, err
		}
		updates["config"] = string(encoded)
	}
	if input.Credential != nil {
		credentialRef, err := s.sealCredential(input.Credential)
		if err != nil {
			return ConnectorView{}, err
		}
		updates["credential_ref"] = credentialRef
	}
	if input.SyncIntervalSeconds != nil {
		interval := *input.SyncIntervalSeconds
		if interval < 0 || interval > 30*24*60*60 {
			return ConnectorView{}, apperror.Invalidf("sync interval must be between 0 and 30 days")
		}
		updates["sync_interval_seconds"] = interval
		if interval == 0 {
			updates["next_sync_at"] = nil
		} else {
			next := time.Now().Add(time.Duration(interval) * time.Second)
			updates["next_sync_at"] = &next
		}
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
		if !*input.Enabled {
			updates["next_sync_at"] = nil
		}
	}
	if err := s.db.WithContext(ctx).Model(&models.Connector{}).Where("tenant_id = ? AND id = ?", principal.TenantID, connector.ID).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			return ConnectorView{}, fmt.Errorf("%w: a connector with this name already exists", apperror.ErrConflict)
		}
		return ConnectorView{}, fmt.Errorf("update connector: %w", err)
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "connector_updated"); err != nil {
		return ConnectorView{}, err
	}
	connector, err = s.loadConnector(ctx, principal.TenantID, connector.ID)
	if err != nil {
		return ConnectorView{}, err
	}
	return connectorView(connector), nil
}

func (s *Service) Delete(ctx context.Context, principal authz.Principal, connectorID string) error {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "delete")
	if err != nil {
		return err
	}
	var active int64
	if err := s.db.WithContext(ctx).Model(&models.SyncRun{}).Where("connector_id = ? AND status IN ?", connector.ID, []string{StatusPending, StatusRunning}).Count(&active).Error; err != nil {
		return fmt.Errorf("check active sync runs: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("%w: cancel the active sync run before deleting the connector", apperror.ErrConflict)
	}
	if err := s.db.WithContext(ctx).Delete(&models.Connector{}, "tenant_id = ? AND id = ?", principal.TenantID, connector.ID).Error; err != nil {
		return fmt.Errorf("delete connector: %w", err)
	}
	return s.audit(ctx, principal, "delete", connector.ID, "connector_deleted")
}

func (s *Service) TriggerSync(ctx context.Context, principal authz.Principal, connectorID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	if !connector.Enabled {
		return RunView{}, apperror.Invalidf("connector must be enabled before syncing")
	}
	run, err := s.createRun(ctx, connector, ModeManual, "", "{}", principal.SubjectID)
	if err != nil {
		return RunView{}, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_triggered:"+run.ID); err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *Service) ListRuns(ctx context.Context, principal authz.Principal, connectorID string, limit int) ([]RunView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var runs []models.SyncRun
	if err := s.db.WithContext(ctx).Where("connector_id = ?", connector.ID).Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list sync runs: %w", err)
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		views = append(views, runView(run))
	}
	return views, nil
}

func (s *Service) GetRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return RunView{}, err
	}
	var run models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	return runView(run), nil
}

func (s *Service) RetryRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	var source models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	switch source.Status {
	case StatusFailed, StatusPartial, StatusCancelled:
	default:
		return RunView{}, fmt.Errorf("%w: only a failed, partial or cancelled run can be retried", apperror.ErrConflict)
	}
	cursor := source.Cursor
	if strings.TrimSpace(cursor) == "" {
		cursor = "{}"
	}
	run, err := s.createRun(ctx, connector, ModeResume, source.ID, cursor, principal.SubjectID)
	if err != nil {
		return RunView{}, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_retried:"+source.ID+"->"+run.ID); err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *Service) CancelRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	var run models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	now := time.Now()
	switch run.Status {
	case StatusPending:
		if err := s.db.WithContext(ctx).Model(&models.SyncRun{}).Where("id = ? AND status = ?", run.ID, StatusPending).
			Updates(map[string]any{"status": StatusCancelled, "cancel_requested": true, "finished_at": &now, "updated_at": now}).Error; err != nil {
			return RunView{}, fmt.Errorf("cancel sync run: %w", err)
		}
	case StatusRunning:
		if err := s.db.WithContext(ctx).Model(&models.SyncRun{}).Where("id = ? AND status = ?", run.ID, StatusRunning).
			Updates(map[string]any{"cancel_requested": true, "updated_at": now}).Error; err != nil {
			return RunView{}, fmt.Errorf("cancel sync run: %w", err)
		}
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_cancelled:"+run.ID); err != nil {
		return RunView{}, err
	}
	run, err = s.reloadRun(ctx, run.ID)
	if err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *Service) CancelConnector(ctx context.Context, principal authz.Principal, connectorID string) (int, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return 0, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return 0, err
	}
	count, err := s.cancelActiveRuns(ctx, []string{connector.ID})
	if err != nil {
		return 0, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_cancelled_connector"); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) CancelKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (int, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return 0, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return 0, err
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&models.Connector{}).Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, knowledgeBaseID).Pluck("id", &ids).Error; err != nil {
		return 0, fmt.Errorf("list knowledge base connectors: %w", err)
	}
	count, err := s.cancelActiveRuns(ctx, ids)
	if err != nil {
		return 0, err
	}
	if err := s.audit(ctx, principal, "write", knowledgeBaseID, "sync_cancelled_knowledge_base"); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) Permissions(ctx context.Context, principal authz.Principal, connectorID string) (PermissionMappingView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return PermissionMappingView{}, err
	}
	var knowledgeBase models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, connector.KnowledgeBaseID).First(&knowledgeBase).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PermissionMappingView{}, apperror.ErrNotFound
		}
		return PermissionMappingView{}, fmt.Errorf("load knowledge base: %w", err)
	}
	var members []models.KnowledgeBaseMember
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ?", connector.KnowledgeBaseID).Order("user_id").Find(&members).Error; err != nil {
		return PermissionMappingView{}, fmt.Errorf("list knowledge base members: %w", err)
	}
	config := decodeConfig(connector.Config)
	views := make([]PermissionMemberView, 0, len(members))
	for _, member := range members {
		views = append(views, PermissionMemberView{UserID: member.UserID, Role: member.Role})
	}
	return PermissionMappingView{
		ConnectorID: connector.ID, KnowledgeBaseID: connector.KnowledgeBaseID,
		SourceACL: "none", EffectiveACL: "knowledge_base_members", ACLMode: config.ACLMode,
		ACLVersion: knowledgeBase.ACLVersion, Members: views,
	}, nil
}

// ScheduleDue turns due connectors into durable PENDING runs. It is called from
// the worker maintenance loop, not from the API request path.
func (s *Service) ScheduleDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now()
	var connectors []models.Connector
	if err := s.db.WithContext(ctx).
		Where("enabled = TRUE AND next_sync_at IS NOT NULL AND next_sync_at <= ?", now).
		Order("next_sync_at").Limit(limit).Find(&connectors).Error; err != nil {
		return 0, fmt.Errorf("list due connectors: %w", err)
	}
	triggered := 0
	var firstErr error
	for _, connector := range connectors {
		_, err := s.createRun(ctx, connector, ModeScheduled, "", "{}", "scheduler")
		if err != nil && !errors.Is(err, apperror.ErrConflict) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err == nil {
			triggered++
		}
		var next *time.Time
		if connector.SyncIntervalSeconds > 0 {
			value := now.Add(time.Duration(connector.SyncIntervalSeconds) * time.Second)
			next = &value
		}
		if err := s.db.WithContext(ctx).Model(&models.Connector{}).Where("id = ?", connector.ID).
			Updates(map[string]any{"next_sync_at": next, "updated_at": now}).Error; err != nil && firstErr == nil {
			firstErr = fmt.Errorf("reschedule connector %s: %w", connector.ID, err)
		}
	}
	return triggered, firstErr
}

// ExpireStaleRuns recovers work that Redis lost after the run row was written.
// Without it the one-active-run index would block that connector forever.
func (s *Service) ExpireStaleRuns(ctx context.Context, now time.Time) (int, error) {
	result := s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("status = ? AND lease_until IS NOT NULL AND lease_until < ?", StatusRunning, now.Add(-5*time.Minute)).
		Updates(map[string]any{"status": StatusFailed, "error_summary": "worker lease expired", "finished_at": &now, "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("expire stale sync runs: %w", result.Error)
	}
	expired := int(result.RowsAffected)
	stalePending := s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("status = ? AND created_at < ?", StatusPending, now.Add(-time.Hour)).
		Updates(map[string]any{"status": StatusFailed, "error_summary": "sync dispatch timed out", "finished_at": &now, "updated_at": now})
	if stalePending.Error != nil {
		return expired, fmt.Errorf("expire pending sync runs: %w", stalePending.Error)
	}
	return expired + int(stalePending.RowsAffected), nil
}

// FailRun is the terminal hook for an exhausted Asynq retry budget. It only
// touches active runs, so a late failure cannot overwrite a completed result.
func (s *Service) FailRun(ctx context.Context, task SyncTask, cause error) error {
	if task.SyncRunID == "" {
		return nil
	}
	message := "sync worker exhausted retries"
	if cause != nil {
		message = truncate("sync worker exhausted retries: "+cause.Error(), 2000)
	}
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("id = ? AND tenant_id = ? AND status IN ?", task.SyncRunID, task.TenantID, []string{StatusPending, StatusRunning}).
		Updates(map[string]any{"status": StatusFailed, "error_summary": message, "finished_at": &now, "updated_at": now}).Error
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
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

func (s *Service) cancelActiveRuns(ctx context.Context, connectorIDs []string) (int, error) {
	if len(connectorIDs) == 0 {
		return 0, nil
	}
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("connector_id IN ? AND status = ?", connectorIDs, StatusPending).
		Updates(map[string]any{"status": StatusCancelled, "cancel_requested": true, "finished_at": &now, "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("cancel pending sync runs: %w", result.Error)
	}
	running := s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("connector_id IN ? AND status = ?", connectorIDs, StatusRunning).
		Updates(map[string]any{"cancel_requested": true, "updated_at": now})
	if running.Error != nil {
		return 0, fmt.Errorf("cancel running sync runs: %w", running.Error)
	}
	return int(result.RowsAffected + running.RowsAffected), nil
}

func (s *Service) normalizeConfig(input ConnectorConfig) (ConnectorConfig, error) {
	parsed, err := ParseManifestURL(input.ManifestURL)
	if err != nil {
		return ConnectorConfig{}, err
	}
	_ = parsed
	if input.RequestsPerSecond < 0 || input.RequestsPerSecond > 50 {
		return ConnectorConfig{}, apperror.Invalidf("requests per second must be between 0 and 50")
	}
	maxItems := input.MaxItemsPerRun
	if maxItems == 0 {
		maxItems = min(s.maxItemsLimit, defaultMaxItemsPerRun)
	}
	if maxItems < 1 || maxItems > s.maxItemsLimit {
		return ConnectorConfig{}, apperror.Invalidf("max items per run must be between 1 and %d", s.maxItemsLimit)
	}
	aclMode := strings.TrimSpace(input.ACLMode)
	if aclMode == "" {
		aclMode = ACLModeKnowledgeBase
	}
	if aclMode != ACLModeKnowledgeBase {
		return ConnectorConfig{}, apperror.Invalidf("unsupported ACL mode")
	}
	return ConnectorConfig{
		ManifestURL: strings.TrimSpace(input.ManifestURL), RequestsPerSecond: input.RequestsPerSecond,
		MaxItemsPerRun: maxItems, ACLMode: aclMode,
	}, nil
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
	allowed, err := s.authz.Authorize(ctx, principal, action, "/knowledge-bases/"+id)
	if err != nil {
		return models.KnowledgeBase{}, err
	}
	if !allowed {
		return models.KnowledgeBase{}, apperror.ErrForbidden
	}
	var knowledgeBase models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&knowledgeBase).Error; err != nil {
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
