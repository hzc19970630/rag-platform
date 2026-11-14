import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  queryKeys,
  useAPIKeys,
  useAuditEvents,
  useAuthConfig,
  useChunk,
  useChunks,
  useConfigHealth,
  useConfiguration,
  useConnectorRuns,
  useConnectors,
  useConversations,
  useDeadLetters,
  useDocuments,
  useDocumentContent,
  useDocumentStatus,
  useDocumentVersions,
  useKnowledgeBase,
  useKnowledgeBases,
  useMe,
  useMembers,
  useMessages,
  useModelCatalog,
  useModelProviders,
  useOpsMetrics,
  useProjectionStatus,
  useTenantConfiguration,
  useTenantMembers,
  useTenantOverview,
  useTenantQuota,
} from "./hooks";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function stubFetch(handler: (url: string) => unknown) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const body = handler(String(input));
    return new Response(JSON.stringify(body ?? {}), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("queryKeys", () => {
  it("keeps resource keys stable and scoped", () => {
    expect(queryKeys.knowledgeBase("kb-1")).toEqual(["knowledge-bases", "kb-1"]);
    expect(queryKeys.documents("kb-1")).toEqual(["knowledge-bases", "kb-1", "documents"]);
    expect(queryKeys.audit({ action: "write" })).toEqual(["audit", { action: "write" }]);
    expect(queryKeys.messages("conv-1")).toEqual(["conversations", "conv-1", "messages"]);
  });
});

describe("account and knowledge base queries", () => {
  it("loads auth config and the current session", async () => {
    stubFetch((url) => {
      if (url.endsWith("/auth/config")) return { oidcEnabled: true, ldapEnabled: false, devAuthEnabled: false };
      if (url.endsWith("/me")) return { principal: { subjectId: "u1" }, scope: { knowledgeBaseIds: ["kb-1"] } };
      return {};
    });
    const auth = renderHook(() => useAuthConfig(), { wrapper });
    const me = renderHook(() => useMe(), { wrapper });
    await waitFor(() => expect(auth.result.current.data?.oidcEnabled).toBe(true));
    await waitFor(() => expect(me.result.current.data?.principal.subjectId).toBe("u1"));
  });

  it("selects list payloads and respects scoped ids", async () => {
    stubFetch((url) => {
      if (url.endsWith("/knowledge-bases")) return { items: [{ id: "kb-1" }, { id: "kb-2" }] };
      if (url.endsWith("/knowledge-bases/kb-1")) return { id: "kb-1", name: "KB" };
      if (url.endsWith("/members")) return { items: [{ userId: "u1" }] };
      if (url.endsWith("/configuration")) return { retrieval: {} };
      if (url.endsWith("/projection")) return { runs: [], relationRuns: [], pages: {}, graph: {} };
      if (url.endsWith("/connectors")) return { connectors: [{ id: "conn-1" }] };
      if (url.includes("/connectors/conn-1/runs")) return { runs: [{ id: "run-1" }] };
      return {};
    });

    const list = renderHook(() => useKnowledgeBases(), { wrapper });
    const single = renderHook(() => useKnowledgeBase("kb-1"), { wrapper });
    const members = renderHook(() => useMembers("kb-1"), { wrapper });
    const configuration = renderHook(() => useConfiguration("kb-1"), { wrapper });
    const projection = renderHook(() => useProjectionStatus("kb-1"), { wrapper });
    const connectors = renderHook(() => useConnectors("kb-1"), { wrapper });
    const runs = renderHook(() => useConnectorRuns("conn-1"), { wrapper });

    await waitFor(() => expect(list.result.current.data).toHaveLength(2));
    await waitFor(() => expect(single.result.current.data?.id).toBe("kb-1"));
    await waitFor(() => expect(members.result.current.data).toHaveLength(1));
    await waitFor(() => expect(configuration.result.current.data).toBeTruthy());
    await waitFor(() => expect(projection.result.current.data?.runs).toEqual([]));
    await waitFor(() => expect(connectors.result.current.data).toHaveLength(1));
    await waitFor(() => expect(runs.result.current.data).toHaveLength(1));
  });
});

describe("document queries", () => {
  it("loads status, versions, content, chunks and a single chunk", async () => {
    stubFetch((url) => {
      if (url.includes("/documents/doc-1/status")) return { contentReady: true, version: { status: "EVIDENCE_READY" } };
      if (url.includes("/documents/doc-1/versions")) return { items: [{ id: "ver-1" }] };
      if (url.includes("/documents/doc-1/content")) return { documentId: "doc-1", versionId: "ver-1", filename: "doc.md", status: "READY", title: "Doc", language: "en", blocks: [] };
      if (url.includes("/documents/doc-1/chunks")) return { items: [{ id: "chunk-1" }], hasMore: false };
      if (url.includes("/chunks/chunk-1")) return { chunkId: "chunk-1", content: "chunk", contentHash: "h", documentId: "doc-1", documentVersionId: "ver-1", endOffset: 1, headingPath: "", knowledgeBaseId: "kb-1", ordinal: 0, pageNumber: 1, parentId: "p", sourcePath: "a.md", startOffset: 0, status: "EVIDENCE_READY" };
      if (url.includes("/documents?limit=50")) return { items: [], hasMore: false };
      return {};
    });

    const documents = renderHook(() => useDocuments("kb-1"), { wrapper });
    const status = renderHook(() => useDocumentStatus("doc-1"), { wrapper });
    const versions = renderHook(() => useDocumentVersions("doc-1"), { wrapper });
    const content = renderHook(() => useDocumentContent("doc-1", true), { wrapper });
    const chunks = renderHook(() => useChunks("doc-1", true), { wrapper });
    const chunk = renderHook(() => useChunk("chunk-1"), { wrapper });

    await waitFor(() => expect(documents.result.current.data?.pages).toHaveLength(1));
    await waitFor(() => expect(status.result.current.data?.contentReady).toBe(true));
    await waitFor(() => expect(versions.result.current.data?.items).toHaveLength(1));
    await waitFor(() => expect(content.result.current.data?.documentId).toBe("doc-1"));
    await waitFor(() => expect(chunks.result.current.data?.pages[0].items).toHaveLength(1));
    await waitFor(() => expect(chunk.result.current.data?.chunkId).toBe("chunk-1"));
  });

  it("walks document and chunk cursors", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("cursor=doc-cursor")) return { items: [{ id: "doc-2", version: { status: "EVIDENCE_READY" } }], hasMore: false };
      if (url.includes("/documents?limit=50")) return { items: [{ id: "doc-1", version: { status: "EVIDENCE_READY" } }], hasMore: true, nextCursor: "doc-cursor" };
      if (url.includes("cursor=5")) return { items: [{ id: "chunk-2" }], hasMore: false };
      if (url.includes("/chunks?")) return { items: [{ id: "chunk-1" }], hasMore: true, nextCursor: 5 };
      return {};
    });
    const documents = renderHook(() => useDocuments("kb-1"), { wrapper });
    await waitFor(() => expect(documents.result.current.data?.pages).toHaveLength(1));
    await act(async () => {
      await documents.result.current.fetchNextPage();
    });
    await waitFor(() => expect(documents.result.current.data?.pages).toHaveLength(2));
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("cursor=doc-cursor"))).toBe(true);

    const chunks = renderHook(() => useChunks("doc-1", true), { wrapper });
    await waitFor(() => expect(chunks.result.current.data?.pages).toHaveLength(1));
    await act(async () => {
      await chunks.result.current.fetchNextPage();
    });
    await waitFor(() => expect(chunks.result.current.data?.pages).toHaveLength(2));
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("cursor=5"))).toBe(true);
  });

  it("does not fetch disabled or unscoped queries", async () => {
    const fetchMock = stubFetch(() => ({}));
    renderHook(() => useKnowledgeBase(undefined), { wrapper });
    renderHook(() => useMembers("kb-1", false), { wrapper });
    renderHook(() => useDocumentContent(undefined, true), { wrapper });
    renderHook(() => useChunks(undefined, true), { wrapper });
    renderHook(() => useDocumentStatus(undefined), { wrapper });
    renderHook(() => useChunk(undefined), { wrapper });
    renderHook(() => useModelCatalog(false), { wrapper });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("remaining tenant and ops queries", () => {
  it("loads conversations, keys, dead letters and metrics", async () => {
    stubFetch((url) => {
      if (url.endsWith("/sessions")) return { items: [{ id: "conv-1" }] };
      if (url.includes("/sessions/conv-1/messages")) return { items: [{ id: "msg-1" }] };
      if (url.endsWith("/api-keys")) return { items: [{ id: "key-1" }] };
      if (url.includes("/ops/dead-letters")) return { items: [{ id: 1 }] };
      if (url.endsWith("/ops/metrics")) return { queues: {} };
      return {};
    });
    const conversations = renderHook(() => useConversations(), { wrapper });
    const messages = renderHook(() => useMessages("conv-1"), { wrapper });
    const keys = renderHook(() => useAPIKeys(), { wrapper });
    const dead = renderHook(() => useDeadLetters(), { wrapper });
    const metrics = renderHook(() => useOpsMetrics(), { wrapper });
    await waitFor(() => expect(conversations.result.current.data).toHaveLength(1));
    await waitFor(() => expect(messages.result.current.data).toHaveLength(1));
    await waitFor(() => expect(keys.result.current.data).toHaveLength(1));
    await waitFor(() => expect(dead.result.current.data).toHaveLength(1));
    await waitFor(() => expect(metrics.result.current.data).toBeTruthy());
  });

  it("loads tenant configuration and model catalog data", async () => {
    stubFetch((url) => {
      if (url.endsWith("/tenant")) return { id: "tenant-1", name: "Tenant", chatQuestionsToday: 0, documents: 0, knowledgeBases: 0, storageBytes: 0, members: { admins: 0, members: 0, owners: 0, total: 0, viewers: 0 }, createdAt: "2026-11-01T00:00:00Z" };
      if (url.endsWith("/tenant/quota")) return { maxDocuments: {} };
      if (url.endsWith("/tenant/config")) return { retrieval: {} };
      if (url.endsWith("/tenant/config/health")) return { status: "ok", checks: [] };
      if (url.endsWith("/tenant/members")) return { items: [{ userId: "u1" }] };
      if (url.endsWith("/models/catalog")) return { items: [{ model: "chat" }] };
      if (url.endsWith("/models/providers")) return { items: [{ id: "p1" }] };
      return {};
    });
    const overview = renderHook(() => useTenantOverview(), { wrapper });
    const quota = renderHook(() => useTenantQuota(), { wrapper });
    const configuration = renderHook(() => useTenantConfiguration(), { wrapper });
    const health = renderHook(() => useConfigHealth(), { wrapper });
    const members = renderHook(() => useTenantMembers(), { wrapper });
    const catalog = renderHook(() => useModelCatalog(), { wrapper });
    const providers = renderHook(() => useModelProviders(), { wrapper });
    await waitFor(() => expect(overview.result.current.data?.id).toBe("tenant-1"));
    await waitFor(() => expect(quota.result.current.data).toBeTruthy());
    await waitFor(() => expect(configuration.result.current.data).toBeTruthy());
    await waitFor(() => expect(health.result.current.data?.status).toBe("ok"));
    await waitFor(() => expect(members.result.current.data).toHaveLength(1));
    await waitFor(() => expect(catalog.result.current.data).toHaveLength(1));
    await waitFor(() => expect(providers.result.current.data).toHaveLength(1));
  });

  it("builds audit query parameters and defaults", async () => {
    const fetchMock = stubFetch(() => ({ items: [], total: 0, limit: 5, offset: 2 }));
    const audit = renderHook(
      () => useAuditEvents({ actorId: "user 1", action: "write", resourceType: "knowledge_base", allowed: "false", limit: 5, offset: 2 }),
      { wrapper },
    );
    await waitFor(() => expect(audit.result.current.data?.total).toBe(0));
    const auditCall = fetchMock.mock.calls.map(([input]) => String(input)).find((url) => url.includes("/audit?")) ?? "";
    expect(auditCall).toContain("actorId=user+1");
    expect(auditCall).toContain("action=write");
    expect(auditCall).toContain("resourceType=knowledge_base");
    expect(auditCall).toContain("allowed=false");
    expect(auditCall).toContain("limit=5");
    expect(auditCall).toContain("offset=2");

    const defaults = renderHook(() => useAuditEvents({}), { wrapper });
    await waitFor(() => expect(defaults.result.current.data?.total).toBe(0));
    const defaultCall = fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes("/audit?")).at(-1) ?? "";
    expect(defaultCall).toContain("limit=20");
    expect(defaultCall).toContain("offset=0");
  });
});
