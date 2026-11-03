import type { DocumentView, StreamEvent } from "./types";

export const apiBase = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8082/api/v1";

export const devHeaders = { "X-Dev-User-ID": "dev-user", "X-Dev-Tenant-ID": "dev-tenant" };

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function errorText(response: Response): Promise<string> {
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

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    ...init,
    credentials: "include",
    headers: { ...devHeaders, ...init.headers },
  });
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
  const response = await fetch(`${apiBase}/documents/${documentId}/preview`, { credentials: "include", headers: devHeaders });
  if (!response.ok) throw await toApiError(response);
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  window.open(url, "_blank", "noopener");
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export function parseSSEBlock(raw: string): StreamEvent | undefined {
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

// EventSource cannot attach the development identity headers, so the answer
// stream is consumed from fetch with the same headers as every other call.
export async function streamPost(path: string, body: unknown, onEvent: (event: StreamEvent) => void): Promise<void> {
  const response = await fetch(`${apiBase}${path}`, {
    method: "POST",
    credentials: "include",
    headers: { ...devHeaders, "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw await toApiError(response);
  const reader = response.body?.getReader();
  if (!reader) throw new Error("当前浏览器不支持流式响应");
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
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
}
