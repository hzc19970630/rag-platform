// Package views maps persistence rows and domain projections onto the HTTP
// wire contracts. Transport handlers depend on this package, never on GORM
// rows, so a column rename cannot silently change the API.
package views

import (
	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func Me(principal authz.Principal, scope authz.AuthorizationScope, developmentIdentity bool) httpapi.MeResponse {
	return httpapi.MeResponse{
		Principal:           Principal(principal),
		Scope:               Scope(scope),
		DevelopmentIdentity: developmentIdentity,
	}
}

func Principal(principal authz.Principal) httpapi.PrincipalView {
	return httpapi.PrincipalView{
		SubjectID:      principal.SubjectID,
		TenantID:       principal.TenantID,
		Capabilities:   cloneStrings(principal.Capabilities),
		CredentialType: principal.CredentialType,
		CredentialID:   principal.CredentialID,
	}
}

func Scope(scope authz.AuthorizationScope) httpapi.ScopeView {
	return httpapi.ScopeView{
		SubjectID:        scope.SubjectID,
		TenantID:         scope.TenantID,
		KnowledgeBaseIDs: cloneStrings(scope.KnowledgeBaseIDs),
		ResourceIDs:      cloneStrings(scope.ResourceIDs),
		Capabilities:     cloneStrings(scope.Capabilities),
		ACLVersion:       scope.ACLVersion,
	}
}

func AuthConfig(oidcEnabled bool, oidcIssuer string, devAuthEnabled bool, cookieName string, sessionTTLSeconds int) httpapi.AuthConfig {
	return httpapi.AuthConfig{
		OIDCEnabled:     oidcEnabled,
		OIDCIssuer:      oidcIssuer,
		DevAuthEnabled:  devAuthEnabled,
		SessionCookie:   cookieName,
		SessionTTLHours: sessionTTLSeconds / 3600,
	}
}

func DevLogin(result identity.SessionResult) httpapi.DevLoginResponse {
	return httpapi.DevLoginResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		User:      User(result.User),
		Principal: Principal(result.Principal),
	}
}

func User(row models.User) httpapi.User {
	return httpapi.User{
		ID:          row.ID,
		Email:       row.Email,
		DisplayName: row.DisplayName,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func CreateAPIKeyInput(input httpapi.APIKeyCreateRequest) identity.CreateAPIKeyInput {
	return identity.CreateAPIKeyInput{
		Name:             input.Name,
		Capabilities:     cloneStrings(input.Capabilities),
		KnowledgeBaseIDs: cloneStrings(input.KnowledgeBaseIDs),
		ExpiresAt:        input.ExpiresAt,
	}
}

func APIKey(view identity.APIKeyView) httpapi.APIKey {
	return httpapi.APIKey{
		ID:               view.ID,
		Name:             view.Name,
		KeyPrefix:        view.KeyPrefix,
		Capabilities:     cloneStrings(view.Capabilities),
		KnowledgeBaseIDs: cloneStrings(view.KnowledgeBaseIDs),
		ExpiresAt:        view.ExpiresAt,
		RevokedAt:        view.RevokedAt,
		LastUsedAt:       view.LastUsedAt,
		CreatedAt:        view.CreatedAt,
	}
}

func APIKeys(rows []identity.APIKeyView) []httpapi.APIKey {
	items := make([]httpapi.APIKey, 0, len(rows))
	for _, row := range rows {
		items = append(items, APIKey(row))
	}
	return items
}

func TenantMember(view authz.TenantMemberView) httpapi.TenantMember {
	return httpapi.TenantMember{
		UserID:      view.UserID,
		Email:       view.Email,
		DisplayName: view.DisplayName,
		Role:        view.Role,
	}
}

func TenantMembers(rows []authz.TenantMemberView) []httpapi.TenantMember {
	items := make([]httpapi.TenantMember, 0, len(rows))
	for _, row := range rows {
		items = append(items, TenantMember(row))
	}
	return items
}

func AuditEvent(row models.AuditLog) httpapi.AuditEvent {
	return httpapi.AuditEvent{
		ID:           row.ID,
		TenantID:     row.TenantID,
		ActorID:      row.ActorID,
		Action:       row.Action,
		ResourceType: row.ResourceType,
		ResourceID:   row.ResourceID,
		Allowed:      row.Allowed,
		Reason:       row.Reason,
		RequestID:    row.RequestID,
		IPAddress:    row.IPAddress,
		UserAgent:    row.UserAgent,
		CreatedAt:    row.CreatedAt,
	}
}

func AuditEvents(rows []models.AuditLog) []httpapi.AuditEvent {
	items := make([]httpapi.AuditEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, AuditEvent(row))
	}
	return items
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}
