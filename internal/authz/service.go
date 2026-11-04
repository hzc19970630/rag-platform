package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const casbinModel = `[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act, eft

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && keyMatch2(r.obj, p.obj) && regexMatch(r.act, p.act)`

const authorizationChangedChannel = "authorization.policy_changed"

const (
	scopeCacheTTL  = 30 * time.Second
	scopeRedisTTL  = 30 * time.Second
	scopeCacheSize = 4096
)

// tenantPolicy is an immutable Casbin projection for one tenant. Building it
// only reads that tenant's memberships and knowledge bases, so a change in one
// tenant never rebuilds another tenant's policy.
type tenantPolicy struct {
	enforcer *casbin.Enforcer
	revision int64
}

type scopeCacheEntry struct {
	scope   AuthorizationScope
	expires time.Time
}

type Service struct {
	db *gorm.DB
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

type TenantMemberView struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type AuditQuery struct {
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Allowed      *bool
	Limit        int
}

func NewService(db *gorm.DB) (*Service, error) {
	if _, err := newEnforcer(); err != nil {
		return nil, err
	}
	return &Service{
		db:               db,
		tenants:          map[string]*tenantPolicy{},
		scopes:           map[string]map[string]scopeCacheEntry{},
		revisionByTenant: map[string]int64{},
		now:              time.Now,
	}, nil
}

func newEnforcer() (*casbin.Enforcer, error) {
	accessModel, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("create casbin model: %w", err)
	}
	enforcer, err := casbin.NewEnforcer(accessModel)
	if err != nil {
		return nil, fmt.Errorf("create casbin enforcer: %w", err)
	}
	enforcer.EnableAutoSave(false)
	return enforcer, nil
}

// StartInvalidation subscribes to the policy-change channel so every API/worker
// instance drops its cached projection promptly. Correctness does not depend on
// delivery: every request reads the tenant revision from the database.
func (s *Service) StartInvalidation(ctx context.Context, redisAddr, redisPassword string) error {
	if strings.TrimSpace(redisAddr) == "" {
		return nil
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr, Password: redisPassword})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return fmt.Errorf("connect authorization invalidation redis: %w", err)
	}
	s.mu.Lock()
	s.redis = client
	s.mu.Unlock()
	go func() {
		pubsub := client.Subscribe(context.Background(), authorizationChangedChannel)
		defer pubsub.Close()
		for message := range pubsub.Channel() {
			var payload struct {
				TenantID string `json:"tenantId"`
				Revision int64  `json:"revision"`
			}
			_ = json.Unmarshal([]byte(message.Payload), &payload)
			if payload.TenantID != "" {
				s.InvalidateTenant(payload.TenantID)
				continue
			}
			_ = s.RefreshPolicies(context.Background())
		}
	}()
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	client := s.redis
	s.redis = nil
	s.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

// RefreshPolicies drops every cached tenant projection. Policies are rebuilt
// lazily per tenant on the next authorization check; a manual refresh must not
// block on loading the whole policy space.
func (s *Service) RefreshPolicies(ctx context.Context) error {
	s.mu.Lock()
	s.tenants = map[string]*tenantPolicy{}
	s.scopes = map[string]map[string]scopeCacheEntry{}
	s.revisionByTenant = map[string]int64{}
	s.mu.Unlock()
	return nil
}

// InvalidateTenant drops one tenant's policy and scope caches. Other tenants
// keep their projection, so one tenant's ACL change cannot cause an N-tenant
// rebuild storm.
func (s *Service) InvalidateTenant(tenantID string) {
	if strings.TrimSpace(tenantID) == "" {
		_ = s.RefreshPolicies(context.Background())
		return
	}
	s.mu.Lock()
	delete(s.tenants, tenantID)
	delete(s.scopes, tenantID)
	delete(s.revisionByTenant, tenantID)
	s.mu.Unlock()
}

// buildTenantPolicy loads only one tenant's memberships and knowledge bases.
// The policy is a projection of membership tables, never a second source of
// truth, so it is built in memory instead of being persisted to casbin_rule.
func (s *Service) buildTenantPolicy(ctx context.Context, tenantID string) (*casbin.Enforcer, error) {
	next, err := newEnforcer()
	if err != nil {
		return nil, err
	}

	var memberships []models.Membership
	if err := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Find(&memberships).Error; err != nil {
		return nil, fmt.Errorf("load tenant memberships: %w", err)
	}
	var knowledgeBases []models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Find(&knowledgeBases).Error; err != nil {
		return nil, fmt.Errorf("load tenant knowledge bases: %w", err)
	}
	kbByID := make(map[string]models.KnowledgeBase, len(knowledgeBases))
	kbIDs := make([]string, 0, len(knowledgeBases))
	for _, knowledgeBase := range knowledgeBases {
		kbByID[knowledgeBase.ID] = knowledgeBase
		kbIDs = append(kbIDs, knowledgeBase.ID)
	}
	var kbMembers []models.KnowledgeBaseMember
	if len(kbIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("knowledge_base_id IN ?", kbIDs).Find(&kbMembers).Error; err != nil {
			return nil, fmt.Errorf("load knowledge base memberships: %w", err)
		}
	}

	for _, membership := range memberships {
		if _, err := next.AddGroupingPolicy(membership.UserID, tenantRole(membership.Role), tenantID); err != nil {
			return nil, fmt.Errorf("add tenant policy: %w", err)
		}
		if membership.Role == "owner" || membership.Role == "admin" {
			if _, err := next.AddPolicy(tenantRole(membership.Role), tenantID, "/knowledge-bases", "read|write|delete|manage_members", "allow"); err != nil {
				return nil, fmt.Errorf("add tenant collection policy: %w", err)
			}
		}
	}
	for range knowledgeBases {
		for _, role := range []string{"owner", "admin"} {
			if _, err := next.AddPolicy(tenantRole(role), tenantID, "/knowledge-bases/:id", "read|write|delete|manage_members", "allow"); err != nil {
				return nil, fmt.Errorf("add tenant resource policy: %w", err)
			}
		}
	}
	for _, member := range kbMembers {
		if _, ok := kbByID[member.KnowledgeBaseID]; !ok {
			continue
		}
		role := kbRole(member.Role, member.KnowledgeBaseID)
		if _, err := next.AddGroupingPolicy(member.UserID, role, tenantID); err != nil {
			return nil, fmt.Errorf("add knowledge base grouping policy: %w", err)
		}
		object := resourcePath(member.KnowledgeBaseID)
		actions := "read"
		if member.Role == "owner" || member.Role == "admin" {
			actions = "read|write|delete|manage_members"
		} else if member.Role == "editor" {
			actions = "read|write"
		}
		if _, err := next.AddPolicy(role, tenantID, object, actions, "allow"); err != nil {
			return nil, fmt.Errorf("add knowledge base policy: %w", err)
		}
		if member.Role == "owner" || member.Role == "admin" {
			if _, err := next.AddPolicy(role, tenantID, object+"/members", "read|manage_members", "allow"); err != nil {
				return nil, fmt.Errorf("add member management policy: %w", err)
			}
		}
	}
	return next, nil
}

func (s *Service) currentRevision(ctx context.Context, tenantID string) (int64, error) {
	var row models.AuthorizationRevision
	if err := s.db.WithContext(ctx).First(&row, "tenant_id = ?", tenantID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.AuthorizationRevision{TenantID: tenantID, Revision: 1}
			if createErr := s.db.WithContext(ctx).Create(&row).Error; createErr != nil {
				var existing models.AuthorizationRevision
				if lookupErr := s.db.WithContext(ctx).First(&existing, "tenant_id = ?", tenantID).Error; lookupErr == nil {
					return existing.Revision, nil
				}
				return 0, fmt.Errorf("create authorization revision: %w", createErr)
			}
			return row.Revision, nil
		}
		return 0, fmt.Errorf("load authorization revision: %w", err)
	}
	return row.Revision, nil
}

// authorizeState returns the current revision and that tenant's policy. The
// revision is read on every call, so a committed ACL change is visible without
// waiting for a cache TTL; only the expensive policy build is cached.
func (s *Service) authorizeState(ctx context.Context, tenantID string) (int64, *casbin.Enforcer, error) {
	revision, err := s.currentRevision(ctx, tenantID)
	if err != nil {
		return 0, nil, err
	}
	s.mu.RLock()
	policy := s.tenants[tenantID]
	s.mu.RUnlock()
	if policy != nil && policy.revision == revision {
		return revision, policy.enforcer, nil
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	// Another request may have built this tenant while we waited.
	s.mu.RLock()
	policy = s.tenants[tenantID]
	s.mu.RUnlock()
	if policy != nil && policy.revision == revision {
		return revision, policy.enforcer, nil
	}
	next, err := s.buildTenantPolicy(ctx, tenantID)
	if err != nil {
		return 0, nil, err
	}
	s.mu.Lock()
	s.tenants[tenantID] = &tenantPolicy{enforcer: next, revision: revision}
	s.revisionByTenant[tenantID] = revision
	s.mu.Unlock()
	return revision, next, nil
}

// BumpRevision is the single mutation point for authorization changes. It
// commits the new revision, refreshes this instance immediately and notifies
// the others. Call it after the business relationship change commits.
func (s *Service) BumpRevision(ctx context.Context, tenantID string) error {
	var revision int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.AuthorizationRevision{}).Where("tenant_id = ?", tenantID).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": nowUTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			row := models.AuthorizationRevision{TenantID: tenantID, Revision: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			revision = row.Revision
			return nil
		}
		var row models.AuthorizationRevision
		if err := tx.First(&row, "tenant_id = ?", tenantID).Error; err != nil {
			return err
		}
		revision = row.Revision
		return nil
	})
	if err != nil {
		return fmt.Errorf("bump authorization revision: %w", err)
	}
	// Drop this tenant's policy, scope and revision caches. Other tenants keep
	// their projections; the next check rebuilds only this tenant.
	s.InvalidateTenant(tenantID)
	s.mu.RLock()
	client := s.redis
	s.mu.RUnlock()
	if client != nil {
		payload, _ := json.Marshal(map[string]any{"tenantId": tenantID, "revision": revision})
		_ = client.Publish(ctx, authorizationChangedChannel, payload).Err()
	}
	return nil
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

// auditRequired keeps the audit trail focused on security-relevant events:
// every denial is recorded, while allowed read checks (graph/snapshot reads)
// do not append a row per request. State-changing actions still record both
// allowed and denied outcomes.
func auditRequired(action string, allowed bool) bool {
	if !allowed {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(action), "read")
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
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AuthorizationScope{}, apperror.ErrForbidden
		}
		return AuthorizationScope{}, fmt.Errorf("load tenant membership: %w", err)
	}
	var ids []string
	if membership.Role == "owner" || membership.Role == "admin" {
		var knowledgeBases []models.KnowledgeBase
		if err := s.db.WithContext(ctx).Where("tenant_id = ?", principal.TenantID).Find(&knowledgeBases).Error; err != nil {
			return AuthorizationScope{}, fmt.Errorf("list tenant knowledge bases: %w", err)
		}
		for _, knowledgeBase := range knowledgeBases {
			ids = append(ids, knowledgeBase.ID)
		}
	} else {
		var rows []struct{ KnowledgeBaseID string }
		if err := s.db.WithContext(ctx).Table("knowledge_base_members kb_member").Select("kb_member.knowledge_base_id").Joins("JOIN knowledge_bases kb ON kb.id = kb_member.knowledge_base_id").Where("kb.tenant_id = ? AND kb_member.user_id = ?", principal.TenantID, principal.SubjectID).Scan(&rows).Error; err != nil {
			return AuthorizationScope{}, fmt.Errorf("list member knowledge bases: %w", err)
		}
		for _, row := range rows {
			ids = append(ids, row.KnowledgeBaseID)
		}
	}
	// An API key cannot widen its owner's access. Intersect the key's own scope
	// with the role-derived scope, so revoking a membership or KB grant removes
	// access even when the key itself is still active.
	if len(principal.APIKeyKnowledgeBaseIDs) > 0 {
		ids = intersect(ids, principal.APIKeyKnowledgeBaseIDs)
	}
	if target.KnowledgeBaseID != "" && !contains(ids, target.KnowledgeBaseID) {
		return AuthorizationScope{}, apperror.ErrForbidden
	}
	capabilities := capabilitiesForRole(membership.Role)
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

func (s *Service) ListAudit(ctx context.Context, principal Principal, limit int) ([]models.AuditLog, error) {
	return s.QueryAudit(ctx, principal, AuditQuery{Limit: limit})
}

// QueryAudit lets tenant owners/admins see the tenant trail while every other
// role sees only its own actions. Permission denials are included, so a denied
// retrieval leaves a trace.
func (s *Service) QueryAudit(ctx context.Context, principal Principal, query AuditQuery) ([]models.AuditLog, error) {
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		return nil, apperror.ErrForbidden
	}
	dbQuery := s.db.WithContext(ctx).Where("tenant_id = ?", principal.TenantID)
	if membership.Role != "owner" && membership.Role != "admin" {
		dbQuery = dbQuery.Where("actor_id = ?", principal.SubjectID)
	} else if query.ActorID != "" {
		dbQuery = dbQuery.Where("actor_id = ?", query.ActorID)
	}
	if query.Action != "" {
		dbQuery = dbQuery.Where("action = ?", query.Action)
	}
	if query.ResourceType != "" {
		dbQuery = dbQuery.Where("resource_type = ?", query.ResourceType)
	}
	if query.ResourceID != "" {
		dbQuery = dbQuery.Where("resource_id = ?", query.ResourceID)
	}
	if query.Allowed != nil {
		dbQuery = dbQuery.Where("allowed = ?", *query.Allowed)
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []models.AuditLog
	if err := dbQuery.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	return rows, nil
}

// PurgeAuditLogs deletes at most one batch of audit rows older than the cutoff.
// The worker maintenance loop drains several batches per run so a backlog does
// not grow without bound while each statement stays short.
func (s *Service) PurgeAuditLogs(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	result := s.db.WithContext(ctx).Exec(`
		DELETE FROM audit_logs
		WHERE id IN (
			SELECT id FROM audit_logs WHERE created_at < ? ORDER BY created_at, id LIMIT ?
		)`, before, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge audit logs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) ListTenantMembers(ctx context.Context, principal Principal) ([]TenantMemberView, error) {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return nil, err
	}
	var rows []struct {
		UserID      string
		Email       string
		DisplayName string
		Role        string
	}
	if err := s.db.WithContext(ctx).Table("memberships m").Select("m.user_id, u.email, u.display_name, m.role").Joins("JOIN users u ON u.id = m.user_id").Where("m.tenant_id = ?", principal.TenantID).Order("m.created_at").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tenant members: %w", err)
	}
	views := make([]TenantMemberView, 0, len(rows))
	for _, row := range rows {
		views = append(views, TenantMemberView{UserID: row.UserID, Email: row.Email, DisplayName: row.DisplayName, Role: row.Role})
	}
	return views, nil
}

func (s *Service) AddTenantMember(ctx context.Context, principal Principal, userID, role string) (TenantMemberView, error) {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return TenantMemberView{}, err
	}
	if !validTenantRole(role) {
		return TenantMemberView{}, fmt.Errorf("unsupported tenant role")
	}
	if role == "owner" && !s.isOwner(ctx, principal) {
		return TenantMemberView{}, apperror.ErrForbidden
	}
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return TenantMemberView{}, apperror.ErrNotFound
	}
	membership := models.Membership{TenantID: principal.TenantID, UserID: userID, Role: role}
	if err := s.db.WithContext(ctx).Save(&membership).Error; err != nil {
		return TenantMemberView{}, fmt.Errorf("add tenant member: %w", err)
	}
	if err := s.BumpRevision(ctx, principal.TenantID); err != nil {
		return TenantMemberView{}, err
	}
	_ = s.recordAdminAudit(ctx, principal, "add_member", "membership", userID, "role="+role)
	return TenantMemberView{UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: role}, nil
}

func (s *Service) UpdateTenantMember(ctx context.Context, principal Principal, userID, role string) error {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return err
	}
	if !validTenantRole(role) {
		return fmt.Errorf("unsupported tenant role")
	}
	if role == "owner" && !s.isOwner(ctx, principal) {
		return apperror.ErrForbidden
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, userID).Error; err != nil {
		return apperror.ErrNotFound
	}
	if membership.Role == "owner" && role != "owner" {
		var owners int64
		if err := s.db.WithContext(ctx).Model(&models.Membership{}).Where("tenant_id = ? AND role = ?", principal.TenantID, "owner").Count(&owners).Error; err != nil {
			return fmt.Errorf("count tenant owners: %w", err)
		}
		if owners <= 1 {
			return fmt.Errorf("the last tenant owner cannot be demoted")
		}
	}
	if err := s.db.WithContext(ctx).Model(&membership).Updates(map[string]any{"role": role, "updated_at": nowUTC()}).Error; err != nil {
		return fmt.Errorf("update tenant member: %w", err)
	}
	if err := s.BumpRevision(ctx, principal.TenantID); err != nil {
		return err
	}
	_ = s.recordAdminAudit(ctx, principal, "update_member", "membership", userID, "role="+role)
	return nil
}

func (s *Service) RemoveTenantMember(ctx context.Context, principal Principal, userID string) error {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return err
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, userID).Error; err != nil {
		return apperror.ErrNotFound
	}
	if membership.Role == "owner" {
		var owners int64
		if err := s.db.WithContext(ctx).Model(&models.Membership{}).Where("tenant_id = ? AND role = ?", principal.TenantID, "owner").Count(&owners).Error; err != nil {
			return fmt.Errorf("count tenant owners: %w", err)
		}
		if owners <= 1 {
			return fmt.Errorf("the last tenant owner cannot be removed")
		}
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND user_id = ?", principal.TenantID, userID).Delete(&models.Membership{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ? AND knowledge_base_id IN (SELECT id FROM knowledge_bases WHERE tenant_id = ?)", userID, principal.TenantID).Delete(&models.KnowledgeBaseMember{}).Error
	})
	if err != nil {
		return fmt.Errorf("remove tenant member: %w", err)
	}
	if err := s.BumpRevision(ctx, principal.TenantID); err != nil {
		return err
	}
	_ = s.recordAdminAudit(ctx, principal, "remove_member", "membership", userID, "")
	return nil
}

// RequireTenantOperator gates operator surfaces (task inspection, dead-letter
// replay, reconciliation, metrics). API keys are rejected even when they carry
// management capabilities: those actions must be attributable to a person.
func (s *Service) RequireTenantOperator(ctx context.Context, principal Principal) error {
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.SubjectID) == "" {
		return apperror.ErrUnauthenticated
	}
	if principal.CredentialType == "api_key" {
		return apperror.ErrForbidden
	}
	return s.requireTenantAdmin(ctx, principal)
}

func (s *Service) requireTenantAdmin(ctx context.Context, principal Principal) error {
	if principal.CredentialType == "api_key" && !contains(principal.Capabilities, "knowledge_base:manage_members") {
		return apperror.ErrForbidden
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		return apperror.ErrForbidden
	}
	if membership.Role != "owner" && membership.Role != "admin" {
		return apperror.ErrForbidden
	}
	return nil
}

func (s *Service) isOwner(ctx context.Context, principal Principal) bool {
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		return false
	}
	return membership.Role == "owner"
}

func (s *Service) recordAdminAudit(ctx context.Context, principal Principal, action, resourceType, resourceID, reason string) error {
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action, ResourceType: resourceType, ResourceID: resourceID, Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func (s *Service) recordAudit(ctx context.Context, principal Principal, action, object string, allowed bool, reason string) error {
	if principal.CredentialType == "api_key" {
		reason += ";credential=api_key:" + principal.CredentialID
	}
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action, ResourceType: "path", ResourceID: object, Allowed: allowed, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func tenantRole(role string) string { return "tenant_" + role }

func kbRole(role, id string) string { return "kb_" + role + ":" + id }

func resourcePath(id string) string { return "/knowledge-bases/" + id }

func capabilitiesForRole(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members"}
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

func validTenantRole(role string) bool {
	switch role {
	case "owner", "admin", "member", "viewer":
		return true
	default:
		return false
	}
}

func nowUTC() time.Time { return time.Now().UTC() }
