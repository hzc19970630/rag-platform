import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { SessionProvider } from "../session";
import { meWith } from "../test/fixtures";
import {
  buildMcpClientConfig,
  evidenceCount,
  McpPage,
  mcpDefaultKeyName,
  mcpExpiry,
  mcpKeyRequest,
} from "./McpPage";

function renderPage(capabilities: string[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntdApp>
        <MemoryRouter>
          <SessionProvider value={meWith(capabilities)}>
            <McpPage />
          </SessionProvider>
        </MemoryRouter>
      </AntdApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("mcp key request", () => {
  it("only carries knowledge_base:read, chat and the selected knowledge bases", () => {
    expect(mcpKeyRequest("mcp-demo", ["kb-1", "kb-2"])).toEqual({
      name: "mcp-demo",
      capabilities: ["knowledge_base:read", "chat"],
      knowledgeBaseIds: ["kb-1", "kb-2"],
    });
  });

  it("adds the expiry only when one is selected", () => {
    expect(mcpKeyRequest("mcp-demo", ["kb-1"], "2026-12-12T00:00:00.000Z")).toMatchObject({
      expiresAt: "2026-12-12T00:00:00.000Z",
    });
    expect(mcpKeyRequest("mcp-demo", ["kb-1"])).not.toHaveProperty("expiresAt");
  });
});

describe("mcp key expiry", () => {
  it("turns day presets into an absolute timestamp and keeps never-expire unset", () => {
    const now = new Date("2026-11-12T00:00:00.000Z");
    expect(mcpExpiry(30, now)).toBe("2026-12-12T00:00:00.000Z");
    expect(mcpExpiry(7, now)).toBe("2026-11-19T00:00:00.000Z");
    expect(mcpExpiry(0, now)).toBeUndefined();
  });
});

describe("mcp default key name", () => {
  it("uses the single knowledge base name and a count for multi-select", () => {
    expect(mcpDefaultKeyName([])).toBe("");
    expect(mcpDefaultKeyName([{ name: "Demo handbook" }])).toBe("mcp-Demo handbook");
    expect(mcpDefaultKeyName([{ name: "A" }, { name: "B" }])).toBe("mcp-2-knowledge-bases");
  });
});

describe("mcp client configuration", () => {
  it("passes the API URL and key through stdio environment variables", () => {
    const config = JSON.parse(buildMcpClientConfig({
      apiUrl: "http://localhost:8082/api/v1",
      apiKey: "rk_example",
      command: "/opt/rag-mcp",
    }));
    expect(config.mcpServers["rag-platform"]).toEqual({
      command: "/opt/rag-mcp",
      env: {
        RAG_API_URL: "http://localhost:8082/api/v1",
        RAG_API_KEY: "rk_example",
      },
    });
  });

  it("counts evidence items defensively", () => {
    expect(evidenceCount({ items: [{ id: 1 }, { id: 2 }] })).toBe(2);
    expect(evidenceCount({ evidence: { items: [{ id: 1 }] } })).toBe(1);
    expect(evidenceCount({})).toBe(0);
    expect(evidenceCount(undefined)).toBe(0);
  });
});

describe("mcp page access", () => {
  it("shows the permission notice to users without tenant administration", async () => {
    renderPage(["knowledge_base:read"]);
    expect(await screen.findByText(i18n.t("mcp.noPermission"))).toBeInTheDocument();
  });

  it("renders the key, configuration and self-check steps for tenant operators", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ items: [{ id: "kb-1", name: "Demo handbook" }] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    renderPage(["knowledge_base:read", "tenant:admin"]);

    expect(await screen.findByText(i18n.t("mcp.keyStepTitle"))).toBeInTheDocument();
    expect(await screen.findByPlaceholderText("mcp-Demo handbook")).toBeInTheDocument();
    expect(screen.getByText(i18n.t("mcp.toolsTitle"))).toBeInTheDocument();
    expect(screen.getByText(i18n.t("mcp.selfCheckTitle"))).toBeInTheDocument();
    expect(screen.getByText(i18n.t("mcp.releaseTitle"))).toBeInTheDocument();
  });
});
