package authz

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	policyStore     PolicyStore
	scopeStore      ScopeStore
	auditStore      AuditStore
	membershipStore MembershipStore
	// mu guards the tenant-policy map, the revision map and the scope cache.
	// A built enforcer is immutable, so Enforce itself runs without a lock.
	mu sync.RWMutex
	// tenants holds the policy projection per tenant, keyed by tenant id.
	tenants map[string]*tenantPolicy
	// scopes is an in-process L1 cache; Redis is the optional L2 shared cache.
	scopes map[string]map[string]scopeCacheEntry
	// revisionByTenant records the last revision observed for each tenant so a
	// tenant policy can be reused without a rebuild.
	revisionByTenant map[string]int64
	refreshMu        sync.Mutex
	redis            *redis.Client
	now              func() time.Time
}

func NewService(stores Stores) (*Service, error) {
	if _, err := newEnforcer(); err != nil {
		return nil, err
	}
	return &Service{
		policyStore:      stores.Policy,
		scopeStore:       stores.Scope,
		auditStore:       stores.Audit,
		membershipStore:  stores.Membership,
		tenants:          map[string]*tenantPolicy{},
		scopes:           map[string]map[string]scopeCacheEntry{},
		revisionByTenant: map[string]int64{},
		now:              time.Now,
	}, nil
}

func (s *Service) Authorize(ctx context.Context, principal Principal, action, object string) (bool, error) {
	if strings.TrimSpace(principal.SubjectID) == "" || strings.TrimSpace(principal.TenantID) == "" {
		return false, apperror.ErrUnauthenticated
	}
	_, enforcer, err := s.authorizeState(ctx, principal.TenantID)
	if err != nil {
		return false, err
	}
	if !principalAllowsAction(principal, action) {
		if auditErr := s.recordAudit(ctx, principal, action, object, false, "api_key_capability_missing"); auditErr != nil {
			return false, auditErr
		}
		return false, nil
	}
	allowed, err := enforcer.Enforce(principal.SubjectID, principal.TenantID, object, action)
	reason := "allowed"
	if err != nil {
		reason = err.Error()
	} else if !allowed {
		reason = "policy_denied"
	}
	if auditRequired(action, allowed) {
		if auditErr := s.recordAudit(ctx, principal, action, object, allowed, reason); auditErr != nil && err == nil {
			return false, auditErr
		}
	}
	return allowed, err
}

// AuthorizeKnowledgeBase is the single entry point for resource-level checks
// on a knowledge base. Callers pass the id and action, so the object path is
// built in one place instead of being duplicated across services.
func (s *Service) AuthorizeKnowledgeBase(ctx context.Context, principal Principal, knowledgeBaseID, action string) (bool, error) {
	return s.Authorize(ctx, principal, action, "/knowledge-bases/"+knowledgeBaseID)
}

// AuthorizeKnowledgeBases checks the collection-level action used when
// creating a knowledge base, before a resource id exists.
func (s *Service) AuthorizeKnowledgeBases(ctx context.Context, principal Principal, action string) (bool, error) {
	return s.Authorize(ctx, principal, action, "/knowledge-bases")
}

func resourcePath(id string) string { return "/knowledge-bases/" + id }

func capabilitiesForRole(role string) []string {
	switch role {
	case "owner", "admin":
		// tenant:admin marks the tenant-wide operator surface. It is derived from
		// the session role only; API keys cannot carry it (see KnownCapability).
		return []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members", "tenant:admin"}
	case "editor":
		return []string{"knowledge_base:read", "knowledge_base:write"}
	default:
		return []string{"knowledge_base:read"}
	}
}

// KnownCapability is the closed set an API key may carry. "chat" is a separate
// capability because retrieval and generation are different risk surfaces.
func KnownCapability(capability string) bool {
	switch capability {
	case "knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members", "chat":
		return true
	default:
		return false
	}
}

func principalAllowsAction(principal Principal, action string) bool {
	if principal.CredentialType != "api_key" || len(principal.Capabilities) == 0 {
		return true
	}
	required := ""
	switch action {
	case "read":
		required = "knowledge_base:read"
	case "write":
		required = "knowledge_base:write"
	case "delete":
		required = "knowledge_base:delete"
	case "manage_members":
		required = "knowledge_base:manage_members"
	default:
		return true
	}
	return contains(principal.Capabilities, required)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func intersect(left, right []string) []string {
	result := make([]string, 0, len(left))
	for _, value := range left {
		if contains(right, value) {
			result = append(result, value)
		}
	}
	return result
}

func nowUTC() time.Time { return time.Now().UTC() }
