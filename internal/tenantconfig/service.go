// Package tenantconfig owns the tenant tier of the business configuration
// chain. Deployment connection settings stay in env/YAML; retrieval and
// chunking policy are tenant-scoped database rows so an operator can tune them
// without a redeploy.
package tenantconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

const SchemaVersion = "TenantConfigurationV1"

// ChunkPolicy mirrors the deployment chunking values without importing the
// config loader into this domain.
type ChunkPolicy struct {
	Size    int
	Overlap int
	MinSize int
	Markers []string
}

func (p ChunkPolicy) Validate() error {
	return configschema.ValidateNative(configschema.DomainChunking, map[string]any{
		"size": p.Size, "overlap": p.Overlap, "min_size": p.MinSize, "split_markers": p.Markers,
	})
}

// Deployment is the read-only deployment tier handed in by the composition
// root. It keeps this package independent of internal/config.
type Deployment struct {
	Retrieval configschema.Tier
	Chunking  configschema.Tier
	Chunk     ChunkPolicy
}

// Input is one whole replacement of the tenant override object.
type Input struct {
	Retrieval map[string]json.RawMessage
	Chunking  map[string]json.RawMessage
}

// DomainView is one configuration domain with the schema, the three visible
// tiers and the resolved state per field.
type DomainView struct {
	Fields         []configschema.Field
	Deployment     configschema.Tier
	TenantOverride map[string]json.RawMessage
	Effective      map[string]json.RawMessage
	States         map[string]configschema.State
}

// View is the tenant configuration surface.
type View struct {
	Retrieval DomainView
	Chunking  DomainView
}

// Authorizer gates the tenant administration surface. The authz service
// implements it; tests can supply a stub without a policy database.
type Authorizer interface {
	RequireTenantOperator(ctx context.Context, principal authz.Principal) error
}

// Service reads and writes the tenant tier and resolves it for the query and
// ingestion paths.
type Service struct {
	db         *gorm.DB
	authz      Authorizer
	deployment Deployment
	quality    QualityGate
}

// QualityGate blocks high-risk retrieval saves without a recent evaluation.
type QualityGate interface {
	RequireRecentEval(ctx context.Context, tenantID, knowledgeBaseID string) error
}

func (s *Service) SetQualityGate(gate QualityGate) { s.quality = gate }

func NewService(db *gorm.DB, authorizer Authorizer, deployment Deployment) *Service {
	return &Service{db: db, authz: authorizer, deployment: deployment}
}

// Get returns the tenant configuration view. It requires a tenant operator
// because it exposes policy that affects every knowledge base in the tenant.
func (s *Service) Get(ctx context.Context, principal authz.Principal) (View, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return View{}, err
	}
	row, err := s.loadTenant(ctx, principal.TenantID)
	if err != nil {
		return View{}, err
	}
	return s.buildView(row), nil
}

// Put replaces both tenant override objects and records one audit row in the
// same transaction. Empty maps clear the tier.
func (s *Service) Put(ctx context.Context, principal authz.Principal, input Input) (View, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return View{}, err
	}
	override, retrievalJSON, err := normalizeRetrieval(input.Retrieval)
	if err != nil {
		return View{}, err
	}
	chunkingJSON, err := normalizeChunking(input.Chunking)
	if err != nil {
		return View{}, err
	}
	before, beforeErr := s.loadTenant(ctx, principal.TenantID)
	if s.quality != nil && override.RerankEnabled != nil && *override.RerankEnabled && !tenantRerankEnabled(before) {
		if err := s.quality.RequireRecentEval(ctx, principal.TenantID, ""); err != nil {
			return View{}, err
		}
	}
	reason := configAuditReason(before, beforeErr, retrievalJSON, chunkingJSON)
	audit := models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID,
		Action: "update_tenant_config", ResourceType: "tenant_config", ResourceID: principal.TenantID,
		Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}
	if err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := tx.Model(&models.Tenant{}).Where("id = ?", principal.TenantID).
			Updates(map[string]any{"retrieval_config": retrievalJSON, "chunking_config": chunkingJSON}).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Create(&audit).Error
	}); err != nil {
		return View{}, fmt.Errorf("update tenant configuration: %w", err)
	}
	row, err := s.loadTenant(ctx, principal.TenantID)
	if err != nil {
		return View{}, err
	}
	return s.buildView(row), nil
}

// TenantRetrievalOverride returns the raw tenant tier for the retrieval path.
// An empty tenant id or a tenant without an override yields an empty document.
func (s *Service) TenantRetrievalOverride(ctx context.Context, tenantID string) (string, error) {
	if strings.TrimSpace(tenantID) == "" {
		return "", nil
	}
	var row models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).Where("id = ?", tenantID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", fmt.Errorf("load tenant retrieval config: %w", err)
	}
	return row.RetrievalConfig, nil
}

// ChunkingForTenant resolves the deployment chunking policy with the tenant
// override applied. Callers build a chunker from the result for each document;
// existing chunks are not rewritten.
func (s *Service) ChunkingForTenant(ctx context.Context, tenantID string) (ChunkPolicy, error) {
	resolved := s.deployment.Chunk
	if strings.TrimSpace(tenantID) == "" {
		return resolved, nil
	}
	var row models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).Where("id = ?", tenantID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return resolved, nil
		}
		return ChunkPolicy{}, fmt.Errorf("load tenant chunking config: %w", err)
	}
	override, err := parseObject(row.ChunkingConfig)
	if err != nil {
		return ChunkPolicy{}, err
	}
	if len(override) == 0 {
		return resolved, nil
	}
	if err := configschema.Validate(configschema.DomainChunking, override); err != nil {
		return ChunkPolicy{}, err
	}
	var typed chunkingOverride
	encoded, err := json.Marshal(override)
	if err != nil {
		return ChunkPolicy{}, fmt.Errorf("encode tenant chunking config: %w", err)
	}
	if err := json.Unmarshal(encoded, &typed); err != nil {
		return ChunkPolicy{}, apperror.Invalidf("decode tenant chunking config: %w", err)
	}
	resolved = typed.Apply(resolved)
	if err := resolved.Validate(); err != nil {
		return ChunkPolicy{}, err
	}
	return resolved, nil
}

func tenantRerankEnabled(row models.Tenant) bool {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.RetrievalConfig), &value); err != nil {
		return false
	}
	raw, ok := value["rerank_enabled"]
	if !ok {
		return false
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return false
	}
	return enabled
}

func (s *Service) loadTenant(ctx context.Context, tenantID string) (models.Tenant, error) {
	var row models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).Where("id = ?", tenantID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.Tenant{}, apperror.ErrNotFound
		}
		return models.Tenant{}, fmt.Errorf("load tenant configuration: %w", err)
	}
	return row, nil
}

func (s *Service) buildView(row models.Tenant) View {
	retrievalOverride, _ := parseObject(row.RetrievalConfig)
	chunkingOverride, _ := parseObject(row.ChunkingConfig)
	if retrievalOverride == nil {
		retrievalOverride = map[string]json.RawMessage{}
	}
	if chunkingOverride == nil {
		chunkingOverride = map[string]json.RawMessage{}
	}
	return View{
		Retrieval: s.domainView(configschema.DomainRetrieval, s.deployment.Retrieval, retrievalOverride),
		Chunking:  s.domainView(configschema.DomainChunking, s.deployment.Chunking, chunkingOverride),
	}
}

func (s *Service) domainView(domain string, deployment configschema.Tier, tenant map[string]json.RawMessage) DomainView {
	states := configschema.Resolve(domain, deployment, tenant, nil, nil)
	effective := make(map[string]json.RawMessage, len(states))
	for key, state := range states {
		effective[key] = state.Value
	}
	return DomainView{
		Fields:         configschema.FieldsFor(domain),
		Deployment:     deployment,
		TenantOverride: tenant,
		Effective:      effective,
		States:         states,
	}
}

type chunkingOverride struct {
	Size         *int      `json:"size,omitempty"`
	Overlap      *int      `json:"overlap,omitempty"`
	MinSize      *int      `json:"min_size,omitempty"`
	SplitMarkers *[]string `json:"split_markers,omitempty"`
}

func (o chunkingOverride) Apply(base ChunkPolicy) ChunkPolicy {
	if o.Size != nil {
		base.Size = *o.Size
	}
	if o.Overlap != nil {
		base.Overlap = *o.Overlap
	}
	if o.MinSize != nil {
		base.MinSize = *o.MinSize
	}
	if o.SplitMarkers != nil {
		base.Markers = append([]string(nil), (*o.SplitMarkers)...)
	}
	return base
}

func normalizeRetrieval(values map[string]json.RawMessage) (retrieval.ParamsOverride, string, error) {
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	if err := configschema.Validate(configschema.DomainRetrieval, values); err != nil {
		return retrieval.ParamsOverride{}, "", err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return retrieval.ParamsOverride{}, "", apperror.Invalidf("encode retrieval parameters: %w", err)
	}
	override, err := retrieval.ParseOverride(string(encoded))
	if err != nil {
		return retrieval.ParamsOverride{}, "", err
	}
	if err := retrieval.ValidateOverride(override); err != nil {
		return retrieval.ParamsOverride{}, "", err
	}
	normalized, err := json.Marshal(override)
	if err != nil {
		return retrieval.ParamsOverride{}, "", apperror.Invalidf("encode retrieval parameters: %w", err)
	}
	return override, string(normalized), nil
}

func normalizeChunking(values map[string]json.RawMessage) (string, error) {
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	if err := configschema.Validate(configschema.DomainChunking, values); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", apperror.Invalidf("encode chunking parameters: %w", err)
	}
	return string(encoded), nil
}

func parseObject(raw string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode configuration document: %w", err)
	}
	return values, nil
}

func configAuditReason(before models.Tenant, beforeErr error, afterRetrieval, afterChunking string) string {
	keys := make([]string, 0, 2)
	if beforeErr != nil {
		keys = append(keys, "before=unreadable")
	} else {
		keys = append(keys, "retrieval_before="+compact(before.RetrievalConfig), "chunking_before="+compact(before.ChunkingConfig))
	}
	keys = append(keys, "retrieval_after="+compact(afterRetrieval), "chunking_after="+compact(afterChunking))
	sort.Strings(keys)
	return strings.Join(keys, "; ")
}

func compact(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}
