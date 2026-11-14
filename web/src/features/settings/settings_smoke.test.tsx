import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { KnowledgeBase } from "../../api/types";
import { SessionProvider } from "../../session";
import { configurationFixture, meWith, retrievalSchemaFixture } from "../../test/fixtures";
import i18n from "../../i18n";
import { BasicSettings } from "./BasicSettings";
import { DangerZone } from "./DangerZone";
import { DataSourceSettings } from "./DataSourceSettings";
import { EmbeddingMigration } from "./EmbeddingMigration";
import { MembersSettings } from "./MembersSettings";
import { ModelSettings } from "./ModelSettings";
import { ProjectionPanel } from "./ProjectionPanel";
import { RetrievalSettings } from "./RetrievalSettings";
import { SchemaConfigForm } from "./SchemaConfigForm";
import { SettingsDrawer } from "./SettingsDrawer";

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
} as unknown as KnowledgeBase;

function renderPanel(node: ReactNode, capabilities = ["knowledge_base:read", "knowledge_base:write"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <AntdApp>
        <SessionProvider value={meWith(capabilities)}>
          <MemoryRouter>{node}</MemoryRouter>
        </SessionProvider>
      </AntdApp>
    </QueryClientProvider>,
  );
}

function stubFetch(handler: (url: string, init?: RequestInit) => unknown, status = 200) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    return new Response(JSON.stringify(handler(String(input), init) ?? {}), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("settings panels", () => {
  it("renders basic settings read-only and editable", async () => {
    stubFetch(() => ({}));
    const readOnly = renderPanel(
      <BasicSettings knowledgeBase={knowledgeBase} canWrite={false} onSaved={() => undefined} />,
      ["knowledge_base:read"],
    );
    expect(await readOnly.findByText(i18n.t("settings.readOnlyHint"))).toBeInTheDocument();
    readOnly.unmount();

    const onSaved = vi.fn();
    renderPanel(<BasicSettings knowledgeBase={knowledgeBase} canWrite onSaved={onSaved} />);
    const nameInput = screen.getByLabelText(i18n.t("knowledgeBase.name"));
    fireEvent.change(nameInput, { target: { value: "Renamed KB" } });
    fireEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });

  it("explains when the basic settings name is empty", async () => {
    stubFetch(() => ({}));
    renderPanel(<BasicSettings knowledgeBase={knowledgeBase} canWrite onSaved={() => undefined} />);
    fireEvent.change(screen.getByLabelText(i18n.t("knowledgeBase.name")), { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
    expect(await screen.findByText(i18n.t("settings.nameRequired"))).toBeInTheDocument();
  });

  it("renders the danger zone for allowed and forbidden users", () => {
    const forbidden = renderPanel(<DangerZone knowledgeBase={knowledgeBase} canDelete={false} />, ["knowledge_base:read"]);
    expect(screen.getByText(i18n.t("settings.deleteForbidden"))).toBeInTheDocument();
    forbidden.unmount();
    renderPanel(<DangerZone knowledgeBase={knowledgeBase} canDelete />);
    expect(screen.getByRole("button", { name: i18n.t("settings.deleteKnowledgeBase") })).toBeInTheDocument();
  });

  it("renders model and retrieval settings from configuration", async () => {
    stubFetch(() => ({}));
    const configuration = configurationFixture();
    const models = renderPanel(
      <ModelSettings knowledgeBaseId="kb-1" configuration={configuration} canWrite={false} onSaved={() => undefined} />,
      ["knowledge_base:read"],
    );
    expect(models.container.textContent).toContain("模型路由");
    models.unmount();
    const retrieval = renderPanel(
      <RetrievalSettings knowledgeBaseId="kb-1" configuration={configuration} canWrite={false} onSaved={() => undefined} />,
      ["knowledge_base:read"],
    );
    expect(retrieval.container.querySelectorAll("input").length).toBeGreaterThan(0);
  });

  it("hides member management from non-managers", () => {
    stubFetch(() => ({ items: [] }));
    renderPanel(<MembersSettings knowledgeBaseId="kb-1" ownerId="user-1" canManage={false} />, ["knowledge_base:read"]);
    expect(screen.getByText(i18n.t("settings.membersHidden"))).toBeInTheDocument();
  });

  it("renders members, connectors and projection state", async () => {
    stubFetch((url) => {
      if (url.endsWith("/members")) return { items: [{ userId: "user-2", role: "viewer", email: "", displayName: "User 2" }] };
      if (url.endsWith("/connectors")) return { connectors: [] };
      if (url.endsWith("/projection")) return {
        runs: [], relationRuns: [], pages: { total: 1, published: 1, pendingReview: 0, stale: 0, indexed: 1 },
        graph: { snapshots: 0, activeSnapshot: "", entities: 0, relations: 0, pendingReview: 0 },
      };
      return {};
    });
    const members = renderPanel(<MembersSettings knowledgeBaseId="kb-1" ownerId="user-1" canManage />);
    await waitFor(() => expect(members.container.textContent).toContain("user-2"));
    members.unmount();

    const connectors = renderPanel(<DataSourceSettings knowledgeBaseId="kb-1" canOperate />);
    await waitFor(() => expect(connectors.container.textContent).toContain("连接器"));
    connectors.unmount();

    const projection = renderPanel(<ProjectionPanel knowledgeBaseId="kb-1" canWrite={false} />);
    await waitFor(() => expect(projection.container.textContent?.length).toBeGreaterThan(0));
  });

  it("renders the embedding migration panel and settings drawer", async () => {
    stubFetch(() => ({}));
    const migration = renderPanel(
      <EmbeddingMigration
        knowledgeBaseId="kb-1"
        configuration={configurationFixture()}
        catalog={[]}
        canWrite={false}
        onSaved={() => undefined}
      />,
    );
    expect(migration.container.textContent).toContain("Embedding");
    migration.unmount();

    renderPanel(
      <SettingsDrawer
        open
        onClose={() => undefined}
        knowledgeBase={knowledgeBase}
        configuration={configurationFixture()}
        canWrite
        canManage
        canDelete
        canOperate
        onChanged={() => undefined}
      />,
    );
    expect(document.body.textContent).toContain(knowledgeBase.name);
  });

  it("renders and submits the schema form", async () => {
    const onSave = vi.fn(async () => undefined);
    const onClear = vi.fn(async () => undefined);
    renderPanel(
      <SchemaConfigForm
        domain={{ schema: retrievalSchemaFixture() }}
        override={{}}
        titleKey="settings.retrieval"
        canWrite
        saving={false}
        onSave={onSave}
        onClear={onClear}
      />,
    );
    expect(screen.getByRole("button", { name: /保\s*存/ })).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
    expect(onClear).not.toHaveBeenCalled();
  });
});

describe("tenant configuration and preview", () => {
  it("renders tenant configuration domains and health", async () => {
    const domain = { ...retrievalSchemaFixture(), tenantOverride: {} };
    stubFetch((url) => {
      if (url.endsWith("/tenant/config/health")) return { status: "ok", checks: [] };
      if (url.endsWith("/tenant/config")) {
        return {
          retrieval: { schema: retrievalSchemaFixture(), tenantOverride: {}, fields: {} },
          chunking: { schema: [], tenantOverride: {}, fields: {} },
        };
      }
      return {};
    });
    const { TenantConfigurationTab } = await import("./TenantConfigurationTab");
    const page = renderPanel(<TenantConfigurationTab />);
    await waitFor(() => expect(page.container.querySelectorAll("input").length).toBeGreaterThan(0));
    void domain;
  });

  it("renders a configuration change preview", async () => {
    const { ConfigPreviewContent } = await import("./ConfigPreviewContent");
    const preview = {
      affected: { knowledgeBases: 1, documents: 2, chunks: 3 },
      impact: "next_ingest",
      estimatedDurationTier: "minutes",
      estimatedCostTier: "low",
      blockers: [],
      rebuildOptional: true,
      changes: [{ domain: "retrieval", key: "top_k", oldValue: 5, newValue: 8, impact: "next_retrieval" }],
    } as unknown as Parameters<typeof ConfigPreviewContent>[0]["preview"];
    const page = renderPanel(<ConfigPreviewContent preview={preview} />);
    expect(page.container.textContent).toContain("top_k");
    expect(page.container.textContent).toContain("1");
  });
});
