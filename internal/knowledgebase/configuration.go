package knowledgebase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

const SchemaKnowledgeBaseConfigurationV1 = "KnowledgeBaseConfigurationV1"

const (
	retrievalSourceGlobal        = "global"
	retrievalSourceKnowledgeBase = "knowledge_base"
)

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
	ContextTokenLimit    int     `json:"context_token_limit"`
	AdjacentExpansion    int     `json:"adjacent_expansion"`
	MaxAdjacentChunks    int     `json:"max_adjacent_chunks"`
	RRFK                 int     `json:"rrf_k"`
	ExpansionScoreFactor float64 `json:"expansion_score_factor"`
}

type RetrievalConfigurationV1 struct {
	GlobalDefaults        RetrievalParamsV1        `json:"globalDefaults"`
	KnowledgeBaseOverride retrieval.ParamsOverride `json:"knowledgeBaseOverride"`
	Effective             RetrievalParamsV1        `json:"effective"`
	Sources               map[string]string        `json:"sources"`
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
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	override, err := retrieval.ParseOverride(knowledgeBase.RetrievalConfig)
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	effective, err := retrieval.ResolveParams(defaults, knowledgeBase.RetrievalConfig, "")
	if err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	return KnowledgeBaseConfigurationV1{
		SchemaVersion:    SchemaKnowledgeBaseConfigurationV1,
		KnowledgeBaseID:  knowledgeBase.ID,
		ACLVersion:       knowledgeBase.ACLVersion,
		IndexingStrategy: strategy,
		Retrieval: RetrievalConfigurationV1{
			GlobalDefaults:        retrievalParamsView(defaults),
			KnowledgeBaseOverride: override,
			Effective:             retrievalParamsView(effective),
			Sources:               retrievalSources(override),
		},
		ReadOnly: readOnly,
	}, nil
}

func retrievalParamsView(params config.RetrievalConfig) RetrievalParamsV1 {
	return RetrievalParamsV1{
		TopK:                 params.TopK,
		EmbeddingTopK:        params.EmbeddingTopK,
		KeywordTopK:          params.KeywordTopK,
		VectorThreshold:      params.VectorThreshold,
		KeywordThreshold:     params.KeywordThreshold,
		RerankEnabled:        params.RerankEnabled,
		RerankTopK:           params.RerankTopK,
		RerankThreshold:      params.RerankThreshold,
		ContextTokenLimit:    params.ContextTokenLimit,
		AdjacentExpansion:    params.AdjacentExpansion,
		MaxAdjacentChunks:    params.MaxAdjacentChunks,
		RRFK:                 params.RRFK,
		ExpansionScoreFactor: params.ExpansionScoreFactor,
	}
}

func retrievalSources(override retrieval.ParamsOverride) map[string]string {
	sources := map[string]string{}
	set := func(name string, overridden bool) {
		if overridden {
			sources[name] = retrievalSourceKnowledgeBase
			return
		}
		sources[name] = retrievalSourceGlobal
	}
	set("top_k", override.TopK != nil)
	set("embedding_top_k", override.EmbeddingTopK != nil)
	set("keyword_top_k", override.KeywordTopK != nil)
	set("vector_threshold", override.VectorThreshold != nil)
	set("keyword_threshold", override.KeywordThreshold != nil)
	set("rerank_enabled", override.RerankEnabled != nil)
	set("rerank_top_k", override.RerankTopK != nil)
	set("rerank_threshold", override.RerankThreshold != nil)
	set("context_token_limit", override.ContextTokenLimit != nil)
	set("adjacent_expansion", override.AdjacentExpansion != nil)
	set("max_adjacent_chunks", override.MaxAdjacentChunks != nil)
	set("rrf_k", override.RRFK != nil)
	set("expansion_score_factor", override.ExpansionScoreFactor != nil)
	return sources
}

func (s *Service) Configuration(ctx context.Context, principal authz.Principal, id string, defaults config.RetrievalConfig, readOnly ReadOnlySystemConfigurationV1) (KnowledgeBaseConfigurationV1, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "read"); err != nil {
		return KnowledgeBaseConfigurationV1{}, err
	}
	var row models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return KnowledgeBaseConfigurationV1{}, apperror.ErrNotFound
		}
		return KnowledgeBaseConfigurationV1{}, fmt.Errorf("load knowledge base configuration: %w", err)
	}
	return BuildKnowledgeBaseConfiguration(row, defaults, readOnly)
}

// ReplaceRetrievalConfig applies whole-replacement semantics for the
// knowledge-base tier. An empty override clears it and falls back to the global
// defaults. Retrieval parameters do not participate in ACL or projection
// invalidation, so neither acl_version nor derived projections change.
func (s *Service) ReplaceRetrievalConfig(ctx context.Context, principal authz.Principal, id string, override retrieval.ParamsOverride) (models.KnowledgeBase, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "write"); err != nil {
		return models.KnowledgeBase{}, err
	}
	if err := retrieval.ValidateOverride(override); err != nil {
		return models.KnowledgeBase{}, err
	}
	var row models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&row).Error; err != nil {
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
