// Package configpreview computes the impact of a proposed business
// configuration change before it is saved. It is read-only: it never writes
// configuration, triggers a rebuild or changes an index.
package configpreview

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
	"gorm.io/gorm"
)

const (
	ScopeTenant        = "tenant"
	ScopeKnowledgeBase = "knowledge_base"
)

const (
	ImpactNone       = "none"
	ImpactHot        = "hot"
	ImpactNextIngest = "next_ingest"
	ImpactRebuild    = "rebuild"
	ImpactRestart    = "restart"
)

const (
	ActionApplyNow           = "apply_now"
	ActionRebuildOptional    = "rebuild_history_optional"
	ActionMigrateEmbedding   = "open_embedding_migration"
	ActionRebuildRequired    = "rebuild_required"
	ActionRestartRequired    = "restart_required"
	BlockerEmbeddingContract = "embedding_contract_requires_rebuild"
)

// Input is one proposed change. Retrieval and chunking replace the current
// override documents, matching PUT semantics.
type Input struct {
	Scope              string
	KnowledgeBaseID    string
	Retrieval          map[string]json.RawMessage
	Chunking           map[string]json.RawMessage
	EmbeddingModel     string
	EmbeddingDimension *int
}

// Change is one effective value that differs from the current configuration.
type Change struct {
	Domain       string          `json:"domain"`
	Key          string          `json:"key"`
	OldValue     json.RawMessage `json:"oldValue"`
	NewValue     json.RawMessage `json:"newValue"`
	Source       string          `json:"source"`
	ApplyMode    string          `json:"applyMode"`
	Impact       string          `json:"impact"`
	RebuildScope string          `json:"rebuildScope"`
	Risk         string          `json:"risk"`
}

// Blocker prevents the change from being applied without a migration.
type Blocker struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
}

// Affected counts the data a history rebuild would touch.
type Affected struct {
	KnowledgeBases int64 `json:"knowledgeBases"`
	Documents      int64 `json:"documents"`
	Chunks         int64 `json:"chunks"`
}

// Preview is the read-only impact report.
type Preview struct {
	Scope                 string    `json:"scope"`
	KnowledgeBaseID       string    `json:"knowledgeBaseId,omitempty"`
	Changes               []Change  `json:"changes"`
	Impact                string    `json:"impact"`
	RebuildRequired       bool      `json:"rebuildRequired"`
	RebuildOptional       bool      `json:"rebuildOptional"`
	RestartRequired       bool      `json:"restartRequired"`
	Blockers              []Blocker `json:"blockers"`
	Affected              Affected  `json:"affected"`
	EstimatedDurationTier string    `json:"estimatedDurationTier"`
	EstimatedCostTier     string    `json:"estimatedCostTier"`
	Actions               []string  `json:"actions"`
}

// Authorizer gates the two scopes.
type Authorizer interface {
	RequireTenantOperator(ctx context.Context, principal authz.Principal) error
	AuthorizeKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID, action string) (bool, error)
}

type Options struct {
	DB                 *gorm.DB
	Authz              Authorizer
	Deployment         tenantconfig.Deployment
	EmbeddingModel     string
	EmbeddingDimension int
}

type Service struct {
	db         *gorm.DB
	authz      Authorizer
	deployment tenantconfig.Deployment
	embedding  string
	dimension  int
}

func NewService(options Options) *Service {
	return &Service{
		db: options.DB, authz: options.Authz, deployment: options.Deployment,
		embedding: options.EmbeddingModel, dimension: options.EmbeddingDimension,
	}
}

// Preview validates the proposed values, loads the current tiers and returns
// the impact. It never persists anything.
func (s *Service) Preview(ctx context.Context, principal authz.Principal, input Input) (Preview, error) {
	if input.Retrieval == nil {
		input.Retrieval = map[string]json.RawMessage{}
	}
	if input.Chunking == nil {
		input.Chunking = map[string]json.RawMessage{}
	}
	if err := configschema.Validate(configschema.DomainRetrieval, input.Retrieval); err != nil {
		return Preview{}, err
	}
	if err := configschema.Validate(configschema.DomainChunking, input.Chunking); err != nil {
		return Preview{}, err
	}
	var currentTenant models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).Where("id = ?", principal.TenantID).Take(&currentTenant).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Preview{}, apperror.ErrNotFound
		}
		return Preview{}, fmt.Errorf("load tenant: %w", err)
	}
	currentRetrieval, err := parseMap(currentTenant.RetrievalConfig)
	if err != nil {
		return Preview{}, err
	}
	currentChunking, err := parseMap(currentTenant.ChunkingConfig)
	if err != nil {
		return Preview{}, err
	}

	switch input.Scope {
	case ScopeTenant:
		return s.previewTenant(ctx, principal, currentTenant, currentRetrieval, currentChunking, input)
	case ScopeKnowledgeBase:
		return s.previewKnowledgeBase(ctx, principal, currentTenant, currentRetrieval, input)
	default:
		return Preview{}, apperror.Invalidf("scope must be tenant or knowledge_base")
	}
}

func (s *Service) previewTenant(ctx context.Context, principal authz.Principal, tenant models.Tenant, currentRetrieval, currentChunking map[string]json.RawMessage, input Input) (Preview, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return Preview{}, err
	}
	var kbs []models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Select("id, retrieval_config").Order("id ASC").Find(&kbs).Error; err != nil {
		return Preview{}, fmt.Errorf("load knowledge bases: %w", err)
	}
	retrievalChanges, retrievalAffected, err := diffTenant(configschema.DomainRetrieval, s.deployment.Retrieval, currentRetrieval, input.Retrieval, kbs)
	if err != nil {
		return Preview{}, err
	}
	chunkingChanges, _, err := diffTenant(configschema.DomainChunking, s.deployment.Chunking, currentChunking, input.Chunking, kbs)
	if err != nil {
		return Preview{}, err
	}
	changes := append(retrievalChanges, chunkingChanges...)
	affectedIDs := map[string]bool{}
	for id := range retrievalAffected {
		affectedIDs[id] = true
	}
	if len(chunkingChanges) > 0 {
		for _, kb := range kbs {
			affectedIDs[kb.ID] = true
		}
	}
	embeddingChanges, embeddingBlocker, err := embeddingDiff(s.embedding, s.dimension, input.EmbeddingModel, input.EmbeddingDimension)
	if err != nil {
		return Preview{}, err
	}
	if len(embeddingChanges) > 0 {
		changes = append(changes, embeddingChanges...)
		for _, kb := range kbs {
			affectedIDs[kb.ID] = true
		}
	}
	ids := keys(affectedIDs)
	documents, chunks, err := s.counts(ctx, principal.TenantID, ids)
	if err != nil {
		return Preview{}, err
	}
	blockers := []Blocker{}
	if embeddingBlocker != nil {
		blockers = append(blockers, *embeddingBlocker)
	}
	blockers = append(blockers, rebuildBlockers(changes)...)
	return summarize(ScopeTenant, "", changes, blockers, Affected{
		KnowledgeBases: int64(len(ids)), Documents: documents, Chunks: chunks,
	}), nil
}

func (s *Service) previewKnowledgeBase(ctx context.Context, principal authz.Principal, tenant models.Tenant, currentRetrieval map[string]json.RawMessage, input Input) (Preview, error) {
	if strings.TrimSpace(input.KnowledgeBaseID) == "" {
		return Preview{}, apperror.Invalidf("knowledgeBaseId is required")
	}
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, input.KnowledgeBaseID, "write")
	if err != nil {
		return Preview{}, err
	}
	if !allowed {
		return Preview{}, apperror.ErrForbidden
	}
	if len(input.Chunking) > 0 {
		return Preview{}, apperror.Invalidf("knowledge base chunking is not configurable yet")
	}
	var kb models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", input.KnowledgeBaseID).Take(&kb).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Preview{}, apperror.ErrNotFound
		}
		return Preview{}, fmt.Errorf("load knowledge base: %w", err)
	}
	currentKB, err := parseMap(kb.RetrievalConfig)
	if err != nil {
		return Preview{}, err
	}
	changes, changed, err := diffKnowledgeBase(configschema.DomainRetrieval, s.deployment.Retrieval, currentRetrieval, currentKB, input.Retrieval)
	if err != nil {
		return Preview{}, err
	}
	embeddingChanges, embeddingBlocker, err := embeddingDiff(
		firstNonEmpty(kb.EmbeddingModel, s.embedding),
		firstNonZero(kb.EmbeddingDimension, s.dimension),
		input.EmbeddingModel, input.EmbeddingDimension,
	)
	if err != nil {
		return Preview{}, err
	}
	if len(embeddingChanges) > 0 {
		changes = append(changes, embeddingChanges...)
	}
	ids := []string{}
	if len(changes) > 0 {
		ids = []string{kb.ID}
	}
	documents, chunks, err := s.counts(ctx, principal.TenantID, ids)
	if err != nil {
		return Preview{}, err
	}
	blockers := []Blocker{}
	if embeddingBlocker != nil {
		blockers = append(blockers, *embeddingBlocker)
	}
	blockers = append(blockers, rebuildBlockers(changes)...)
	affected := Affected{}
	if changed || len(embeddingChanges) > 0 {
		affected = Affected{KnowledgeBases: 1, Documents: documents, Chunks: chunks}
	}
	return summarize(ScopeKnowledgeBase, kb.ID, changes, blockers, affected), nil
}

func diffTenant(domain string, deployment configschema.Tier, current, proposed map[string]json.RawMessage, kbs []models.KnowledgeBase) ([]Change, map[string]bool, error) {
	changes := make([]Change, 0)
	affected := map[string]bool{}
	for _, field := range configschema.FieldsFor(domain) {
		oldState := configschema.Resolve(domain, deployment, current, nil, nil)[field.Key]
		newState := configschema.Resolve(domain, deployment, proposed, nil, nil)[field.Key]
		if sameJSON(oldState.Value, newState.Value) {
			continue
		}
		changes = append(changes, changeOf(field, oldState.Value, newState.Value, newState.Source))
		for _, kb := range kbs {
			kbValues, err := parseMap(kb.RetrievalConfig)
			if err != nil {
				return nil, nil, fmt.Errorf("knowledge base %s retrieval config: %w", kb.ID, err)
			}
			oldKB := configschema.Resolve(domain, deployment, current, kbValues, nil)[field.Key]
			newKB := configschema.Resolve(domain, deployment, proposed, kbValues, nil)[field.Key]
			if !sameJSON(oldKB.Value, newKB.Value) {
				affected[kb.ID] = true
			}
		}
	}
	return changes, affected, nil
}

func diffKnowledgeBase(domain string, deployment configschema.Tier, tenantValues, currentKB, proposedKB map[string]json.RawMessage) ([]Change, bool, error) {
	changes := make([]Change, 0)
	for _, field := range configschema.FieldsFor(domain) {
		oldState := configschema.Resolve(domain, deployment, tenantValues, currentKB, nil)[field.Key]
		newState := configschema.Resolve(domain, deployment, tenantValues, proposedKB, nil)[field.Key]
		if sameJSON(oldState.Value, newState.Value) {
			continue
		}
		changes = append(changes, changeOf(field, oldState.Value, newState.Value, newState.Source))
	}
	return changes, len(changes) > 0, nil
}

func changeOf(field configschema.Field, oldValue, newValue json.RawMessage, source string) Change {
	return Change{
		Domain: field.Domain, Key: field.Key, OldValue: oldValue, NewValue: newValue,
		Source: source, ApplyMode: field.ApplyMode, Impact: field.Impact,
		RebuildScope: field.RebuildScope, Risk: field.Risk,
	}
}

func embeddingDiff(currentModel string, currentDimension int, requestedModel string, requestedDimension *int) ([]Change, *Blocker, error) {
	changes := make([]Change, 0)
	if strings.TrimSpace(requestedModel) != "" && requestedModel != currentModel {
		changes = append(changes, embeddingChange("model", currentModel, requestedModel))
	}
	if requestedDimension != nil {
		if *requestedDimension <= 0 {
			return nil, nil, apperror.Invalidf("embeddingDimension must be a positive integer")
		}
		if *requestedDimension != currentDimension {
			changes = append(changes, embeddingChange("dimension", currentDimension, *requestedDimension))
		}
	}
	if len(changes) == 0 {
		return nil, nil, nil
	}
	blocker := &Blocker{
		Code:        BlockerEmbeddingContract,
		Message:     "Embedding 模型或维度变更属于数据契约变更，旧 Chunk 向量与新配置不兼容。",
		Remediation: "通过知识库「模型迁移」发起 create-index → reembed → verify，完成前不要保存并对外提供检索。",
	}
	return changes, blocker, nil
}

func embeddingChange(key string, oldValue, newValue any) Change {
	oldJSON, _ := json.Marshal(oldValue)
	newJSON, _ := json.Marshal(newValue)
	return Change{
		Domain: "embedding", Key: key, OldValue: oldJSON, NewValue: newJSON,
		Source: "deployment", ApplyMode: "rebuild", Impact: ImpactRebuild,
		RebuildScope: configschema.RebuildReembed, Risk: configschema.RiskHigh,
	}
}

func rebuildBlockers(changes []Change) []Blocker {
	blockers := make([]Blocker, 0)
	for _, change := range changes {
		if change.Impact != ImpactRebuild || change.Domain == "embedding" {
			continue
		}
		blockers = append(blockers, Blocker{
			Code:        "rebuild_required",
			Message:     change.Domain + "." + change.Key + " 需要重建后才能生效。",
			Remediation: "先完成对应重建任务，再保存该配置。",
		})
	}
	return blockers
}

func summarize(scope, knowledgeBaseID string, changes []Change, blockers []Blocker, affected Affected) Preview {
	preview := Preview{
		Scope: scope, KnowledgeBaseID: knowledgeBaseID, Changes: changes,
		Blockers: blockers, Affected: affected, Impact: ImpactNone,
	}
	rank := map[string]int{ImpactNone: 0, ImpactHot: 1, ImpactNextIngest: 2, ImpactRebuild: 3, ImpactRestart: 4}
	for _, change := range changes {
		if rank[change.Impact] > rank[preview.Impact] {
			preview.Impact = change.Impact
		}
	}
	if len(blockers) > 0 {
		preview.RebuildRequired = true
	}
	switch preview.Impact {
	case ImpactNextIngest:
		preview.RebuildOptional = true
	case ImpactRebuild:
		preview.RebuildRequired = true
	case ImpactRestart:
		preview.RestartRequired = true
	}
	if preview.Impact == ImpactHot || preview.Impact == ImpactNone {
		preview.EstimatedDurationTier, preview.EstimatedCostTier = "none", "none"
	} else {
		preview.EstimatedDurationTier, preview.EstimatedCostTier = estimateTier(affected.Chunks)
	}
	preview.Actions = actionsFor(preview)
	return preview
}

func actionsFor(preview Preview) []string {
	if preview.RestartRequired {
		return []string{ActionRestartRequired}
	}
	if preview.RebuildRequired {
		for _, blocker := range preview.Blockers {
			if blocker.Code == BlockerEmbeddingContract {
				return []string{ActionMigrateEmbedding}
			}
		}
		return []string{ActionRebuildRequired}
	}
	if preview.Impact == ImpactNextIngest {
		return []string{ActionApplyNow, ActionRebuildOptional}
	}
	if preview.Impact == ImpactHot {
		return []string{ActionApplyNow}
	}
	return []string{}
}

func estimateTier(chunks int64) (string, string) {
	switch {
	case chunks <= 0:
		return "none", "none"
	case chunks <= 1_000:
		return "low", "low"
	case chunks <= 10_000:
		return "medium", "medium"
	default:
		return "high", "high"
	}
}

func (s *Service) counts(ctx context.Context, tenantID string, knowledgeBaseIDs []string) (int64, int64, error) {
	if len(knowledgeBaseIDs) == 0 {
		return 0, 0, nil
	}
	db := tenantdb.FromContext(ctx, s.db)
	var documents int64
	if err := db.Table("documents").
		Where("tenant_id = ? AND deleted_at IS NULL AND knowledge_base_id IN ?", tenantID, knowledgeBaseIDs).
		Count(&documents).Error; err != nil {
		return 0, 0, fmt.Errorf("count documents: %w", err)
	}
	var chunks int64
	if err := db.Table("chunks").
		Where("tenant_id = ? AND knowledge_base_id IN ?", tenantID, knowledgeBaseIDs).
		Count(&chunks).Error; err != nil {
		return 0, 0, fmt.Errorf("count chunks: %w", err)
	}
	return documents, chunks, nil
}

func parseMap(raw string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode stored configuration: %w", err)
	}
	return values, nil
}

func sameJSON(left, right json.RawMessage) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return string(left) == string(right)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return string(left) == string(right)
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func keys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
