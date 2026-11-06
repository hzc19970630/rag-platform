import type { DocumentView, StreamEvent } from "./types";

export const apiBase = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8082/api/v1";

// Development identity headers must never ship in a production build.
export function devHeaders(dev = import.meta.env.DEV): Record<string, string> {
  return dev ? { "X-Dev-User-ID": "dev-user", "X-Dev-Tenant-ID": "dev-tenant" } : {};
}

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function errorText(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { error?: string; message?: string };
    return [body.error, body.message].filter(Boolean).join(": ") || `请求失败 (${response.status})`;
  } catch {
    return response.statusText || `请求失败 (${response.status})`;
  }
}

async function toApiError(response: Response): Promise<ApiError> {
  try {
    const body = (await response.clone().json()) as { error?: string; message?: string };
    return new ApiError(response.status, [body.error, body.message].filter(Boolean).join(": "), body.error ?? "");
  } catch {
    return new ApiError(response.status, await errorText(response));
  }
}

let refreshPromise: Promise<boolean> | null = null;

// refreshSession is single-flight: a burst of 401s shares one refresh call and
// the next request retries against the rotated cookie.
function refreshSession(): Promise<boolean> {
  if (!refreshPromise) {
    refreshPromise = fetch(`${apiBase}/auth/refresh`, {
      method: "POST",
      credentials: "include",
      headers: devHeaders(),
    })
      .then((response) => response.ok)
      .catch(() => false)
      .finally(() => {
        refreshPromise = null;
      });
  }
  return refreshPromise;
}

async function rawFetch(path: string, init: RequestInit): Promise<Response> {
  return fetch(`${apiBase}${path}`, {
    ...init,
    credentials: "include",
    headers: { ...devHeaders(), ...init.headers },
  });
}

async function request(path: string, init: RequestInit = {}): Promise<Response> {
  const response = await rawFetch(path, init);
  if (response.status !== 401 || path === "/auth/refresh") {
    return response;
  }
  if (!(await refreshSession())) {
    return response;
  }
  return rawFetch(path, init);
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await request(path, init);
  if (!response.ok) throw await toApiError(response);
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export function jsonInit(body: unknown, method = "POST"): RequestInit {
  return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

export function uploadDocument(knowledgeBaseId: string, file: File, documentId?: string): Promise<DocumentView> {
  const form = new FormData();
  form.append("file", file);
  if (documentId) form.append("document_id", documentId);
  return api<DocumentView>(`/knowledge-bases/${knowledgeBaseId}/documents`, {
    method: "POST",
    headers: { "Idempotency-Key": `web-${crypto.randomUUID()}` },
    body: form,
  });
}

export async function openDocumentPreview(documentId: string): Promise<void> {
  const response = await request(`/documents/${documentId}/preview`);
  if (!response.ok) throw await toApiError(response);
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  window.open(url, "_blank", "noopener");
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

function parseSSEBlock(raw: string): StreamEvent | undefined {
  let eventType = "message";
  let eventId = "";
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("event:")) eventType = line.slice(6).trim();
    else if (line.startsWith("id:")) eventId = line.slice(3).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
  }
  if (dataLines.length === 0) return undefined;
  try {
    const parsed = JSON.parse(dataLines.join("\n")) as StreamEvent;
    return { ...parsed, type: parsed.type || eventType, event_id: parsed.event_id || eventId };
  } catch {
    return undefined;
  }
}

export type StreamOptions = {
  signal?: AbortSignal;
  headers?: Record<string, string>;
};

async function consumeSSE(response: Response, onEvent: (event: StreamEvent) => void, signal?: AbortSignal): Promise<void> {
  const reader = response.body?.getReader();
  if (!reader) throw new Error("当前浏览器不支持流式响应");
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      if (signal?.aborted) return;
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let separator = buffer.indexOf("\n\n");
      while (separator >= 0) {
        const block = buffer.slice(0, separator);
        buffer = buffer.slice(separator + 2);
        const event = parseSSEBlock(block);
        if (event) onEvent(event);
        separator = buffer.indexOf("\n\n");
      }
    }
  } finally {
    reader.releaseLock();
  }
}

// EventSource cannot attach the development identity headers, so the answer
// stream is consumed from fetch with the same headers as every other call.
export async function streamPost(
  path: string,
  body: unknown,
  onEvent: (event: StreamEvent) => void,
  options: StreamOptions = {},
): Promise<void> {
  const response = await request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify(body),
    signal: options.signal,
  });
  if (!response.ok) throw await toApiError(response);
  await consumeSSE(response, onEvent, options.signal);
}

export async function streamGet(
  path: string,
  onEvent: (event: StreamEvent) => void,
  options: StreamOptions = {},
): Promise<void> {
  const response = await request(path, {
    method: "GET",
    headers: { Accept: "text/event-stream", ...options.headers },
    signal: options.signal,
  });
  if (!response.ok) throw await toApiError(response);
  await consumeSSE(response, onEvent, options.signal);
}
