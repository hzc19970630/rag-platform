package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
)

const (
	scopeCacheTTL  = 30 * time.Second
	scopeRedisTTL  = 30 * time.Second
	scopeCacheSize = 4096
)

type scopeCacheEntry struct {
	scope   AuthorizationScope
	expires time.Time
}

func (s *Service) BuildScope(ctx context.Context, principal Principal, target QueryTarget) (AuthorizationScope, error) {
	if strings.TrimSpace(principal.SubjectID) == "" || strings.TrimSpace(principal.TenantID) == "" {
		return AuthorizationScope{}, apperror.ErrUnauthenticated
	}
	revision, _, err := s.authorizeState(ctx, principal.TenantID)
	if err != nil {
		return AuthorizationScope{}, err
	}
	if cached, ok := s.cachedScope(ctx, principal, target, revision); ok {
		return cached, nil
	}
	subject, ok, err := s.scopeStore.Subject(ctx, principal.TenantID, principal.SubjectID)
	if err != nil {
		return AuthorizationScope{}, err
	}
	if !ok {
		return AuthorizationScope{}, apperror.ErrForbidden
	}
	ids := append([]string(nil), subject.KnowledgeBaseIDs...)
	// An API key cannot widen its owner's access. Intersect the key's own scope
	// with the role-derived scope, so revoking a membership or KB grant removes
	// access even when the key itself is still active.
	if len(principal.APIKeyKnowledgeBaseIDs) > 0 {
		ids = intersect(ids, principal.APIKeyKnowledgeBaseIDs)
	}
	if target.KnowledgeBaseID != "" && !contains(ids, target.KnowledgeBaseID) {
		return AuthorizationScope{}, apperror.ErrForbidden
	}
	capabilities := capabilitiesForRole(subject.Role)
	if principal.CredentialType == "api_key" {
		capabilities = intersect(capabilities, principal.Capabilities)
		for _, capability := range principal.Capabilities {
			if capability == "chat" && !contains(capabilities, capability) {
				capabilities = append(capabilities, capability)
			}
		}
	}
	scope := AuthorizationScope{
		SubjectID:        principal.SubjectID,
		TenantID:         principal.TenantID,
		KnowledgeBaseIDs: ids,
		ResourceIDs:      ids,
		Capabilities:     capabilities,
		ACLVersion:       fmt.Sprintf("tenant:%s:%d", principal.TenantID, revision),
	}
	s.storeScope(ctx, principal, target, revision, scope)
	return scope, nil
}

func scopeCacheKey(principal Principal, target QueryTarget, revision int64) string {
	return fmt.Sprintf("%s|%s|%s|%d", principal.TenantID, principal.SubjectID, target.KnowledgeBaseID, revision)
}

func redisScopeKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "authz:scope:" + hex.EncodeToString(sum[:])
}

// cachedScope checks the in-process L1 cache and then the optional Redis L2
// cache. API keys bypass both because their scope depends on credential
// capabilities and knowledge-base allowlists, which are cheaper to rebuild than
// to fingerprint safely.
func (s *Service) cachedScope(ctx context.Context, principal Principal, target QueryTarget, revision int64) (AuthorizationScope, bool) {
	if principal.CredentialType == "api_key" {
		return AuthorizationScope{}, false
	}
	key := scopeCacheKey(principal, target, revision)
	now := s.currentTime()
	s.mu.RLock()
	entry, ok := s.scopes[principal.TenantID][key]
	s.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		return cloneScope(entry.scope), true
	}
	if ok {
		s.mu.Lock()
		delete(s.scopes[principal.TenantID], key)
		s.mu.Unlock()
	}
	s.mu.RLock()
	client := s.redis
	s.mu.RUnlock()
	if client == nil {
		return AuthorizationScope{}, false
	}
	raw, err := client.Get(ctx, redisScopeKey(key)).Bytes()
	if err != nil {
		return AuthorizationScope{}, false
	}
	var scope AuthorizationScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return AuthorizationScope{}, false
	}
	s.mu.Lock()
	s.putScopeLocked(principal.TenantID, key, scope, now)
	s.mu.Unlock()
	return cloneScope(scope), true
}

func (s *Service) storeScope(ctx context.Context, principal Principal, target QueryTarget, revision int64, scope AuthorizationScope) {
	if principal.CredentialType == "api_key" {
		return
	}
	key := scopeCacheKey(principal, target, revision)
	now := s.currentTime()
	s.mu.Lock()
	s.putScopeLocked(principal.TenantID, key, scope, now)
	client := s.redis
	s.mu.Unlock()
	if client == nil {
		return
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return
	}
	_ = client.Set(ctx, redisScopeKey(key), raw, scopeRedisTTL).Err()
}

// putScopeLocked is called with s.mu held. The per-tenant cap keeps revision
// churn from growing the map without bound; the TTL does the normal cleanup.
func (s *Service) putScopeLocked(tenantID, key string, scope AuthorizationScope, now time.Time) {
	tenantScopes := s.scopes[tenantID]
	if tenantScopes == nil || len(tenantScopes) >= scopeCacheSize {
		tenantScopes = map[string]scopeCacheEntry{}
		s.scopes[tenantID] = tenantScopes
	}
	tenantScopes[key] = scopeCacheEntry{scope: cloneScope(scope), expires: now.Add(scopeCacheTTL)}
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func cloneScope(scope AuthorizationScope) AuthorizationScope {
	cloned := scope
	cloned.KnowledgeBaseIDs = append([]string(nil), scope.KnowledgeBaseIDs...)
	cloned.ResourceIDs = append([]string(nil), scope.ResourceIDs...)
	cloned.Capabilities = append([]string(nil), scope.Capabilities...)
	return cloned
}

func (s *Service) CanRead(ctx context.Context, scope AuthorizationScope, resource ResourceRef) (bool, error) {
	if resource.TenantID != scope.TenantID || !contains(scope.KnowledgeBaseIDs, resource.KnowledgeBaseID) {
		return false, nil
	}
	if len(scope.Capabilities) > 0 && !contains(scope.Capabilities, "knowledge_base:read") {
		return false, nil
	}
	return true, nil
}

func (s *Service) FilterReadable(ctx context.Context, scope AuthorizationScope, refs []ResourceRef) ([]ResourceRef, error) {
	result := make([]ResourceRef, 0, len(refs))
	for _, ref := range refs {
		allowed, err := s.CanRead(ctx, scope, ref)
		if err != nil {
			return nil, err
		}
		if allowed {
			result = append(result, ref)
		}
	}
	return result, nil
}
