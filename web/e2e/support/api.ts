import { request, type APIRequestContext, type APIResponse } from "@playwright/test";

export const apiBase = (process.env.E2E_API_URL ?? "http://localhost:8082/api/v1").replace(/\/$/, "");
export const apiOrigin = apiBase.replace(/\/api\/v1$/, "");
export const devHeaders = {
  "X-Dev-User-ID": "dev-user",
  "X-Dev-Tenant-ID": "dev-tenant",
};

export type KnowledgeBase = {
  id: string;
  name: string;
};

export type DocumentSummary = {
  document: { id: string; filename: string };
  version: { status: string };
};

export type DocumentDetail = {
  pipeline?: { status: string };
};

export type EvidenceSet = {
  schemaVersion: string;
  items: Array<{ knowledgeBaseId: string }>;
  plan: { intent: string; vectorEnabled: boolean; keywordEnabled: boolean };
};

export async function newApi(): Promise<APIRequestContext> {
  return request.newContext({
    baseURL: `${apiBase}/`,
    extraHTTPHeaders: devHeaders,
    timeout: 20_000,
  });
}

async function readError(response: APIResponse): Promise<string> {
  return `${response.status()} ${await response.text()}`.trim();
}

export async function createKnowledgeBase(api: APIRequestContext, name: string): Promise<KnowledgeBase> {
  const response = await api.post("knowledge-bases", {
    data: { name, description: "Playwright E2E" },
  });
  if (response.status() !== 201) throw new Error(`create knowledge base: ${await readError(response)}`);
  return (await response.json()) as KnowledgeBase;
}

export async function deleteKnowledgeBase(api: APIRequestContext, id: string): Promise<void> {
  const response = await api.delete(`knowledge-bases/${id}`);
  if (response.status() !== 204 && response.status() !== 404) {
    throw new Error(`delete knowledge base: ${await readError(response)}`);
  }
}

export async function listDocuments(api: APIRequestContext, knowledgeBaseId: string): Promise<DocumentSummary[]> {
  const response = await api.get(`knowledge-bases/${knowledgeBaseId}/documents?limit=100`);
  if (!response.ok()) throw new Error(`list documents: ${await readError(response)}`);
  const body = (await response.json()) as { items?: DocumentSummary[] };
  return body.items ?? [];
}

export async function waitForDocument(
  api: APIRequestContext,
  knowledgeBaseId: string,
  filename: string,
  timeoutMs = 150_000,
): Promise<DocumentSummary> {
  const deadline = Date.now() + timeoutMs;
  let lastStatus = "not found";
  while (Date.now() < deadline) {
    const documents = await listDocuments(api, knowledgeBaseId);
    const match = documents.find((item) => item.document.filename === filename);
    if (match) {
      const response = await api.get(`documents/${match.document.id}`);
      if (!response.ok()) throw new Error(`document detail: ${await readError(response)}`);
      const detail = (await response.json()) as DocumentDetail;
      lastStatus = detail.pipeline?.status ?? "unknown";
      if (lastStatus === "EVIDENCE_READY") return match;
      if (lastStatus === "FAILED") throw new Error(`document ${filename} failed to parse`);
    }
    await new Promise((resolve) => setTimeout(resolve, 1_000));
  }
  throw new Error(`document ${filename} did not become EVIDENCE_READY (last status: ${lastStatus})`);
}

export async function searchEvidence(
  api: APIRequestContext,
  knowledgeBaseId: string,
  query: string,
): Promise<EvidenceSet> {
  const response = await api.post("search", {
    data: { query, knowledgeBaseId },
  });
  if (!response.ok()) throw new Error(`search: ${await readError(response)}`);
  return (await response.json()) as EvidenceSet;
}
