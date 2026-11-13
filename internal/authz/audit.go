package authz

import (
	"context"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type AuditQuery struct {
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Allowed      *bool
	Limit        int
	Offset       int
}

// AuditPage is one server-paginated page of the audit trail.
type AuditPage struct {
	Items  []models.AuditLog
	Total  int64
	Limit  int
	Offset int
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

func (s *Service) ListAudit(ctx context.Context, principal Principal, limit int) ([]models.AuditLog, error) {
	return s.QueryAudit(ctx, principal, AuditQuery{Limit: limit})
}

// QueryAudit lets tenant owners/admins see the tenant trail while every other
// role sees only its own actions. Permission denials are included, so a denied
// retrieval leaves a trace.
func (s *Service) QueryAudit(ctx context.Context, principal Principal, query AuditQuery) ([]models.AuditLog, error) {
	filter, err := s.auditFilter(ctx, principal, query)
	if err != nil {
		return nil, err
	}
	return s.auditStore.List(ctx, principal.TenantID, filter)
}

// QueryAuditPage returns one page plus the filtered total for the workbench.
func (s *Service) QueryAuditPage(ctx context.Context, principal Principal, query AuditQuery) (AuditPage, error) {
	filter, err := s.auditFilter(ctx, principal, query)
	if err != nil {
		return AuditPage{}, err
	}
	rows, err := s.auditStore.List(ctx, principal.TenantID, filter)
	if err != nil {
		return AuditPage{}, err
	}
	total, err := s.auditStore.Count(ctx, principal.TenantID, filter)
	if err != nil {
		return AuditPage{}, err
	}
	return AuditPage{Items: rows, Total: total, Limit: filter.Limit, Offset: filter.Offset}, nil
}

func (s *Service) auditFilter(ctx context.Context, principal Principal, query AuditQuery) (AuditFilter, error) {
	role, ok, err := s.auditStore.MemberRole(ctx, principal.TenantID, principal.SubjectID)
	if err != nil {
		return AuditFilter{}, err
	}
	if !ok {
		return AuditFilter{}, apperror.ErrForbidden
	}
	filter := AuditFilter{
		Action: query.Action, ResourceType: query.ResourceType, ResourceID: query.ResourceID,
		Allowed: query.Allowed, Limit: query.Limit, Offset: query.Offset,
	}
	if role != "owner" && role != "admin" {
		filter.ActorID = principal.SubjectID
	} else if query.ActorID != "" {
		filter.ActorID = query.ActorID
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter, nil
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
	return s.auditStore.Purge(ctx, before, limit)
}

func (s *Service) recordAudit(ctx context.Context, principal Principal, action, object string, allowed bool, reason string) error {
	if principal.CredentialType == "api_key" {
		reason += ";credential=api_key:" + principal.CredentialID
	}
	return s.auditStore.Create(ctx, models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action, ResourceType: "path", ResourceID: object, Allowed: allowed, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	})
}
