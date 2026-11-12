import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { SessionProvider } from "../session";
import { meWith } from "../test/fixtures";
import { buildMcpClientConfig, McpPage, mcpKeyRequest } from "./McpPage";

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
  it("only carries knowledge_base:read, chat and the selected knowledge base", () => {
    expect(mcpKeyRequest("mcp-demo", "kb-1")).toEqual({
      name: "mcp-demo",
      capabilities: ["knowledge_base:read", "chat"],
      knowledgeBaseIds: ["kb-1"],
    });
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
});

describe("mcp page access", () => {
  it("shows the permission notice to users without tenant administration", async () => {
    renderPage(["knowledge_base:read"]);
    expect(await screen.findByText(i18n.t("mcp.noPermission"))).toBeInTheDocument();
  });

  it("renders the key and configuration steps for tenant operators", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ items: [{ id: "kb-1", name: "Demo handbook" }] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    renderPage(["knowledge_base:read", "tenant:admin"]);

    expect(await screen.findByText(i18n.t("mcp.keyStepTitle"))).toBeInTheDocument();
    expect(await screen.findByPlaceholderText("mcp-Demo handbook")).toBeInTheDocument();
    expect(screen.getByText(i18n.t("mcp.toolsTitle"))).toBeInTheDocument();
    expect(screen.getByText(i18n.t("mcp.buildTitle"))).toBeInTheDocument();
  });
});
