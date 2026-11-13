// Package views maps persistence rows and domain projections onto the HTTP
// wire contracts. Transport handlers depend on this package, never on GORM
// rows, so a column rename cannot silently change the API.
package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/tenant"
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

func AuthConfig(oidcEnabled bool, oidcIssuer string, ldapEnabled bool, devAuthEnabled bool, cookieName string, sessionTTLSeconds int) httpapi.AuthConfig {
	return httpapi.AuthConfig{
		OIDCEnabled:     oidcEnabled,
		OIDCIssuer:      oidcIssuer,
		LDAPEnabled:     ldapEnabled,
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

func TenantOverview(overview tenant.Overview) httpapi.TenantOverview {
	return httpapi.TenantOverview{
		ID:        overview.ID,
		Name:      overview.Name,
		CreatedAt: overview.CreatedAt,
		Members: httpapi.TenantMemberCounts{
			Total:   overview.Members.Total,
			Owners:  overview.Members.Owners,
			Admins:  overview.Members.Admins,
			Members: overview.Members.Members,
			Viewers: overview.Members.Viewers,
		},
		KnowledgeBases:     overview.KnowledgeBases,
		Documents:          overview.Documents,
		StorageBytes:       overview.StorageBytes,
		ChatQuestionsToday: overview.ChatQuestionsToday,
	}
}

func TenantQuota(effective tenantquota.Effective) httpapi.TenantQuota {
	return httpapi.TenantQuota{
		MaxDocuments:       tenantQuotaField(effective.MaxDocuments),
		MaxStorageBytes:    tenantQuotaField(effective.MaxStorageBytes),
		ChatRequestsPerDay: tenantQuotaField(effective.ChatRequestsPerDay),
	}
}

func tenantQuotaField(field tenantquota.Field) httpapi.TenantQuotaField {
	source := "deployment"
	if field.Overridden {
		source = "tenant"
	}
	return httpapi.TenantQuotaField{
		Value:     field.Value,
		Default:   field.Default,
		Source:    source,
		Unlimited: field.Value <= 0,
	}
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
