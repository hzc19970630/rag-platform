// Package tenant owns the tenant-level administration surface: the overview
// an operator needs and the quota overrides enforcement reads at runtime.
package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"github.com/example/rag-platform/internal/platform/tenantquota"
	"gorm.io/gorm"
)

// userMessageRole mirrors conversation.MessageRoleUser. It is the persisted
// value counted against the daily chat budget.
const userMessageRole = "user"

type Service struct {
	db     *gorm.DB
	authz  *authz.Service
	quotas *tenantquota.Resolver
}

func NewService(db *gorm.DB, authorizer *authz.Service, quotas *tenantquota.Resolver) *Service {
	return &Service{db: db, authz: authorizer, quotas: quotas}
}

type MemberCounts struct {
	Total   int64
	Owners  int64
	Admins  int64
	Members int64
	Viewers int64
}

type Overview struct {
	ID                 string
	Name               string
	CreatedAt          time.Time
	Members            MemberCounts
	KnowledgeBases     int64
	Documents          int64
	StorageBytes       int64
	ChatQuestionsToday int64
}

// Overview reports the tenant's current shape. Every count is scoped to the
// caller's principal tenant; there is no tenant id input to cross.
func (s *Service) Overview(ctx context.Context, principal authz.Principal) (Overview, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return Overview{}, err
	}
	var row models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).First(&row, "id = ?", principal.TenantID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Overview{}, apperror.ErrNotFound
		}
		return Overview{}, fmt.Errorf("load tenant: %w", err)
	}
	members, err := s.memberCounts(ctx, principal.TenantID)
	if err != nil {
		return Overview{}, err
	}
	var knowledgeBases int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Model(&models.KnowledgeBase{}).Count(&knowledgeBases).Error; err != nil {
		return Overview{}, fmt.Errorf("count tenant knowledge bases: %w", err)
	}
	var usage struct {
		Documents int64
		Bytes     int64
	}
	usageQuery := `SELECT
	    (SELECT COUNT(*) FROM documents d WHERE d.tenant_id = ? AND d.deleted_at IS NULL) AS documents,
	    (SELECT COALESCE(SUM(v.size_bytes), 0) FROM document_versions v JOIN documents d ON d.id = v.document_id WHERE d.tenant_id = ? AND d.deleted_at IS NULL) AS bytes`
	if err := tenantdb.FromContext(ctx, s.db).Raw(usageQuery, principal.TenantID, principal.TenantID).Scan(&usage).Error; err != nil {
		return Overview{}, fmt.Errorf("load tenant usage: %w", err)
	}
	start := time.Now().UTC().Truncate(24 * time.Hour)
	var chatQuestions int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Model(&models.Message{}).
		Where("role = ? AND created_at >= ?", userMessageRole, start).
		Count(&chatQuestions).Error; err != nil {
		return Overview{}, fmt.Errorf("count tenant chat questions: %w", err)
	}
	return Overview{
		ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt,
		Members: members, KnowledgeBases: knowledgeBases,
		Documents: usage.Documents, StorageBytes: usage.Bytes,
		ChatQuestionsToday: chatQuestions,
	}, nil
}

func (s *Service) memberCounts(ctx context.Context, tenantID string) (MemberCounts, error) {
	var rows []struct {
		Role  string
		Count int64
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Model(&models.Membership{}).
		Select("role, COUNT(*) AS count").
		Group("role").
		Scan(&rows).Error; err != nil {
		return MemberCounts{}, fmt.Errorf("count tenant members: %w", err)
	}
	var counts MemberCounts
	for _, row := range rows {
		counts.Total += row.Count
		switch row.Role {
		case "owner":
			counts.Owners = row.Count
		case "admin":
			counts.Admins = row.Count
		case "member":
			counts.Members = row.Count
		case "viewer":
			counts.Viewers = row.Count
		}
	}
	return counts, nil
}

// Quota returns the resolved quota: tenant overrides applied over deployment
// defaults, with each field's source.
func (s *Service) Quota(ctx context.Context, principal authz.Principal) (tenantquota.Effective, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return tenantquota.Effective{}, err
	}
	return s.quotas.Effective(ctx, principal.TenantID)
}

// UpdateQuota replaces the tenant override object and records the change in
// the tenant audit trail. The save and the audit row commit together.
func (s *Service) UpdateQuota(ctx context.Context, principal authz.Principal, overrides tenantquota.Overrides) (tenantquota.Effective, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return tenantquota.Effective{}, err
	}
	if err := overrides.Validate(); err != nil {
		return tenantquota.Effective{}, apperror.Invalidf("%s", err)
	}
	before, beforeErr := s.quotas.Overrides(ctx, principal.TenantID)
	reason := quotaAuditReason(before, beforeErr, overrides)
	audit := models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID,
		Action: "update_tenant_quota", ResourceType: "tenant", ResourceID: principal.TenantID,
		Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}
	if err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := s.quotas.SaveTo(ctx, tx, principal.TenantID, overrides); err != nil {
			return err
		}
		return tx.WithContext(ctx).Create(&audit).Error
	}); err != nil {
		return tenantquota.Effective{}, err
	}
	return tenantquota.Resolve(s.quotas.Defaults(), overrides), nil
}

func quotaAuditReason(before tenantquota.Overrides, beforeErr error, after tenantquota.Overrides) string {
	beforeJSON := "unreadable"
	if beforeErr == nil {
		if encoded, err := json.Marshal(before); err == nil {
			beforeJSON = string(encoded)
		}
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		afterJSON = []byte("unreadable")
	}
	return "before=" + beforeJSON + ";after=" + string(afterJSON)
}
