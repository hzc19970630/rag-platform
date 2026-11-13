import type {
  ConfigDomain,
  ConfigFieldDefinition,
  ConfigFieldState,
  IndexingStrategy,
  KnowledgeBaseConfigurationV1,
  MeResponse,
  RetrievalParamsV1,
} from "../api/types";

export function meWith(capabilities: string[]): MeResponse {
  return {
    principal: {
      subjectId: "user-1",
      tenantId: "tenant-1",
      capabilities,
      credentialType: "session",
      credentialId: "credential-1",
    },
    scope: {
      subjectId: "user-1",
      tenantId: "tenant-1",
      knowledgeBaseIds: ["kb-1"],
      resourceIds: [],
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
  vector_ef_search: 40,
  vector_oversample: 1,
  vector_iterative_scan: true,
  context_token_limit: 4000,
  adjacent_expansion: 1,
  max_adjacent_chunks: 4,
  rrf_k: 60,
  expansion_score_factor: 0.5,
};

const sources = Object.fromEntries(
  Object.keys(params).map((key) => [key, "global"]),
) as KnowledgeBaseConfigurationV1["retrieval"]["sources"];

const numeric: Array<[keyof RetrievalParamsV1, number, number | undefined, number | undefined, number]> = [
  ["top_k", 8, 0, 200, 1],
  ["embedding_top_k", 20, 0, 200, 1],
  ["keyword_top_k", 20, 0, 200, 1],
  ["vector_threshold", 0.2, 0, 1, 0.01],
  ["keyword_threshold", 0, 0, undefined, 0.01],
  ["rerank_top_k", 8, 0, 200, 1],
  ["rerank_threshold", -10, -10, 10, 0.01],
  ["vector_ef_search", 0, 0, 1000, 1],
  ["vector_oversample", 1, 1, 20, 1],
  ["context_token_limit", 3000, 1, undefined, 1],
  ["adjacent_expansion", 1, 0, undefined, 1],
  ["max_adjacent_chunks", 2, 0, undefined, 1],
  ["rrf_k", 60, 1, 200, 1],
  ["expansion_score_factor", 0.5, 0, 1, 0.05],
];

export function retrievalSchemaFixture(): ConfigFieldDefinition[] {
  return [
    ...numeric.map(([key, value, min, max, step]) => ({
      key,
      domain: "retrieval",
      group: key === "rrf_k" || key === "top_k" ? "fusion" : "recall",
      kind: Number.isInteger(value) ? "int" as const : "float" as const,
      default: value,
      min,
      max,
      step,
      applyMode: "next_retrieval",
      impact: "hot",
      rebuildScope: "none",
      risk: "normal",
      sensitivity: "normal",
      labelKey: `retrieval.${key}`,
    })),
    {
      key: "rerank_enabled",
      domain: "retrieval",
      group: "rerank",
      kind: "bool" as const,
      default: false,
      applyMode: "next_retrieval",
      impact: "hot",
      rebuildScope: "none",
      risk: "normal",
      sensitivity: "normal",
      labelKey: "retrieval.rerank_enabled",
    },
    {
      key: "vector_iterative_scan",
      domain: "retrieval",
      group: "recall",
      kind: "bool" as const,
      default: true,
      applyMode: "next_retrieval",
      impact: "hot",
      rebuildScope: "none",
      risk: "normal",
      sensitivity: "normal",
      labelKey: "retrieval.vector_iterative_scan",
    },
  ];
}

export function configDomainFixture(): ConfigDomain {
  const schema = retrievalSchemaFixture();
  const fields: Record<string, ConfigFieldState> = {};
  for (const field of schema) {
    fields[field.key] = { value: field.default, default: field.default, source: "default", applyMode: "next_retrieval" };
  }
  return { schema, deploymentDefaults: {}, tenantOverride: {}, effective: params, fields };
}

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
      tenantOverride: {},
      knowledgeBaseOverride: {},
      effective: params,
      sources,
      schema: retrievalSchemaFixture(),
      fields: configDomainFixture().fields,
    },
    modelBinding: {
      chat: { model: "qwen-plus", version: "", source: "deployment" },
      rerank: { model: "bge-reranker-v2", version: "", source: "deployment" },
      embedding: { model: "bge-m3", version: "", source: "knowledge_base" },
      embeddingDimension: 1024,
      embeddingLocked: true,
      embeddingLockReason: "vector contract is pinned",
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