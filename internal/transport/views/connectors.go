package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/datasource"
)

func Connector(view datasource.ConnectorView) httpapi.Connector {
	return httpapi.Connector{
		ID:                  view.ID,
		TenantID:            view.TenantID,
		KnowledgeBaseID:     view.KnowledgeBaseID,
		Name:                view.Name,
		Type:                view.Type,
		Config:              ConnectorConfig(view.Config),
		Enabled:             view.Enabled,
		SyncIntervalSeconds: view.SyncIntervalSeconds,
		NextSyncAt:          view.NextSyncAt,
		LastSyncAt:          view.LastSyncAt,
		HasCredential:       view.HasCredential,
		CreatedBy:           view.CreatedBy,
		CreatedAt:           view.CreatedAt,
		UpdatedAt:           view.UpdatedAt,
	}
}

func Connectors(rows []datasource.ConnectorView) []httpapi.Connector {
	items := make([]httpapi.Connector, 0, len(rows))
	for _, row := range rows {
		items = append(items, Connector(row))
	}
	return items
}

func ConnectorConfig(view datasource.ConnectorConfig) httpapi.ConnectorConfig {
	return httpapi.ConnectorConfig{
		ManifestURL:       view.ManifestURL,
		RequestsPerSecond: view.RequestsPerSecond,
		MaxItemsPerRun:    view.MaxItemsPerRun,
		ACLMode:           view.ACLMode,
	}
}

func ConnectorRun(view datasource.RunView) httpapi.ConnectorRun {
	return httpapi.ConnectorRun{
		ID:              view.ID,
		TenantID:        view.TenantID,
		ConnectorID:     view.ConnectorID,
		KnowledgeBaseID: view.KnowledgeBaseID,
		Mode:            view.Mode,
		Status:          view.Status,
		Cursor:          view.Cursor,
		ResumeOfRunID:   view.ResumeOfRunID,
		TriggeredBy:     view.TriggeredBy,
		ItemsTotal:      view.ItemsTotal,
		ItemsCreated:    view.ItemsCreated,
		ItemsUpdated:    view.ItemsUpdated,
		ItemsDeleted:    view.ItemsDeleted,
		ItemsSkipped:    view.ItemsSkipped,
		ItemsFailed:     view.ItemsFailed,
		ErrorSummary:    view.ErrorSummary,
		CancelRequested: view.CancelRequested,
		Attempts:        view.Attempts,
		StartedAt:       view.StartedAt,
		FinishedAt:      view.FinishedAt,
		CreatedAt:       view.CreatedAt,
	}
}

func ConnectorRuns(rows []datasource.RunView) []httpapi.ConnectorRun {
	items := make([]httpapi.ConnectorRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, ConnectorRun(row))
	}
	return items
}

func ConnectorPermissions(view datasource.PermissionMappingView) httpapi.ConnectorPermissions {
	members := make([]httpapi.PermissionMember, 0, len(view.Members))
	for _, member := range view.Members {
		members = append(members, httpapi.PermissionMember{UserID: member.UserID, Role: member.Role})
	}
	return httpapi.ConnectorPermissions{
		ConnectorID:     view.ConnectorID,
		KnowledgeBaseID: view.KnowledgeBaseID,
		SourceACL:       view.SourceACL,
		EffectiveACL:    view.EffectiveACL,
		ACLMode:         view.ACLMode,
		ACLVersion:      view.ACLVersion,
		Members:         members,
	}
}

func CreateConnectorInput(input httpapi.ConnectorCreateRequest) datasource.CreateConnectorInput {
	return datasource.CreateConnectorInput{
		Name:                input.Name,
		Type:                input.Type,
		Config:              connectorConfig(input.Config),
		Credential:          credential(input.Credential),
		SyncIntervalSeconds: input.SyncIntervalSeconds,
		Enabled:             input.Enabled,
	}
}

func UpdateConnectorInput(input httpapi.ConnectorUpdateRequest) datasource.UpdateConnectorInput {
	result := datasource.UpdateConnectorInput{
		Name:                input.Name,
		Credential:          credential(input.Credential),
		SyncIntervalSeconds: input.SyncIntervalSeconds,
		Enabled:             input.Enabled,
	}
	if input.Config != nil {
		config := connectorConfig(*input.Config)
		result.Config = &config
	}
	return result
}

func connectorConfig(view httpapi.ConnectorConfig) datasource.ConnectorConfig {
	return datasource.ConnectorConfig{
		ManifestURL:       view.ManifestURL,
		RequestsPerSecond: view.RequestsPerSecond,
		MaxItemsPerRun:    view.MaxItemsPerRun,
		ACLMode:           view.ACLMode,
	}
}

func credential(view *httpapi.CredentialInput) *datasource.CredentialInput {
	if view == nil {
		return nil
	}
	return &datasource.CredentialInput{
		Type:     view.Type,
		Token:    view.Token,
		Username: view.Username,
		Password: view.Password,
	}
}
