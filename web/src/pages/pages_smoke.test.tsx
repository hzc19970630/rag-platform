import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionProvider } from "../session";
import { configurationFixture, meWith } from "../test/fixtures";
import { DocumentPage } from "./DocumentPage";
import { KnowledgeBasePage } from "./KnowledgeBasePage";
import { KnowledgeBasesPage } from "./KnowledgeBasesPage";
import { OpsPage } from "./OpsPage";
import { App } from "../App";
import i18n from "../i18n";

const knowledgeBase = {
  id: "kb-1",
  tenantId: "tenant-1",
  ownerId: "user-1",
  name: "Test KB",
  description: "Description",
  embeddingModel: "bge-m3",
  embeddingDimension: 1024,
  aclVersion: 3,
  retrievalConfig: {},
  indexingStrategy: {},
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
};

const projectionStatus = {
  knowledgeBaseId: "kb-1",
  joinStatus: "KNOWLEDGE_PARTIAL",
  wikiEnabled: true,
  graphEnabled: false,
  runs: [],
  relationRuns: [],
  pages: { total: 0, published: 0, pendingReview: 0, stale: 0, indexed: 0 },
  graph: { snapshots: 0, activeSnapshot: "", entities: 0, relations: 0, pendingReview: 0 },
};

function pageData(url: string): unknown {
  if (url.includes("/knowledge-bases/kb-1/configuration")) return configurationFixture();
  if (url.includes("/knowledge-bases/kb-1/projection")) return projectionStatus;
  if (url.includes("/knowledge-bases/kb-1/documents")) return { items: [], hasMore: false };
  if (url.includes("/knowledge-bases/kb-1/members")) return { items: [] };
  if (url.includes("/knowledge-bases/kb-1/connectors")) return { connectors: [] };
  if (url.endsWith("/knowledge-bases/kb-1")) return knowledgeBase;
  if (url.endsWith("/knowledge-bases")) return { items: [knowledgeBase] };
  if (url.includes("/documents/doc-1/status")) {
    return {
      document: { id: "doc-1", filename: "doc.md", status: "EVIDENCE_READY", knowledgeBaseId: "kb-1" },
      version: { id: "ver-1", status: "EVIDENCE_READY", version: 1, sizeBytes: 10 },
      contentReady: true,
      stages: [],
      pipeline: { id: "pipe-1", status: "EVIDENCE_READY" },
    };
  }
  if (url.includes("/documents/doc-1/versions")) return { items: [{ id: "ver-1", version: 1, status: "EVIDENCE_READY" }] };
  if (url.includes("/documents/doc-1/content")) return { content: "hello content", mimeType: "text/markdown", filename: "doc.md" };
  if (url.includes("/documents/doc-1/chunks")) return { items: [], hasMore: false };
  if (url.includes("/ops/metrics")) return { counters: {}, gauges: {}, timings: {}, queues: {}, operations: { outboxBacklog: 0 } };
  if (url.includes("/ops/dead-letters")) return { items: [] };
  if (url.endsWith("/api-keys")) return { items: [] };
  return {};
}

function stubFetch(handler: (url: string) => { status?: number; body: unknown } = (url) => ({ body: pageData(url) })) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const response = handler(String(input));
    return new Response(JSON.stringify(response.body ?? {}), {
      status: response.status ?? 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPage(node: ReactNode, path: string, capabilities = [
  "knowledge_base:read", "knowledge_base:write", "knowledge_base:delete",
  "knowledge_base:manage_members", "chat", "tenant:admin",
]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <AntdApp>
        <SessionProvider value={meWith(capabilities)}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="/knowledge-bases" element={<KnowledgeBasesPage />} />
              <Route path="/knowledge-bases/:knowledgeBaseId" element={<KnowledgeBasePage />} />
              <Route path="/documents/:documentId" element={<DocumentPage />} />
              <Route path="/ops" element={<OpsPage />} />
              <Route path="*" element={node} />
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </AntdApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

describe("page smoke tests", () => {
  it("renders the knowledge base list", async () => {
    stubFetch();
    renderPage(<KnowledgeBasesPage />, "/knowledge-bases");
    expect(await screen.findByText("Test KB")).toBeInTheDocument();
  });

  it("renders a knowledge base detail page", async () => {
    stubFetch();
    const page = renderPage(<KnowledgeBasePage />, "/knowledge-bases/kb-1");
    await waitFor(() => expect(page.container.textContent).toContain("Test KB"));
  });

  it("renders a document detail page", async () => {
    stubFetch();
    const page = renderPage(<DocumentPage />, "/documents/doc-1");
    await waitFor(() => expect(page.container.textContent).toContain("doc.md"));
  });

  it("renders the operations page for administrators", async () => {
    stubFetch();
    const page = renderPage(<OpsPage />, "/ops");
    await waitFor(() => expect(page.container.textContent).toContain("死信"));
  });

  it("hides the operations page from non-administrators", async () => {
    stubFetch();
    renderPage(<OpsPage />, "/ops", ["knowledge_base:read"]);
    expect(await screen.findByText(i18n.t("common.noPermission"))).toBeInTheDocument();
  });
});

describe("App routing", () => {
  it("redirects an unauthenticated session to login", async () => {
    stubFetch((url) => {
      if (url.endsWith("/me")) return { status: 401, body: { error: "unauthenticated" } };
      if (url.endsWith("/auth/config")) return { body: { oidcEnabled: false, ldapEnabled: false, devAuthEnabled: true } };
      return { body: {} };
    });
    window.history.pushState({}, "", "/knowledge-bases");
    render(<App />);
    expect((await screen.findAllByText(/登录|Login|sign in/i)).length).toBeGreaterThan(0);
  });

  it("shows a retryable error for server failures", async () => {
    stubFetch((url) => {
      if (url.endsWith("/me")) return { status: 400, body: { error: "bad_request" } };
      return { body: {} };
    });
    window.history.pushState({}, "", "/knowledge-bases");
    render(<App />);
    expect(await screen.findByRole("button", { name: /重\s*试|Retry/i })).toBeInTheDocument();
  });

  it("renders the protected shell for an authenticated session", async () => {
    stubFetch((url) => {
      if (url.endsWith("/me")) return { body: meWith(["knowledge_base:read"]) };
      return { body: pageData(url) };
    });
    window.history.pushState({}, "", "/knowledge-bases");
    render(<App />);
    expect(await screen.findByText("Test KB")).toBeInTheDocument();
  });
});
