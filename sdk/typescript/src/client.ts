import type { components } from "./generated";

type EvidenceSet = components["schemas"]["EvidenceSet"];
type Citation = components["schemas"]["Citation"];
type Chunk = components["schemas"]["Chunk"];
type Conversation = components["schemas"]["Conversation"];
type DocumentStatus = components["schemas"]["DocumentStatus"];
type KnowledgeBaseList = components["schemas"]["KnowledgeBaseList"];
type StreamEvent = components["schemas"]["StreamEvent"];

export type RagClientOptions = {
  /** API base URL, including /api/v1, e.g. http://localhost:8082/api/v1 */
  baseUrl: string;
  /** API key created with knowledge_base:read and chat capabilities. */
  apiKey: string;
  /** Whole-request timeout in milliseconds (default 180000). */
  timeoutMs?: number;
  /** Injectable fetch for tests/proxies. */
  fetch?: typeof fetch;
};

export type SearchInput = {
  query: string;
  knowledgeBaseId: string;
  sessionOverride?: string;
};

export type AskInput = {
  question: string;
  knowledgeBaseId?: string;
  sessionId?: string;
  title?: string;
  requestId?: string;
  signal?: AbortSignal;
};

export type AskResult = {
  sessionId: string;
  requestId: string;
  content: string;
  status: string;
  citations: Citation[];
};

export class RagApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message?: string) {
    super(message ? `${code}: ${message}` : code);
    this.name = "RagApiError";
    this.status = status;
    this.code = code;
  }
}

export class RagClient {
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(options: RagClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.apiKey = options.apiKey;
    this.timeoutMs = options.timeoutMs ?? 180_000;
    this.fetchImpl = options.fetch ?? globalThis.fetch;
  }

  async listKnowledgeBases(signal?: AbortSignal): Promise<KnowledgeBaseList> {
    return this.request<KnowledgeBaseList>("GET", "/knowledge-bases", undefined, signal);
  }

  async search(input: SearchInput, signal?: AbortSignal): Promise<EvidenceSet> {
    return this.request<EvidenceSet>("POST", "/search", input, signal);
  }

  async getDocumentStatus(documentId: string, signal?: AbortSignal): Promise<DocumentStatus> {
    return this.request<DocumentStatus>(
      "GET",
      `/documents/${encodeURIComponent(documentId)}/status`,
      undefined,
      signal,
    );
  }

  async getCitation(chunkId: string, signal?: AbortSignal): Promise<Chunk> {
    return this.request<Chunk>("GET", `/chunks/${encodeURIComponent(chunkId)}`, undefined, signal);
  }

  async ask(input: AskInput): Promise<AskResult> {
    let sessionId = input.sessionId;
    if (!sessionId) {
      if (!input.knowledgeBaseId) {
        throw new Error("knowledgeBaseId is required when sessionId is absent");
      }
      const conversation = await this.request<Conversation>(
        "POST",
        "/sessions",
        { knowledge_base_id: input.knowledgeBaseId, title: input.title },
        input.signal,
      );
      if (!conversation.id) throw new Error("session creation returned no id");
      sessionId = conversation.id;
    }
    const requestId = input.requestId ?? `sdk-${globalThis.crypto.randomUUID()}`;
    let content = "";
    let status = "";
    let code = "";
    let citations: Citation[] = [];
    let lastSequence = 0;
    let complete = false;
    let tailTimeout = false;

    const handle = (event: StreamEvent) => {
      if (event.sequence > lastSequence) lastSequence = event.sequence;
      const data = (event.data ?? {}) as Record<string, unknown>;
      switch (event.type) {
        case "answer":
          content = typeof data.content === "string" ? data.content : content;
          status = typeof data.status === "string" ? data.status : status;
          if (Array.isArray(data.citations)) citations = data.citations as Citation[];
          break;
        case "complete":
          status = typeof data.status === "string" ? data.status : status;
          code = typeof data.code === "string" ? data.code : code;
          complete = true;
          break;
        case "error": {
          const errorCode = typeof data.code === "string" ? data.code : "error";
          if (errorCode === "tail_timeout") {
            tailTimeout = true;
            break;
          }
          throw new RagApiError(0, errorCode, typeof data.message === "string" ? data.message : "");
        }
      }
    };

    await this.stream(
      "POST",
      `/sessions/${encodeURIComponent(sessionId)}/messages`,
      { query: input.question, requestId },
      handle,
      input.signal,
    );
    for (let attempt = 0; attempt < 3 && !complete; attempt++) {
      if (!tailTimeout && lastSequence === 0) break;
      tailTimeout = false;
      await delay(250 * (attempt + 1), input.signal);
      await this.stream(
        "GET",
        `/requests/${encodeURIComponent(requestId)}/events?stream=1&after=${lastSequence}`,
        undefined,
        handle,
        input.signal,
      );
    }
    if (!complete) {
      throw new Error(`answer stream ended before a terminal event (request ${requestId})`);
    }
    if (status === "FAILED" || status === "TIMEOUT" || status === "CANCELLED") {
      throw new RagApiError(0, code || status.toLowerCase(), `answer finished with status ${status}`);
    }
    return { sessionId, requestId, content, status, citations };
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    const response = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method,
      headers: this.headers("application/json"),
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: this.combinedSignal(signal),
    });
    if (!response.ok) throw await toApiError(response);
    return (await response.json()) as T;
  }

  private async stream(
    method: string,
    path: string,
    body: unknown,
    onEvent: (event: StreamEvent) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    const response = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method,
      headers: this.headers("text/event-stream"),
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: this.combinedSignal(signal),
    });
    if (!response.ok) throw await toApiError(response);
    if (!(response.headers.get("content-type") ?? "").includes("text/event-stream")) {
      throw new Error(`expected an event stream, got ${response.headers.get("content-type")}`);
    }
    if (!response.body) throw new Error("event stream has no body");
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let separator = buffer.indexOf("\n\n");
        while (separator >= 0) {
          const block = buffer.slice(0, separator);
          buffer = buffer.slice(separator + 2);
          const payload = block
            .split(/\r?\n/)
            .filter((line) => line.startsWith("data:"))
            .map((line) => line.slice(5).trimStart())
            .join("\n");
          if (payload.trim() !== "") onEvent(JSON.parse(payload) as StreamEvent);
          separator = buffer.indexOf("\n\n");
        }
      }
    } finally {
      reader.releaseLock();
    }
  }

  private headers(accept: string): Record<string, string> {
    return {
      Authorization: `Bearer ${this.apiKey}`,
      Accept: accept,
      "Content-Type": "application/json",
    };
  }

  private combinedSignal(signal?: AbortSignal): AbortSignal {
    const timeout = AbortSignal.timeout(this.timeoutMs);
    if (!signal) return timeout;
    return (AbortSignal as unknown as { any(signals: AbortSignal[]): AbortSignal }).any([signal, timeout]);
  }
}

async function toApiError(response: Response): Promise<RagApiError> {
  let code = "http_error";
  let message = "";
  try {
    const payload = (await response.json()) as { error?: string; message?: string };
    if (payload.error) code = payload.error;
    if (payload.message) message = payload.message;
  } catch {
    // keep the defaults
  }
  return new RagApiError(response.status, code, message);
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}