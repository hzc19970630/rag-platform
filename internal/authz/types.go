package authz

import "context"

type Principal struct {
	SubjectID    string   `json:"subjectId"`
	TenantID     string   `json:"tenantId"`
	Capabilities []string `json:"capabilities,omitempty"`
	// CredentialType is session, api_key or dev. It is part of the audit record
	// and decides whether Capabilities narrows the role.
	CredentialType string `json:"credentialType,omitempty"`
	CredentialID   string `json:"credentialId,omitempty"`
	// APIKeyKnowledgeBaseIDs is the key's own scope. Empty means "all knowledge
	// bases the subject can read"; the role scope is still applied.
	APIKeyKnowledgeBaseIDs []string `json:"-"`
	// Request metadata travels with the principal so every audit row carries the
	// correlation id and client origin without threading a second context type
	// through each service.
	RequestID string `json:"-"`
	IPAddress string `json:"-"`
	UserAgent string `json:"-"`
}

type AccessContext struct {
	Principal
	RequestID   string
	DevIdentity bool
}

type QueryTarget struct {
	KnowledgeBaseID string
}

type AuthorizationScope struct {
	SubjectID        string   `json:"subjectId"`
	TenantID         string   `json:"tenantId"`
	KnowledgeBaseIDs []string `json:"knowledgeBaseIds"`
	ResourceIDs      []string `json:"resourceIds"`
	Capabilities     []string `json:"capabilities"`
	ACLVersion       string   `json:"aclVersion"`
}

type ResourceRef struct {
	TenantID        string
	KnowledgeBaseID string
	ResourceID      string
	ResourceType    string
}

type AuthorizationPort interface {
	BuildScope(context.Context, Principal, QueryTarget) (AuthorizationScope, error)
	CanRead(context.Context, AuthorizationScope, ResourceRef) (bool, error)
	FilterReadable(context.Context, AuthorizationScope, []ResourceRef) ([]ResourceRef, error)
}
