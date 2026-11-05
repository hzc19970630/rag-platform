package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ConnectorManager owns one slice of the datasource use cases and shares the
// service dependencies through the embedded service.
type ConnectorManager struct{ *Service }

func (s *ConnectorManager) Create(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input CreateConnectorInput) (ConnectorView, error) {
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
	port, ok := ConnectorFor(connectorType)
	if !ok {
		return ConnectorView{}, apperror.Invalidf("unsupported connector type")
	}
	config, err := port.NormalizeConfig(input.Config, s.maxItemsLimit)
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

func (s *ConnectorManager) List(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]ConnectorView, error) {
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

func (s *ConnectorManager) Get(ctx context.Context, principal authz.Principal, connectorID string) (ConnectorView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return ConnectorView{}, err
	}
	return connectorView(connector), nil
}

func (s *ConnectorManager) Update(ctx context.Context, principal authz.Principal, connectorID string, input UpdateConnectorInput) (ConnectorView, error) {
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
		port, ok := ConnectorFor(connector.Type)
		if !ok {
			return ConnectorView{}, apperror.Invalidf("unsupported connector type")
		}
		config, err := port.NormalizeConfig(*input.Config, s.maxItemsLimit)
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

func (s *ConnectorManager) Delete(ctx context.Context, principal authz.Principal, connectorID string) error {
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

func (s *ConnectorManager) Permissions(ctx context.Context, principal authz.Principal, connectorID string) (PermissionMappingView, error) {
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
