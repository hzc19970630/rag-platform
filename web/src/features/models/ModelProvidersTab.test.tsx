import { describe, expect, it } from "vitest";
import { buildProviderRequest, deploymentCatalog, type Draft } from "./ModelProvidersTab";
import type { ModelCatalogEntry } from "../../api/types";

function catalogEntry(overrides: Partial<ModelCatalogEntry> = {}): ModelCatalogEntry {
  return {
    id: "",
    providerId: "",
    provider: "remote",
    model: "chat-a",
    displayName: "chat-a",
    capability: "chat",
    version: "deployment",
    dimension: 0,
    source: "deployment",
    enabled: true,
    ...overrides,
  };
}

function draft(overrides: Partial<Draft> = {}): Draft {
  return {
    name: " primary ",
    kind: "openai_compatible",
    baseUrl: " https://llm.example.com/v1 ",
    apiKey: "",
    apiKeySet: true,
    healthUrl: "",
    enabled: true,
    models: [{ model: "chat-a", displayName: "", capability: "chat", version: "", dimension: 0, enabled: true }],
    ...overrides,
  };
}

describe("deploymentCatalog", () => {
  it("keeps only env/YAML models and hides tenant provider entries", () => {
    const items = [
      catalogEntry({ model: "chat-env", capability: "chat", source: "deployment", dimension: 0 }),
      catalogEntry({ model: "embed-env", capability: "embedding", source: "deployment", dimension: 1024 }),
      catalogEntry({ id: "m1", providerId: "p1", model: "chat-tenant", provider: "tenant-a", source: "provider" }),
    ];
    expect(deploymentCatalog(items).map((entry) => entry.model)).toEqual(["chat-env", "embed-env"]);
  });
});

describe("buildProviderRequest", () => {
  it("keeps the stored key when the field is blank and trims the endpoint", () => {
    const payload = buildProviderRequest(draft());
    expect(payload.name).toBe("primary");
    expect(payload.base_url).toBe("https://llm.example.com/v1");
    expect(payload.api_key).toBeUndefined();
    expect(payload.models).toHaveLength(1);
    expect(payload.models[0].version).toBe("");
  });

  it("sends a typed key and drops empty model rows", () => {
    const payload = buildProviderRequest(draft({
      apiKey: "sk-typed",
      models: [
        { model: " ", displayName: "", capability: "chat", version: "", dimension: 0, enabled: true },
        { model: "embed-a", displayName: "Embed A", capability: "embedding", version: "v3", dimension: 1024, enabled: false },
      ],
    }));
    expect(payload.api_key).toBe("sk-typed");
    expect(payload.models).toEqual([
      {
        model: "embed-a", display_name: "Embed A", capability: "embedding",
        version: "v3", dimension: 1024, enabled: false,
      },
    ]);
  });

  it("zeroes the dimension for non-embedding capabilities", () => {
    const payload = buildProviderRequest(draft({
      models: [{ model: "rerank-a", displayName: "", capability: "rerank", version: "", dimension: 768, enabled: true }],
    }));
    expect(payload.models[0].dimension).toBe(0);
  });
});