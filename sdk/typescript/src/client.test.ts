import assert from "node:assert/strict";
import { test } from "node:test";

import { RagApiError, RagClient } from "./client";

const sse = [
  'event: answer\ndata: {"request_id":"req-1","sequence":1,"type":"answer","data":{"content":"answer text","citations":[{"chunkId":"c1"}],"status":"COMPLETED"}}\n\n',
  'event: complete\ndata: {"request_id":"req-1","sequence":2,"type":"complete","data":{"status":"COMPLETED"}}\n\n',
].join("");

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const auth = new Headers(init?.headers).get("Authorization");
  assert.equal(auth, "Bearer test-key");
  if (url.endsWith("/knowledge-bases")) {
    return Promise.resolve(Response.json({ items: [{ id: "kb1" }] }));
  }
  if (url.endsWith("/search")) {
    const body = JSON.parse(String(init?.body ?? "{}")) as { knowledgeBaseId?: string };
    if (body.knowledgeBaseId === "kb-forbidden") {
      return Promise.resolve(Response.json({ error: "forbidden", message: "outside scope" }, { status: 403 }));
    }
    return Promise.resolve(Response.json({ items: [{ chunkId: "c1" }] }));
  }
  if (url.endsWith("/sessions")) {
    return Promise.resolve(Response.json({ id: "s1" }, { status: 201 }));
  }
  if (url.endsWith("/sessions/s1/messages")) {
    return Promise.resolve(new Response(sse, { headers: { "content-type": "text/event-stream" } }));
  }
  if (url.endsWith("/chunks/c1")) {
    return Promise.resolve(Response.json({ chunkId: "c1", content: "hello", sourcePath: "doc.md" }));
  }
  if (url.endsWith("/documents/d1/status")) {
    return Promise.resolve(Response.json({ contentReady: true }));
  }
  return Promise.resolve(Response.json({ error: "not_found" }, { status: 404 }));
}

function client(): RagClient {
  return new RagClient({
    baseUrl: "http://test/api/v1",
    apiKey: "test-key",
    fetch: fakeFetch as typeof fetch,
  });
}

test("search returns evidence and citation lookup works", async () => {
  const api = client();
  const evidence = await api.search({ query: "hello", knowledgeBaseId: "kb1" });
  assert.equal(evidence.items?.[0]?.chunkId, "c1");
  const chunk = await api.getCitation("c1");
  assert.equal(chunk.sourcePath, "doc.md");
  assert.equal((await api.getDocumentStatus("d1")).contentReady, true);
});

test("ask streams answer and citations", async () => {
  const result = await client().ask({ sessionId: "s1", question: "hello", requestId: "req-1" });
  assert.equal(result.content, "answer text");
  assert.equal(result.status, "COMPLETED");
  assert.equal(result.citations[0]?.chunkId, "c1");
});

test("ask creates a session when absent", async () => {
  const result = await client().ask({ knowledgeBaseId: "kb1", question: "hello", requestId: "req-1" });
  assert.equal(result.sessionId, "s1");
});

test("forbidden knowledge base becomes a typed error", async () => {
  await assert.rejects(
    () => client().search({ query: "hello", knowledgeBaseId: "kb-forbidden" }),
    (error: unknown) => error instanceof RagApiError && error.status === 403 && error.code === "forbidden",
  );
});