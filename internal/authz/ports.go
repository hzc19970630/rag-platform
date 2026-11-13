package authz

import (
	"context"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

// TenantPolicyData is the tenant-scoped input of the Casbin projection. It is
// loaded in one place so the policy builder itself stays pure.
type TenantPolicyData struct {
	Memberships          []models.Membership
	KnowledgeBases       []models.KnowledgeBase
	KnowledgeBaseMembers []models.KnowledgeBaseMember
}

// PolicyStore owns the membership projection and the authorization revision.
type PolicyStore interface {
	LoadTenantPolicy(ctx context.Context, tenantID string) (TenantPolicyData, error)
	CurrentRevision(ctx context.Context, tenantID string) (int64, error)
	BumpRevision(ctx context.Context, tenantID string) (int64, error)
}

// ScopeSubject is the persisted half of a scope: the role and the knowledge
// bases the subject can read. API-key narrowing stays in the service.
type ScopeSubject struct {
	Role             string
	KnowledgeBaseIDs []string
}

// ScopeStore resolves the role-derived scope for one subject.
type ScopeStore interface {
	Subject(ctx context.Context, tenantID, userID string) (ScopeSubject, bool, error)
}

// AuditFilter carries the already-authorized filters of one audit query.
type AuditFilter struct {
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Allowed      *bool
	Limit        int
	Offset       int
}

// AuditStore owns the audit trail: membership role lookup for visibility,
// bounded queries, retention purge and append.
type AuditStore interface {
	MemberRole(ctx context.Context, tenantID, userID string) (string, bool, error)
	List(ctx context.Context, tenantID string, filter AuditFilter) ([]models.AuditLog, error)
	Count(ctx context.Context, tenantID string, filter AuditFilter) (int64, error)
	Purge(ctx context.Context, before time.Time, limit int) (int64, error)
	Create(ctx context.Context, entry models.AuditLog) error
}

// MembershipStore owns tenant membership reads and the two mutations that must
// revoke sessions in the same transaction.
type MembershipStore interface {
	List(ctx context.Context, tenantID string) ([]TenantMemberView, error)
	FindUser(ctx context.Context, userID string) (models.User, bool, error)
	Find(ctx context.Context, tenantID, userID string) (models.Membership, bool, error)
	Save(ctx context.Context, membership models.Membership) error
	CountOwners(ctx context.Context, tenantID string) (int64, error)
	UpdateRole(ctx context.Context, tenantID, userID, role string, revokeSessions bool) error
	Remove(ctx context.Context, tenantID, userID string) error
}

// Stores bundles the persistence ports the authorization service needs. The
// service never sees a *gorm.DB.
type Stores struct {
	Policy     PolicyStore
	Scope      ScopeStore
	Audit      AuditStore
	Membership MembershipStore
}
