package authz

import (
	"context"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type TenantMemberView struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

func (s *Service) ListTenantMembers(ctx context.Context, principal Principal) ([]TenantMemberView, error) {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return nil, err
	}
	return s.membershipStore.List(ctx, principal.TenantID)
}

func (s *Service) AddTenantMember(ctx context.Context, principal Principal, userID, role string) (TenantMemberView, error) {
	if err := s.requireTenantAdmin(ctx, principal); err != nil {
		return TenantMemberView{}, err
	}
	if !validTenantRole(role) {
		return TenantMemberView{}, apperror.Invalidf("unsupported tenant role")
	}
	if role == "owner" && !s.isOwner(ctx, principal) {
		return TenantMemberView{}, apperror.ErrForbidden
	}
	user, ok, err := s.membershipStore.FindUser(ctx, userID)
	if err != nil {
		return TenantMemberView{}, err
	}
	if !ok {
		return TenantMemberView{}, apperror.ErrNotFound
	}
	membership := models.Membership{TenantID: principal.TenantID, UserID: userID, Role: role}
	if err := s.membershipStore.Save(ctx, membership); err != nil {
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
		return apperror.Invalidf("unsupported tenant role")
	}
	if role == "owner" && !s.isOwner(ctx, principal) {
		return apperror.ErrForbidden
	}
	membership, ok, err := s.membershipStore.Find(ctx, principal.TenantID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return apperror.ErrNotFound
	}
	if membership.Role == "owner" && role != "owner" {
		owners, err := s.membershipStore.CountOwners(ctx, principal.TenantID)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return fmt.Errorf("the last tenant owner cannot be demoted")
		}
	}
	roleChanged := membership.Role != role
	if err := s.membershipStore.UpdateRole(ctx, principal.TenantID, userID, role, roleChanged); err != nil {
		return err
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
	membership, ok, err := s.membershipStore.Find(ctx, principal.TenantID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return apperror.ErrNotFound
	}
	if membership.Role == "owner" {
		owners, err := s.membershipStore.CountOwners(ctx, principal.TenantID)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return fmt.Errorf("the last tenant owner cannot be removed")
		}
	}
	if err := s.membershipStore.Remove(ctx, principal.TenantID, userID); err != nil {
		return err
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
	membership, ok, err := s.membershipStore.Find(ctx, principal.TenantID, principal.SubjectID)
	if err != nil || !ok {
		return apperror.ErrForbidden
	}
	if membership.Role != "owner" && membership.Role != "admin" {
		return apperror.ErrForbidden
	}
	return nil
}

func (s *Service) isOwner(ctx context.Context, principal Principal) bool {
	membership, ok, err := s.membershipStore.Find(ctx, principal.TenantID, principal.SubjectID)
	if err != nil || !ok {
		return false
	}
	return membership.Role == "owner"
}

func (s *Service) recordAdminAudit(ctx context.Context, principal Principal, action, resourceType, resourceID, reason string) error {
	return s.auditStore.Create(ctx, models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action, ResourceType: resourceType, ResourceID: resourceID, Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	})
}

func tenantRole(role string) string { return "tenant_" + role }

func kbRole(role, id string) string { return "kb_" + role + ":" + id }

func validTenantRole(role string) bool {
	switch role {
	case "owner", "admin", "member", "viewer":
		return true
	default:
		return false
	}
}
