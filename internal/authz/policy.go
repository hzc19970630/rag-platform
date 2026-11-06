package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/redis/go-redis/v9"
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

// tenantPolicy is an immutable Casbin projection for one tenant. Building it
// only reads that tenant's memberships and knowledge bases, so a change in one
// tenant never rebuilds another tenant's policy.
type tenantPolicy struct {
	enforcer *casbin.Enforcer
	revision int64
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

// buildTenantPolicy projects one tenant's memberships and knowledge bases into
// Casbin policies. The policy is a projection of membership tables, never a
// second source of truth, so it is built in memory instead of being persisted
// to casbin_rule.
func buildTenantPolicy(tenantID string, data TenantPolicyData) (*casbin.Enforcer, error) {
	next, err := newEnforcer()
	if err != nil {
		return nil, err
	}

	knowledgeBases := data.KnowledgeBases
	kbByID := make(map[string]struct{}, len(knowledgeBases))
	for _, knowledgeBase := range knowledgeBases {
		kbByID[knowledgeBase.ID] = struct{}{}
	}

	for _, membership := range data.Memberships {
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
	for _, member := range data.KnowledgeBaseMembers {
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

// authorizeState returns the current revision and that tenant's policy. The
// revision is read on every call, so a committed ACL change is visible without
// waiting for a cache TTL; only the expensive policy build is cached.
func (s *Service) authorizeState(ctx context.Context, tenantID string) (int64, *casbin.Enforcer, error) {
	revision, err := s.policyStore.CurrentRevision(ctx, tenantID)
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
	data, err := s.policyStore.LoadTenantPolicy(ctx, tenantID)
	if err != nil {
		return 0, nil, err
	}
	next, err := buildTenantPolicy(tenantID, data)
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
	revision, err := s.policyStore.BumpRevision(ctx, tenantID)
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
