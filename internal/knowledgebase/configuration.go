package knowledgebase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

const SchemaKnowledgeBaseConfigurationV1 = "KnowledgeBaseConfigurationV1"

const (
	retrievalSourceGlobal        = "global"
	retrievalSourceKnowledgeBase = "knowledge_base"
)

const (
	modelSourceDeployment    = "deployment"
	modelSourceKnowledgeBase = "knowledge_base"
)

// ModelBindingEntryV1 reports the effective model and where it came from: a
// knowledge-base override or the deployment default.
type ModelBindingEntryV1 struct {
	Model   string `json:"model"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

// ModelBindingV1 is the settings-page view of routing. Embedding is reported
// as pinned: the vector contract moves only through the managed rebuild (D3).
type ModelBindingV1 struct {
	Chat                ModelBindingEntryV1 `json:"chat"`
	Rerank              ModelBindingEntryV1 `json:"rerank"`
	Embedding           ModelBindingEntryV1 `json:"embedding"`
	EmbeddingDimension  int                 `json:"embeddingDimension"`
	EmbeddingLocked     bool                `json:"embeddingLocked"`
	EmbeddingLockReason string              `json:"embeddingLockReason,omitempty"`
}

// RetrievalParamsV1 is the wire shape for resolved retrieval parameters. The
// domain config type intentionally has YAML tags only, so reports and stored
// documents keep their existing field names; this view is the API contract.
type RetrievalParamsV1 struct {
	TopK                 int     `json:"top_k"`
	EmbeddingTopK        int     `json:"embedding_top_k"`
	KeywordTopK          int     `json:"keyword_top_k"`
	VectorThreshold      float64 `json:"vector_threshold"`
	KeywordThreshold     float64 `json:"keyword_threshold"`
	RerankEnabled        bool    `json:"rerank_enabled"`
	RerankTopK           int     `json:"rerank_top_k"`
	RerankThreshold      float64 `json:"rerank_threshold"`
	VectorEFSearch       int     `json:"vector_ef_search"`
	VectorOversample     int     `json:"vector_oversample"`
	VectorIterativeScan  bool    `json:"vector_iterative_scan"`
	ContextTokenLimit    int     `json:"context_token_limit"`
	AdjacentExpansion    int     `json:"adjacent_expansion"`
	MaxAdjacentChunks    int     `json:"max_adjacent_chunks"`
	RRFK                 int     `json:"rrf_k"`
	ExpansionScoreFactor float64 `json:"expansion_score_factor"`
}

type RetrievalConfigurationV1 struct {
	GlobalDefaults        RetrievalParamsV1             `json:"globalDefaults"`
	TenantOverride        retrieval.ParamsOverride      `json:"tenantOverride"`
	KnowledgeBaseOverride retrieval.ParamsOverride      `json:"knowledgeBaseOverride"`
	Effective             RetrievalParamsV1             `json:"effective"`
	Sources               map[string]string             `json:"sources"`
	Schema                []configschema.Field          `json:"schema"`
	Fields                map[string]configschema.State `json:"fields"`
}

type ReadOnlyEmbeddingV1 struct {
	Source    string `json:"source"`
	Model     string `json:"model"`
	Dimension int    `json:"dimension"`
}

type ReadOnlyChunkingV1 struct {
	Size         int    `json:"size"`
	Overlap      int    `json:"overlap"`
	SplitMarkers string `json:"splitMarkers"`
	MinSize      int    `json:"minSize"`
}

type ReadOnlyChatV1 struct {
	Source string `json:"source"`
	Model  string `json:"model"`
}

type ReadOnlyRerankV1 struct {
	Source string `json:"source"`
	Model  string `json:"model"`
}

type ReadOnlyParserV1 struct {
	Version          string   `json:"version"`
	SupportedFormats []string `json:"supportedFormats"`
}

type ReadOnlySystemConfigurationV1 struct {
	Embedding ReadOnlyEmbeddingV1 `json:"embedding"`
	Chunking  ReadOnlyChunkingV1  `json:"chunking"`
	Chat      ReadOnlyChatV1      `json:"chat"`
	Rerank    ReadOnlyRerankV1    `json:"rerank"`
	Parser    ReadOnlyParserV1    `json:"parser"`
}

type KnowledgeBaseConfigurationV1 struct {
	SchemaVersion    string                        `json:"schemaVersion"`
	KnowledgeBaseID  string                        `json:"knowledgeBaseId"`
	ACLVersion       int64                         `json:"aclVersion"`
	IndexingStrategy retrieval.IndexingStrategy    `json:"indexingStrategy"`
	Retrieval        RetrievalConfigurationV1      `json:"retrieval"`
	ModelBinding     ModelBindingV1                `json:"modelBinding"`
	ReadOnly         ReadOnlySystemConfigurationV1 `json:"readOnly"`
}

// ReadOnlySystemConfiguration projects the non-secret environment/YAML surface.
// It deliberately carries no model endpoints, credentials or object keys.
func ReadOnlySystemConfiguration(cfg config.Config) ReadOnlySystemConfigurationV1 {
	parserVersion := strings.TrimSpace(cfg.ParserVersion)
	if parserVersion == "" {
		parserVersion = config.DefaultParserVersion
	}
	return ReadOnlySystemConfigurationV1{
		Embedding: ReadOnlyEmbeddingV1{Source: cfg.Embedding.Source, Model: cfg.Embedding.Model, Dimension: cfg.Embedding.Dimension},
		Chunking:  ReadOnlyChunkingV1{Size: cfg.Chunk.Size, Overlap: cfg.Chunk.Overlap, SplitMarkers: strings.Join(cfg.Chunk.Markers, ","), MinSize: cfg.Chunk.MinSize},
		Chat:      ReadOnlyChatV1{Source: cfg.Chat.Source, Model: cfg.Chat.Model},
		Rerank:    ReadOnlyRerankV1{Source: cfg.Rerank.Source, Model: cfg.Rerank.Model},
		Parser:    ReadOnlyParserV1{Version: parserVersion, SupportedFormats: append([]string(nil), cfg.SupportedFormats...)},
	}
}

func BuildKnowledgeBaseConfiguration(knowledgeBase models.KnowledgeBase, defaults config.RetrievalConfig, readOnly ReadOnlySystemConfigurationV1) (KnowledgeBaseConfigurationV1, error) {
	tier := configschema.NewTier(config.RetrievalValues(defaults), map[string]bool{})
	return BuildKnowledgeBaseConfigurationWithTenant(knowledgeBase, defaults, "", tier, readOnly)
}

// BuildKnowledgeBaseConfigurationWithTenant resolves the full chain: schema
// defaults, deployment YAML, tenant override, knowledge-base override. The
// per-field state reports where each effective value came from.
func BuildKnowledgeBaseConfigurationWithTenant(knowledgeBase models.KnowledgeBase, defaults config.RetrievalConfig, tenantRaw string, deployment configschema.Tier, readOnly ReadOnlySystemConfigurationV1) (KnowledgeBaseConfigurationV1, error) {
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	override, err := retrieval.ParseOverride(knowledgeBase.RetrievalConfig)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	tenantOverride, err := retrieval.ParseOverride(tenantRaw)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	resolvedDefaults := retrievalParamsFromConfig(defaults)
	effective, err := retrieval.ResolveParamsWithTenant(resolvedDefaults, tenantRaw, knowledgeBase.RetrievalConfig, "")
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	tenantValues, err := rawObject(tenantRaw)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	knowledgeBaseValues, err := rawObject(knowledgeBase.RetrievalConfig)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	fields := configschema.Resolve(configschema.DomainRetrieval, deployment, tenantValues, knowledgeBaseValues, nil)
	sources := make(map[string]string, len(fields))
	for key, state := range fields {
		// The legacy map keeps its two-way meaning (inherited vs overridden) for
		// older clients; the per-field Fields map carries the precise tier.
		switch state.Source {
		case configschema.SourceKnowledgeBase:
			sources[key] = retrievalSourceKnowledgeBase
		case configschema.SourceTenant:
			sources[key] = "tenant"
		default:
			sources[key] = retrievalSourceGlobal
		}
	}
	// The read-only surface reports the knowledge-base binding when one exists;
	// empty means the deployment default already in readOnly.
	if knowledgeBase.ChatModel != "" {
		readOnly.Chat.Model = knowledgeBase.ChatModel
	}
	if knowledgeBase.RerankModel != "" {
		readOnly.Rerank.Model = knowledgeBase.RerankModel
	}
	binding := ModelBindingV1{
		Chat:               modelBindingEntry(knowledgeBase.ChatModel, knowledgeBase.ChatModelVersion, readOnly.Chat.Model),
		Rerank:             modelBindingEntry(knowledgeBase.RerankModel, knowledgeBase.RerankModelVersion, readOnly.Rerank.Model),
		Embedding:          modelBindingEntry(knowledgeBase.EmbeddingModel, "", readOnly.Embedding.Model),
		EmbeddingDimension: knowledgeBase.EmbeddingDimension,
	}
	if knowledgeBase.EmbeddingModel != "" {
		binding.EmbeddingLocked = true
		binding.EmbeddingLockReason = "vector contract is pinned to this knowledge base; changing it requires the managed migration"
	}
	return KnowledgeBaseConfigurationV1{
		SchemaVersion:    SchemaKnowledgeBaseConfigurationV1,
		KnowledgeBaseID:  knowledgeBase.ID,
		ACLVersion:       knowledgeBase.ACLVersion,
		IndexingStrategy: strategy,
		Retrieval: RetrievalConfigurationV1{
			GlobalDefaults:        retrievalParamsView(resolvedDefaults),
			TenantOverride:        tenantOverride,
			KnowledgeBaseOverride: override,
			Effective:             retrievalParamsView(effective),
			Sources:               sources,
			Schema:                configschema.FieldsFor(configschema.DomainRetrieval),
			Fields:                fields,
		},
		ModelBinding: binding,
		ReadOnly:     readOnly,
	}, nil
}

func rawObject(raw string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode retrieval override: %w", err)
	}
	return values, nil
}

func modelBindingEntry(boundModel, boundVersion, deploymentModel string) ModelBindingEntryV1 {
	if boundModel != "" {
		return ModelBindingEntryV1{Model: boundModel, Version: boundVersion, Source: modelSourceKnowledgeBase}
	}
	return ModelBindingEntryV1{Model: deploymentModel, Version: "", Source: modelSourceDeployment}
}

// retrievalParamsFromConfig keeps the config conversion in the knowledge-base
// configuration view, the one domain allowed to read the global config type.
func retrievalParamsFromConfig(cfg config.RetrievalConfig) retrieval.Params {
	return retrieval.Params{
		TopK: cfg.TopK, EmbeddingTopK: cfg.EmbeddingTopK, KeywordTopK: cfg.KeywordTopK,
		VectorThreshold: cfg.VectorThreshold, KeywordThreshold: cfg.KeywordThreshold,
		RerankEnabled: cfg.RerankEnabled, RerankTopK: cfg.RerankTopK, RerankThreshold: cfg.RerankThreshold,
		VectorEFSearch: cfg.VectorEFSearch, VectorOversample: cfg.VectorOversample,
		VectorIterativeScan: cfg.VectorIterativeScan,
		ContextTokenLimit:   cfg.ContextTokenLimit, AdjacentExpansion: cfg.AdjacentExpansion,
		MaxAdjacentChunks: cfg.MaxAdjacentChunks, RRFK: cfg.RRFK, ExpansionScoreFactor: cfg.ExpansionScoreFactor,
	}
}

func retrievalParamsView(params retrieval.Params) RetrievalParamsV1 {
	return RetrievalParamsV1{
		TopK:                 params.TopK,
		EmbeddingTopK:        params.EmbeddingTopK,
		KeywordTopK:          params.KeywordTopK,
		VectorThreshold:      params.VectorThreshold,
		KeywordThreshold:     params.KeywordThreshold,
		RerankEnabled:        params.RerankEnabled,
		RerankTopK:           params.RerankTopK,
		RerankThreshold:      params.RerankThreshold,
		VectorEFSearch:       params.VectorEFSearch,
		VectorOversample:     params.VectorOversample,
		VectorIterativeScan:  params.VectorIterativeScan,
		ContextTokenLimit:    params.ContextTokenLimit,
		AdjacentExpansion:    params.AdjacentExpansion,
		MaxAdjacentChunks:    params.MaxAdjacentChunks,
		RRFK:                 params.RRFK,
		ExpansionScoreFactor: params.ExpansionScoreFactor,
	}
}

func (s *Service) Configuration(ctx context.Context, principal authz.Principal, id string, defaults config.RetrievalConfig, readOnly ReadOnlySystemConfigurationV1) (KnowledgeBaseConfigurationV1, error) {
	tier := configschema.NewTier(config.RetrievalValues(defaults), map[string]bool{})
	return s.ConfigurationWithTenant(ctx, principal, id, defaults, tier, readOnly)
}

// ConfigurationWithTenant includes the tenant tier and the deployment tier's
// explicit keys in the per-field source report.
func (s *Service) ConfigurationWithTenant(ctx context.Context, principal authz.Principal, id string, defaults config.RetrievalConfig, deployment configschema.Tier, readOnly ReadOnlySystemConfigurationV1) (KnowledgeBaseConfigurationV1, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "read"); err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	var row models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return KnowledgeBaseConfigurationV1{}, apperror.ErrNotFound
		}
		return KnowledgeBaseConfigurationV1{}, fmt.Errorf("load knowledge base configuration: %w", err)
	}
	tenantRaw := ""
	if s.tenantParams != nil {
		raw, err := s.tenantParams.TenantRetrievalOverride(ctx, principal.TenantID)
		if err != nil {
			return KnowledgeBaseConfigurationV1{}, err
		}
		tenantRaw = raw
	}
	view, err := BuildKnowledgeBaseConfigurationWithTenant(row, defaults, tenantRaw, deployment, readOnly)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	// An empty knowledge base may adopt another registered model directly; once
	// documents exist the vector contract only moves through the migration.
	if view.ModelBinding.EmbeddingLocked {
		var documents int64
		if err := s.db.WithContext(ctx).Model(&models.Document{}).
			Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, id).
			Count(&documents).Error; err != nil {
			return KnowledgeBaseConfigurationV1{}, fmt.Errorf("count knowledge base documents: %w", err)
		}
		if documents == 0 {
			view.ModelBinding.EmbeddingLocked = false
			view.ModelBinding.EmbeddingLockReason = ""
		}
	}
	return view, nil
}

// ReplaceRetrievalConfig applies whole-replacement semantics for the
// knowledge-base tier. An empty override clears it and falls back to the global
// defaults. Retrieval parameters do not participate in ACL or projection
// invalidation, so neither acl_version nor derived projections change.
func (s *Service) ReplaceRetrievalConfig(ctx context.Context, principal authz.Principal, id string, override retrieval.ParamsOverride) (models.KnowledgeBase, error) {
	return s.writeRetrievalConfig(ctx, principal, id, override)
}

func (s *Service) writeRetrievalConfig(ctx context.Context, principal authz.Principal, id string, override retrieval.ParamsOverride) (models.KnowledgeBase, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "write"); err != nil {
		return models.KnowledgeBase{}, err
	}
	if err := retrieval.ValidateOverride(override); err != nil {
		return models.KnowledgeBase{}, err
	}
	var row models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.KnowledgeBase{}, apperror.ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("load knowledge base: %w", err)
	}
	encoded, err := json.Marshal(override)
	if err != nil {
		return models.KnowledgeBase{}, fmt.Errorf("encode retrieval config: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&row).Update("retrieval_config", string(encoded)).Error; err != nil {
		return models.KnowledgeBase{}, fmt.Errorf("replace retrieval config: %w", err)
	}
	row.RetrievalConfig = string(encoded)
	if err := s.recordRetrievalConfigAudit(ctx, principal, id, row.RetrievalConfig); err != nil {
		return models.KnowledgeBase{}, err
	}
	return row, nil
}
