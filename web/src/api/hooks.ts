import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type {
  APIKeyView,
  AuditEvent,
  AuthConfig,
  ChunkPage,
  ChunkView,
  Connector,
  ConnectorRun,
  Conversation,
  DeadLetter,
  DocumentPage,
  DocumentPreview,
  DocumentStatus,
  DocumentVersionPage,
  KnowledgeBase,
  KnowledgeBaseConfigurationV1,
  MeResponse,
  Member,
  MessageView,
  ModelCatalog,
  ModelProviderList,
  OpsMetrics,
  ProjectionStatus,
  TenantMember,
  TenantOverview,
  TenantQuota,
  TenantConfiguration,
  ConfigHealth,
} from "./types";

export const queryKeys = {
  authConfig: ["auth", "config"] as const,
  me: ["auth", "me"] as const,
  knowledgeBases: ["knowledge-bases"] as const,
  knowledgeBase: (id: string) => ["knowledge-bases", id] as const,
  documents: (id: string) => ["knowledge-bases", id, "documents"] as const,
  members: (id: string) => ["knowledge-bases", id, "members"] as const,
  configuration: (id: string) => ["knowledge-bases", id, "configuration"] as const,
  projection: (id: string) => ["knowledge-bases", id, "projection"] as const,
  connectors: (id: string) => ["knowledge-bases", id, "connectors"] as const,
  connectorRuns: (id: string) => ["connectors", id, "runs"] as const,
  documentStatus: (id: string) => ["documents", id, "status"] as const,
  documentVersions: (id: string) => ["documents", id, "versions"] as const,
  documentContent: (id: string) => ["documents", id, "content"] as const,
  chunks: (id: string) => ["documents", id, "chunks"] as const,
  chunk: (id: string) => ["chunks", id] as const,
  conversations: ["conversations"] as const,
  messages: (id: string) => ["conversations", id, "messages"] as const,
  apiKeys: ["api-keys"] as const,
  deadLetters: ["ops", "dead-letters"] as const,
  metrics: ["ops", "metrics"] as const,
  tenantOverview: ["tenant", "overview"] as const,
  tenantQuota: ["tenant", "quota"] as const,
  tenantMembers: ["tenant", "members"] as const,
  tenantConfig: ["tenant", "config"] as const,
  configHealth: ["tenant", "config", "health"] as const,
  modelCatalog: ["models", "catalog"] as const,
  modelProviders: ["models", "providers"] as const,
  audit: (filters: AuditFilters) => ["audit", filters] as const,
};

export type AuditFilters = {
  actorId?: string;
  action?: string;
  resourceType?: string;
  allowed?: "true" | "false";
  limit?: number;
};

export const terminalVersionStatuses = new Set(["EVIDENCE_READY", "FAILED", "CANCELLED"]);

export function useAuthConfig() {
  return useQuery({ queryKey: queryKeys.authConfig, queryFn: () => api<AuthConfig>("/auth/config"), staleTime: 60_000 });
}

export function useMe() {
  return useQuery({ queryKey: queryKeys.me, queryFn: () => api<MeResponse>("/me"), retry: false });
}

export function useKnowledgeBases() {
  return useQuery({
    queryKey: queryKeys.knowledgeBases,
    queryFn: () => api<{ items: KnowledgeBase[] }>("/knowledge-bases"),
    select: (data) => data.items,
  });
}

export function useKnowledgeBase(id: string | undefined) {
  return useQuery({
    queryKey: queryKeys.knowledgeBase(id ?? ""),
    queryFn: () => api<KnowledgeBase>(`/knowledge-bases/${id}`),
    enabled: Boolean(id),
  });
}

export function useMembers(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.members(id),
    queryFn: () => api<{ items: Member[] }>(`/knowledge-bases/${id}/members`),
    select: (data) => data.items,
    enabled,
  });
}

export function useConfiguration(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.configuration(id),
    queryFn: () => api<KnowledgeBaseConfigurationV1>(`/knowledge-bases/${id}/configuration`),
    enabled,
  });
}

export function useProjectionStatus(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.projection(id),
    queryFn: () => api<ProjectionStatus>(`/knowledge-bases/${id}/projection`),
    enabled,
    refetchInterval: (query) => {
      const status = query.state.data;
      if (!status) return false;
      const running = [...status.runs, ...status.relationRuns].some((run) => run.status.includes("RUNNING") || run.status.includes("PENDING") || run.status.includes("BUILDING"));
      return running ? 2500 : false;
    },
  });
}

export function useConnectors(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.connectors(id),
    queryFn: () => api<{ connectors: Connector[] }>(`/knowledge-bases/${id}/connectors`),
    select: (data) => data.connectors,
    enabled,
  });
}

export function useConnectorRuns(id: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.connectorRuns(id),
    queryFn: () => api<{ runs: ConnectorRun[] }>(`/connectors/${id}/runs?limit=10`),
    select: (data) => data.runs,
    enabled,
  });
}

export function useDocuments(knowledgeBaseId: string) {
  return useInfiniteQuery({
    queryKey: queryKeys.documents(knowledgeBaseId),
    queryFn: ({ pageParam }) =>
      api<DocumentPage>(`/knowledge-bases/${knowledgeBaseId}/documents?limit=50${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ""}`),
    initialPageParam: "",
    getNextPageParam: (last) => (last.hasMore ? last.nextCursor : undefined),
    refetchInterval: (query) => {
      const items = query.state.data?.pages.flatMap((page) => page.items) ?? [];
      return items.some((item) => !terminalVersionStatuses.has(item.version.status)) ? 1500 : false;
    },
  });
}

export function useDocumentStatus(documentId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.documentStatus(documentId ?? ""),
    queryFn: () => api<DocumentStatus>(`/documents/${documentId}/status`),
    enabled: Boolean(documentId),
    refetchInterval: (query) => {
      const status = query.state.data;
      if (!status || status.contentReady || status.version.status === "FAILED" || status.version.status === "CANCELLED") return false;
      return 1500;
    },
  });
}

export function useDocumentVersions(documentId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.documentVersions(documentId ?? ""),
    queryFn: () => api<DocumentVersionPage>(`/documents/${documentId}/versions`),
    enabled: Boolean(documentId),
  });
}

export function useDocumentContent(documentId: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.documentContent(documentId ?? ""),
    queryFn: () => api<DocumentPreview>(`/documents/${documentId}/content`),
    enabled: Boolean(documentId) && enabled,
  });
}

export function useChunks(documentId: string | undefined, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: queryKeys.chunks(documentId ?? ""),
    queryFn: ({ pageParam }) => api<ChunkPage>(`/documents/${documentId}/chunks?limit=50${pageParam > 0 ? `&cursor=${pageParam}` : ""}`),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.hasMore ? last.nextCursor : undefined),
    enabled: Boolean(documentId) && enabled,
  });
}

export function useChunk(chunkId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.chunk(chunkId ?? ""),
    queryFn: () => api<ChunkView>(`/chunks/${chunkId}`),
    enabled: Boolean(chunkId),
  });
}

export function useConversations() {
  return useQuery({
    queryKey: queryKeys.conversations,
    queryFn: () => api<{ items: Conversation[] }>("/sessions"),
    select: (data) => data.items,
  });
}

export function useMessages(conversationId: string | undefined) {
  return useQuery({
    queryKey: queryKeys.messages(conversationId ?? ""),
    queryFn: () => api<{ items: MessageView[] }>(`/sessions/${conversationId}/messages`),
    select: (data) => data.items,
    enabled: Boolean(conversationId),
  });
}

export function useAPIKeys() {
  return useQuery({
    queryKey: queryKeys.apiKeys,
    queryFn: () => api<{ items: APIKeyView[] }>("/api-keys"),
    select: (data) => data.items,
  });
}

export function useDeadLetters() {
  return useQuery({
    queryKey: queryKeys.deadLetters,
    queryFn: () => api<{ items: DeadLetter[] }>("/ops/dead-letters?limit=20"),
    select: (data) => data.items,
  });
}

export function useOpsMetrics() {
  return useQuery({ queryKey: queryKeys.metrics, queryFn: () => api<OpsMetrics>("/ops/metrics") });
}

export function useTenantOverview(enabled = true) {
  return useQuery({
    queryKey: queryKeys.tenantOverview,
    queryFn: () => api<TenantOverview>("/tenant"),
    enabled,
  });
}

export function useTenantQuota(enabled = true) {
  return useQuery({
    queryKey: queryKeys.tenantQuota,
    queryFn: () => api<TenantQuota>("/tenant/quota"),
    enabled,
  });
}

export function useTenantConfiguration(enabled = true) {
  return useQuery({
    queryKey: queryKeys.tenantConfig,
    queryFn: () => api<TenantConfiguration>("/tenant/config"),
    enabled,
  });
}

// The health endpoint probes model endpoints, so it is fetched on demand and
// refreshed explicitly rather than on a short polling interval.
export function useConfigHealth(enabled = true) {
  return useQuery({
    queryKey: queryKeys.configHealth,
    queryFn: () => api<ConfigHealth>("/tenant/config/health"),
    enabled,
    staleTime: 0,
  });
}

export function useTenantMembers(enabled = true) {
  return useQuery({
    queryKey: queryKeys.tenantMembers,
    queryFn: () => api<{ items: TenantMember[] }>("/tenant/members"),
    select: (data) => data.items,
    enabled,
  });
}

export function useAuditEvents(filters: AuditFilters, enabled = true) {
  return useQuery({
    queryKey: queryKeys.audit(filters),
    queryFn: () => {
      const params = new URLSearchParams();
      if (filters.actorId) params.set("actorId", filters.actorId);
      if (filters.action) params.set("action", filters.action);
      if (filters.resourceType) params.set("resourceType", filters.resourceType);
      if (filters.allowed) params.set("allowed", filters.allowed);
      params.set("limit", String(filters.limit ?? 100));
      return api<{ items: AuditEvent[] }>(`/audit?${params.toString()}`);
    },
    select: (data) => data.items,
    enabled,
  });
}

// B13 model catalog: the merged view feeds knowledge-base settings, the
// provider list feeds the tenant admin surface.
export function useModelCatalog(enabled = true) {
  return useQuery({
    queryKey: queryKeys.modelCatalog,
    queryFn: () => api<ModelCatalog>("/models/catalog"),
    select: (data) => data.items,
    enabled,
  });
}

export function useModelProviders(enabled = true) {
  return useQuery({
    queryKey: queryKeys.modelProviders,
    queryFn: () => api<ModelProviderList>("/models/providers"),
    select: (data) => data.items,
    enabled,
  });
}
