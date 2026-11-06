import { afterEach, describe, expect, it, vi } from "vitest";
import { streamAnswer, type AnswerStreamPhase } from "./answerStream";
import type { StreamEvent } from "./types";

function eventBlock(sequence: number, type: string, data: Record<string, unknown>): string {
  const payload = { event_id: `req-1:${sequence}`, request_id: "req-1", sequence, type, data };
  return `id: req-1:${sequence}\nevent: ${type}\ndata: ${JSON.stringify(payload)}\n\n`;
}

function sseResponse(blocks: string[]): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const block of blocks) controller.enqueue(new TextEncoder().encode(block));
      controller.close();
    },
  });
  return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

describe("streamAnswer", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("resumes from the last sequence after the POST stream ends early", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        sseResponse([
          eventBlock(1, "status", { state: "RETRIEVING" }),
          eventBlock(2, "answer", { content: "partial", status: "COMPLETED" }),
        ]),
      )
      .mockResolvedValueOnce(
        sseResponse([
          eventBlock(2, "answer", { content: "partial", status: "COMPLETED" }),
          eventBlock(3, "complete", { status: "COMPLETED" }),
        ]),
      );
    vi.stubGlobal("fetch", fetchMock);

    const events: StreamEvent[] = [];
    const phases: AnswerStreamPhase[] = [];
    await streamAnswer({
      conversationId: "conv-1",
      query: "question",
      requestId: "req-1",
      signal: new AbortController().signal,
      reconnectDelaysMs: [0, 0],
      onEvent: (event) => events.push(event),
      onStatus: (phase) => phases.push(phase),
    });

    expect(events.map((event) => event.sequence)).toEqual([1, 2, 3]);
    expect(phases).toContain("reconnecting");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const resumeURL = String(fetchMock.mock.calls[1][0]);
    expect(resumeURL).toContain("/requests/req-1/events?stream=1&after=2");
    const resumeInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect((resumeInit.headers as Record<string, string>)["Last-Event-ID"]).toBe("req-1:2");
  });

  it("stops without reconnecting when the POST stream completes", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(
      sseResponse([
        eventBlock(1, "answer", { content: "answer", status: "COMPLETED" }),
        eventBlock(2, "complete", { status: "COMPLETED" }),
      ]),
    );
    vi.stubGlobal("fetch", fetchMock);

    await streamAnswer({
      conversationId: "conv-1",
      query: "question",
      requestId: "req-1",
      signal: new AbortController().signal,
      onEvent: () => undefined,
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("propagates an aborted stream without reconnecting", async () => {
    const controller = new AbortController();
    controller.abort();
    const fetchMock = vi.fn().mockImplementation((_url: string, init: RequestInit) => {
      if (init.signal?.aborted) {
        return Promise.reject(new DOMException("aborted", "AbortError"));
      }
      return Promise.resolve(sseResponse([]));
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      streamAnswer({
        conversationId: "conv-1",
        query: "question",
        requestId: "req-1",
        signal: controller.signal,
        onEvent: () => undefined,
      }),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("ignores replayed events at or below the last sequence", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(sseResponse([eventBlock(1, "status", { state: "RECEIVED" })]))
      .mockResolvedValueOnce(
        sseResponse([
          eventBlock(1, "status", { state: "RECEIVED" }),
          eventBlock(2, "complete", { status: "COMPLETED" }),
        ]),
      );
    vi.stubGlobal("fetch", fetchMock);

    const sequences: number[] = [];
    await streamAnswer({
      conversationId: "conv-1",
      query: "question",
      requestId: "req-1",
      signal: new AbortController().signal,
      reconnectDelaysMs: [0],
      onEvent: (event) => sequences.push(event.sequence),
    });

    expect(sequences).toEqual([1, 2]);
  });
});
