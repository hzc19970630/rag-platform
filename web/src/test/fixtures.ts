import type {
  IndexingStrategy,
  KnowledgeBaseConfigurationV1,
  MeResponse,
  RetrievalParamsV1,
} from "../api/types";

export function meWith(capabilities: string[]): MeResponse {
  return {
    principal: { subjectId: "user-1", tenantId: "tenant-1" },
    scope: {
      subjectId: "user-1",
      tenantId: "tenant-1",
      knowledgeBaseIds: ["kb-1"],
      capabilities,
      aclVersion: "1",
    },
    developmentIdentity: true,
  };
}

const params: RetrievalParamsV1 = {
  top_k: 10,
  embedding_top_k: 20,
  keyword_top_k: 20,
  vector_threshold: 0.2,
  keyword_threshold: 0,
  rerank_enabled: false,
  rerank_top_k: 10,
  rerank_threshold: 0,
  context_token_limit: 4000,
  adjacent_expansion: 1,
  max_adjacent_chunks: 4,
  rrf_k: 60,
  expansion_score_factor: 0.5,
};

const sources = Object.fromEntries(
  Object.keys(params).map((key) => [key, "global"]),
) as KnowledgeBaseConfigurationV1["retrieval"]["sources"];

export function strategyFixture(patch: Partial<IndexingStrategy> = {}): IndexingStrategy {
  return {
    vector_enabled: true,
    keyword_enabled: true,
    wiki_enabled: false,
    graph_enabled: false,
    required_capabilities: ["evidence"],
    ...patch,
  };
}

export function configurationFixture(): KnowledgeBaseConfigurationV1 {
  return {
    schemaVersion: "v1",
    knowledgeBaseId: "kb-1",
    aclVersion: 2,
    indexingStrategy: strategyFixture(),
    retrieval: {
      globalDefaults: params,
      knowledgeBaseOverride: {},
      effective: params,
      sources,
    },
    readOnly: {
      embedding: { source: "env", model: "bge-m3", dimension: 1024 },
      chunking: { size: 800, overlap: 80, splitMarkers: "\\n## ", minSize: 100 },
      chat: { source: "yaml", model: "qwen-plus" },
      rerank: { source: "env", model: "bge-reranker-v2" },
      parser: { version: "docreader-m2-v2", supportedFormats: ["pdf", "txt"] },
    },
  };
}