import type { components } from "./generated";

// API DTOs come from the generated OpenAPI schema so a spec change breaks the
// TypeScript build (and `npm run codegen:check` catches the generated file).
// DeepRequired keeps the client's existing required-field ergonomics where the
// spec marks response fields optional.
type Schemas = components["schemas"];
type DeepRequired<T> = T extends (infer U)[]
  ? DeepRequired<U>[]
  : T extends object
    ? { [K in keyof T]-?: DeepRequired<T[K]> }
    : T;

export type Scope = DeepRequired<NonNullable<Schemas["MeResponse"]["scope"]>>;

export type MeResponse = {
  principal: DeepRequired<NonNullable<Schemas["MeResponse"]["principal"]>>;
  scope: Scope;
  developmentIdentity: boolean;
};

export type AuthConfig = Schemas["AuthConfig"];

export type KnowledgeBase = DeepRequired<Schemas["KnowledgeBase"]>;

export type Member = DeepRequired<Schemas["KnowledgeBaseMember"]>;

export type TenantMember = DeepRequired<Schemas["TenantMember"]>;

export type TenantMemberCounts = DeepRequired<Schemas["TenantMemberCounts"]>;

export type TenantOverview = DeepRequired<Schemas["TenantOverview"]>;

export type TenantQuotaField = DeepRequired<Schemas["TenantQuotaField"]>;

export type TenantQuota = DeepRequired<Schemas["TenantQuota"]>;

export type TenantQuotaUpdateRequest = Schemas["TenantQuotaUpdateRequest"];

export type AuditEvent = DeepRequired<Schemas["AuditEvent"]>;

export type ChunkView = DeepRequired<Schemas["Chunk"]>;

export type ChunkPage = DeepRequired<Schemas["ChunkPage"]>;

export type Conversation = DeepRequired<Schemas["Conversation"]>;

export type StreamEvent = Schemas["StreamEvent"];

export type APIKeyView = DeepRequired<Schemas["APIKey"]>;

export type APIKeyCreateRequest = Schemas["APIKeyCreateRequest"];

export type APIKeyCreateResponse = Schemas["APIKeyCreateResponse"];

export type Connector = DeepRequired<Schemas["Connector"]>;

export type ConnectorRun = DeepRequired<Schemas["ConnectorRun"]>;

// The schemas below are intentionally generic or inline in the OpenAPI spec
// (additionalProperties maps), so the typed view models stay hand-written.
export type IndexingStrategy = {
  vector_enabled: boolean;
  keyword_enabled: boolean;
  wiki_enabled: boolean;
  graph_enabled: boolean;
  required_capabilities: string[];
};

export type DocumentView = {
  document: { id: string; filename: string; status: string };
  version: { id: string; status: string; errorCode?: string; errorMessage?: string };
  pipeline: { status: string; errorCode?: string; errorMessage?: string };
};

type StageRun = {
  stage: string;
  status: string;
  attempts: number;
  errorCode?: string;
};

export type DocumentStatus = {
  document: { id: string; filename: string; mimeType: string; status: string };
  version: { id: string; status: string; errorCode?: string; errorMessage?: string };
  pipeline: { id: string; status: string; errorCode?: string };
  stages: StageRun[];
  contentReady: boolean;
  previewable: boolean;
};

export type DocumentSummary = {
  document: {
    id: string;
    filename: string;
    mimeType: string;
    status: string;
    latestVersionId: string;
    createdAt: string;
    updatedAt: string;
  };
  version: {
    id: string;
    version: number;
    sizeBytes: number;
    status: string;
    errorCode?: string;
    errorMessage?: string;
  };
  contentReady: boolean;
  previewable: boolean;
};

export type DocumentPage = {
  schemaVersion: string;
  knowledgeBaseId: string;
  items: DocumentSummary[];
  nextCursor: string;
  hasMore: boolean;
};

export type DocumentVersionView = {
  id: string;
  version: number;
  sizeBytes: number;
  status: string;
  errorCode?: string;
  errorMessage?: string;
  current: boolean;
  contentReady: boolean;
  restorable: boolean;
  createdAt: string;
};

export type DocumentVersionPage = {
  schemaVersion: string;
  documentId: string;
  currentVersionId: string;
  items: DocumentVersionView[];
};

type PreviewBlock = {
  blockId: string;
  kind: string;
  text: string;
  headingPath: string;
  pageNumber: number;
  sourcePath: string;
};

export type DocumentPreview = {
  documentId: string;
  versionId: string;
  filename: string;
  status: string;
  title: string;
  language: string;
  blocks: PreviewBlock[];
};

export type Citation = {
  index: number;
  chunkId: string;
  documentId: string;
  documentVersionId: string;
  headingPath: string;
  pageNumber: number;
  quote: string;
  rank: number;
  score: number;
};

export type MessageView = {
  message: {
    id: string;
    role: string;
    content: string;
    status: string;
    errorCode?: string;
    errorMessage?: string;
    createdAt: string;
  };
  citations: Citation[];
};

export type DeadLetter = {
  id: number;
  idempotencyKey: string;
  taskType: string;
  stage: string;
  attempts: number;
  lastError: string;
  status: string;
  createdAt: string;
};

export type OpsMetrics = {
  startedAt: string;
  counters: Record<string, number>;
  timings: Record<string, { count: number; sumMs: number; maxMs: number; avgMs?: number }>;
  queues?: Record<string, Record<string, number>>;
  queueError?: string;
};

export type RetrievalParamsV1 = {
  top_k: number;
  embedding_top_k: number;
  keyword_top_k: number;
  vector_threshold: number;
  keyword_threshold: number;
  rerank_enabled: boolean;
  rerank_top_k: number;
  rerank_threshold: number;
  vector_ef_search: number;
  vector_oversample: number;
  vector_iterative_scan: boolean;
  context_token_limit: number;
  adjacent_expansion: number;
  max_adjacent_chunks: number;
  rrf_k: number;
  expansion_score_factor: number;
};

export type RetrievalOverride = Partial<RetrievalParamsV1>;

type RetrievalConfigurationV1 = {
  globalDefaults: RetrievalParamsV1;
  knowledgeBaseOverride: RetrievalOverride;
  effective: RetrievalParamsV1;
  sources: Record<keyof RetrievalParamsV1, "global" | "knowledge_base">;
};

type ModelBindingEntryV1 = {
  model: string;
  version: string;
  source: "deployment" | "knowledge_base";
};

type ModelBindingV1 = {
  chat: ModelBindingEntryV1;
  rerank: ModelBindingEntryV1;
  embedding: ModelBindingEntryV1;
  embeddingDimension: number;
  embeddingLocked: boolean;
  embeddingLockReason?: string;
};

export type KnowledgeBaseConfigurationV1 = {
  schemaVersion: string;
  knowledgeBaseId: string;
  aclVersion: number;
  indexingStrategy: IndexingStrategy;
  retrieval: RetrievalConfigurationV1;
  modelBinding: ModelBindingV1;
  readOnly: {
    embedding: { source: string; model: string; dimension: number };
    chunking: { size: number; overlap: number; splitMarkers: string; minSize: number };
    chat: { source: string; model: string };
    rerank: { source: string; model: string };
    parser: { version: string; supportedFormats: string[] };
  };
};

// B13 model catalog DTOs, mirroring the OpenAPI schemas. These are hand-written
// like the other response types here; `npm run codegen:check` only guards
// generated.ts, so update both when a contract changes.
export type ModelCatalogEntry = {
  id: string;
  providerId: string;
  provider: string;
  model: string;
  displayName: string;
  capability: string;
  version: string;
  dimension: number;
  source: string;
  enabled: boolean;
};

export type ModelCatalog = { items: ModelCatalogEntry[] };

export type ModelProvider = {
  id: string;
  name: string;
  kind: string;
  baseUrl: string;
  apiKeySet: boolean;
  healthUrl: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  models: ModelCatalogEntry[];
};

export type ModelProviderList = { items: ModelProvider[] };

export type ModelProviderUpsertRequest = {
  name: string;
  kind: "openai_compatible" | "ollama";
  base_url: string;
  api_key?: string;
  health_url?: string;
  enabled?: boolean;
  models: Array<{
    model: string;
    display_name?: string;
    capability: "chat" | "embedding" | "rerank";
    version?: string;
    dimension?: number;
    enabled?: boolean;
  }>;
};

export type ModelProviderTestResult = { healthy: boolean; error?: string; checkedAt: string };

type PageCounts = {
  total: number;
  published: number;
  pendingReview: number;
  stale: number;
  indexed: number;
};

type GraphCounts = {
  snapshots: number;
  activeSnapshot?: string;
  entities: number;
  relations: number;
  pendingReview: number;
};

export type ProjectionRun = {
  id: string;
  projectionType: string;
  status: string;
  model?: string;
  promptVersion?: string;
  sourceHash?: string;
  aclVersion?: number;
  errorCode?: string;
  errorMessage?: string;
  attempts?: number;
  createdAt: string;
  updatedAt: string;
};

export type ProjectionStatus = {
  knowledgeBaseId: string;
  joinStatus: string;
  wikiEnabled: boolean;
  graphEnabled: boolean;
  runs: ProjectionRun[];
  relationRuns: ProjectionRun[];
  pages: PageCounts;
  graph: GraphCounts;
};
