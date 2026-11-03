package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/gorm-adapter/v3"
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

type Service struct {
	db       *gorm.DB
	adapter  *gormadapter.Adapter
	enforcer *casbin.Enforcer
	mu       sync.Mutex
	// revision caches the tenant revision that the in-process Casbin projection
	// reflects. Every authorize/scope check compares the database revision, so a
	// missed Pub/Sub message cannot keep serving stale policy.
	revision int64
	tenant   string
	redis    *redis.Client
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
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, fmt.Errorf("create casbin adapter: %w", err)
	}
	accessModel, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("create casbin model: %w", err)
	}
	enforcer, err := casbin.NewEnforcer(accessModel, adapter)
	if err != nil {
		return nil, fmt.Errorf("create casbin enforcer: %w", err)
	}
	enforcer.EnableAutoSave(false)
	service := &Service{db: db, adapter: adapter, enforcer: enforcer}
	if err := service.RefreshPolicies(context.Background()); err != nil {
		return nil, err
	}
	return service, nil
}

// StartInvalidation subscribes to the policy-change channel so every API/worker
// instance refreshes its local projection promptly. Correctness does not depend
// on delivery: every request also compares the tenant revision in the database.
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
		for range pubsub.Channel() {
			_ = s.RefreshPolicies(context.Background())
			s.mu.Lock()
			s.revision = 0
			s.tenant = ""
			s.mu.Unlock()
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

func (s *Service) RefreshPolicies(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var memberships []models.Membership
	if err := s.db.WithContext(ctx).Find(&memberships).Error; err != nil {
		return fmt.Errorf("load tenant memberships: %w", err)
	}
	var knowledgeBases []models.KnowledgeBase
	if err := s.db.WithContext(ctx).Find(&knowledgeBases).Error; err != nil {
		return fmt.Errorf("load knowledge bases: %w", err)
	}
	var kbMembers []models.KnowledgeBaseMember
	if err := s.db.WithContext(ctx).Find(&kbMembers).Error; err != nil {
		return fmt.Errorf("load knowledge base memberships: %w", err)
	}

	if err := s.db.WithContext(ctx).Where("ptype IN ?", []string{"p", "g"}).Delete(&gormadapter.CasbinRule{}).Error; err != nil {
		return fmt.Errorf("clear casbin policy projection: %w", err)
	}
	s.enforcer.ClearPolicy()
	for _, membership := range memberships {
		if _, err := s.enforcer.AddGroupingPolicy(membership.UserID, tenantRole(membership.Role), membership.TenantID); err != nil {
			return fmt.Errorf("add tenant policy: %w", err)
		}
		if membership.Role == "owner" || membership.Role == "admin" {
			if _, err := s.enforcer.AddPolicy(tenantRole(membership.Role), membership.TenantID, "/knowledge-bases", "read|write|delete|manage_members", "allow"); err != nil {
				return fmt.Errorf("add tenant collection policy: %w", err)
			}
		}
	}
	for _, knowledgeBase := range knowledgeBases {
		for _, role := range []string{"owner", "admin"} {
			if _, err := s.enforcer.AddPolicy(tenantRole(role), knowledgeBase.TenantID, "/knowledge-bases/:id", "read|write|delete|manage_members", "allow"); err != nil {
				return fmt.Errorf("add tenant resource policy: %w", err)
			}
		}
	}
	for _, member := range kbMembers {
		var knowledgeBase models.KnowledgeBase
		if err := s.db.WithContext(ctx).First(&knowledgeBase, "id = ?", member.KnowledgeBaseID).Error; err != nil {
			return fmt.Errorf("load member knowledge base: %w", err)
		}
		role := kbRole(member.Role, member.KnowledgeBaseID)
		if _, err := s.enforcer.AddGroupingPolicy(member.UserID, role, knowledgeBase.TenantID); err != nil {
			return fmt.Errorf("add knowledge base grouping policy: %w", err)
		}
		object := resourcePath(member.KnowledgeBaseID)
		actions := "read"
		if member.Role == "owner" || member.Role == "admin" {
			actions = "read|write|delete|manage_members"
		} else if member.Role == "editor" {
			actions = "read|write"
		}
		if _, err := s.enforcer.AddPolicy(role, knowledgeBase.TenantID, object, actions, "allow"); err != nil {
			return fmt.Errorf("add knowledge base policy: %w", err)
		}
		if member.Role == "owner" || member.Role == "admin" {
			if _, err := s.enforcer.AddPolicy(role, knowledgeBase.TenantID, object+"/members", "read|manage_members", "allow"); err != nil {
				return fmt.Errorf("add member management policy: %w", err)
			}
		}
	}
	if err := s.enforcer.SavePolicy(); err != nil {
		return fmt.Errorf("save casbin policy projection: %w", err)
	}
	return nil
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

func (s *Service) ensureFresh(ctx context.Context, tenantID string) (int64, error) {
	revision, err := s.currentRevision(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	fresh := s.revision == revision && s.tenant == tenantID
	s.mu.Unlock()
	if fresh {
		return revision, nil
	}
	if err := s.RefreshPolicies(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.revision = revision
	s.tenant = tenantID
	s.mu.Unlock()
	return revision, nil
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
	if err := s.RefreshPolicies(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.revision = revision
	s.tenant = tenantID
	client := s.redis
	s.mu.Unlock()
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
	if _, err := s.ensureFresh(ctx, principal.TenantID); err != nil {
		return false, err
	}
	if !principalAllowsAction(principal, action) {
		if auditErr := s.recordAudit(ctx, principal, action, object, false, "api_key_capability_missing"); auditErr != nil {
			return false, auditErr
		}
		return false, nil
	}
	s.mu.Lock()
	allowed, err := s.enforcer.Enforce(principal.SubjectID, principal.TenantID, object, action)
	s.mu.Unlock()
	reason := "allowed"
	if err != nil {
		reason = err.Error()
	} else if !allowed {
		reason = "policy_denied"
	}
	if auditErr := s.recordAudit(ctx, principal, action, object, allowed, reason); auditErr != nil && err == nil {
		return false, auditErr
	}
	return allowed, err
}

func (s *Service) BuildScope(ctx context.Context, principal Principal, target QueryTarget) (AuthorizationScope, error) {
	if strings.TrimSpace(principal.SubjectID) == "" || strings.TrimSpace(principal.TenantID) == "" {
		return AuthorizationScope{}, apperror.ErrUnauthenticated
	}
	revision, err := s.ensureFresh(ctx, principal.TenantID)
	if err != nil {
		return AuthorizationScope{}, err
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
	return AuthorizationScope{
		SubjectID:        principal.SubjectID,
		TenantID:         principal.TenantID,
		KnowledgeBaseIDs: ids,
		ResourceIDs:      ids,
		Capabilities:     capabilities,
		ACLVersion:       fmt.Sprintf("tenant:%s:%d", principal.TenantID, revision),
	}, nil
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
