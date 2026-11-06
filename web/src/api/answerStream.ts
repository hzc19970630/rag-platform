import { ApiError, streamGet, streamPost } from "./client";
import type { StreamEvent } from "./types";

export type AnswerStreamPhase = "connecting" | "streaming" | "reconnecting";

export type AnswerStreamOptions = {
  conversationId: string;
  query: string;
  requestId: string;
  signal: AbortSignal;
  onEvent: (event: StreamEvent) => void;
  onStatus?: (phase: AnswerStreamPhase) => void;
  reconnectDelaysMs?: number[];
};

// The POST stream can end at the tail timeout without reaching a terminal
// event. Reconnect against the persisted replay endpoint from the last sequence
// the client saw; the server keeps polling until the answer is terminal.
const defaultReconnectDelays = [250, 500, 1000, 2000, 4000];

export async function streamAnswer(options: AnswerStreamOptions): Promise<void> {
  let lastSequence = 0;
  let complete = false;

  const handle = (event: StreamEvent) => {
    if (event.sequence <= lastSequence) return;
    lastSequence = event.sequence;
    if (event.type === "complete") complete = true;
    if (lastSequence === event.sequence && event.type !== "complete") {
      options.onStatus?.("streaming");
    }
    options.onEvent(event);
  };

  options.onStatus?.("connecting");
  try {
    await streamPost(
      `/sessions/${encodeURIComponent(options.conversationId)}/messages`,
      { query: options.query, requestId: options.requestId },
      handle,
      { signal: options.signal },
    );
  } catch (cause) {
    if (options.signal.aborted) throw cause;
    if (isClientError(cause)) throw cause;
  }
  if (complete) return;

  const delays = options.reconnectDelaysMs ?? defaultReconnectDelays;
  let lastError: unknown;
  for (const delay of delays) {
    if (options.signal.aborted) throw abortError();
    options.onStatus?.("reconnecting");
    await sleep(delay, options.signal);
    const after = lastSequence;
    const path = `/requests/${encodeURIComponent(options.requestId)}/events?stream=1&after=${after}`;
    try {
      await streamGet(path, handle, {
        signal: options.signal,
        headers: after > 0 ? { "Last-Event-ID": `${options.requestId}:${after}` } : {},
      });
    } catch (cause) {
      if (options.signal.aborted) throw cause;
      if (isClientError(cause)) throw cause;
      lastError = cause;
    }
    if (complete) return;
  }
  if (lastError) throw lastError;
  throw new Error("answer stream ended before a terminal event");
}

function isClientError(cause: unknown): boolean {
  return cause instanceof ApiError && cause.status >= 400 && cause.status < 500;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(abortError());
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(abortError());
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function abortError(): Error {
  return new DOMException("The answer stream was aborted", "AbortError");
}
