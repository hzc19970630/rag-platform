export type Scope = {
  subjectId: string;
  tenantId: string;
  knowledgeBaseIds: string[];
  capabilities: string[];
  aclVersion: string;
};

export type MeResponse = {
  principal: { subjectId: string; tenantId: string };
  scope: Scope;
  developmentIdentity: boolean;
};

export type AuthConfig = {
  oidcEnabled: boolean;
  oidcIssuer: string;
  devAuthEnabled: boolean;
  sessionCookie: string;
  sessionTTLHours: number;
};

export type IndexingStrategy = {
  vector_enabled: boolean;
  keyword_enabled: boolean;
  wiki_enabled: boolean;
  graph_enabled: boolean;
  required_capabilities: string[];
};

export type KnowledgeBase = {
  id: string;
  tenantId: string;
  ownerId: string;
  name: string;
  description: string;
  aclVersion: number;
  retrievalConfig: string;
  indexingStrategy: string;
  createdAt: string;
  updatedAt: string;
};

export type Member = {
  knowledgeBaseId: string;
  userId: string;
  role: string;
  createdAt: string;
  updatedAt: string;
};

export type DocumentView = {
  document: { id: string; filename: string; status: string };
  version: { id: string; status: string; errorCode?: string; errorMessage?: string };
  pipeline: { status: string; errorCode?: string; errorMessage?: string };
};

export type StageRun = {
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

export type PreviewBlock = {
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

export type ChunkView = {
  chunkId: string;
  documentId: string;
  documentVersionId: string;
  knowledgeBaseId: string;
  parentId?: string;
  ordinal: number;
  content: string;
  headingPath: string;
  pageNumber: number;
  sourcePath: string;
  status: string;
};

export type ChunkPage = {
  items: ChunkView[];
  nextCursor: number;
  hasMore: boolean;
  status: string;
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

export type Conversation = {
  id: string;
  knowledgeBaseId: string;
  userId: string;
  title: string;
  createdAt: string;
  updatedAt: string;
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

export type StreamEvent = {
  event_id: string;
  request_id: string;
  sequence: number;
  type: string;
  data?: Record<string, unknown>;
};

export type APIKeyView = {
  id: string;
  name: string;
  keyPrefix: string;
  capabilities: string[];
  knowledgeBaseIds: string[];
  expiresAt?: string;
  revokedAt?: string;
  lastUsedAt?: string;
  createdAt: string;
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
  context_token_limit: number;
  adjacent_expansion: number;
  max_adjacent_chunks: number;
  rrf_k: number;
  expansion_score_factor: number;
};

export type RetrievalOverride = Partial<RetrievalParamsV1>;

export type RetrievalConfigurationV1 = {
  globalDefaults: RetrievalParamsV1;
  knowledgeBaseOverride: RetrievalOverride;
  effective: RetrievalParamsV1;
  sources: Record<keyof RetrievalParamsV1, "global" | "knowledge_base">;
};

export type KnowledgeBaseConfigurationV1 = {
  schemaVersion: string;
  knowledgeBaseId: string;
  aclVersion: number;
  indexingStrategy: IndexingStrategy;
  retrieval: RetrievalConfigurationV1;
  readOnly: {
    embedding: { source: string; model: string; dimension: number };
    chunking: { size: number; overlap: number; splitMarkers: string; minSize: number };
    chat: { source: string; model: string };
    rerank: { source: string; model: string };
    parser: { version: string; supportedFormats: string[] };
  };
};

export type PageCounts = {
  total: number;
  published: number;
  pendingReview: number;
  stale: number;
  indexed: number;
};

export type GraphCounts = {
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

export type Connector = {
  id: string;
  tenantId: string;
  knowledgeBaseId: string;
  name: string;
  type: string;
  config: {
    manifestUrl: string;
    requestsPerSecond?: number;
    maxItemsPerRun: number;
    aclMode: string;
  };
  enabled: boolean;
  syncIntervalSeconds: number;
  nextSyncAt?: string;
  lastSyncAt?: string;
  hasCredential: boolean;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
};

export type ConnectorRun = {
  id: string;
  connectorId: string;
  knowledgeBaseId: string;
  mode: string;
  status: string;
  triggeredBy: string;
  itemsTotal: number;
  itemsCreated: number;
  itemsUpdated: number;
  itemsDeleted: number;
  itemsSkipped: number;
  itemsFailed: number;
  errorSummary?: string;
  attempts: number;
  startedAt?: string;
  finishedAt?: string;
  createdAt: string;
};
