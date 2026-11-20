import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, DEFAULT_REQUEST_TIMEOUT_MS, api, devHeaders } from "./client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function hangingFetch(): ReturnType<typeof vi.fn> {
  return vi.fn((_url: string, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
    init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
  }));
}

describe("api client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("only sends development identity headers in development", () => {
    expect(devHeaders(false)).toEqual({});
    expect(devHeaders(true)).toEqual({
      "X-Dev-User-ID": "dev-user",
      "X-Dev-Tenant-ID": "dev-tenant",
    });
  });

  it("refreshes the session once and retries the request", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ error: "unauthenticated" }, 401))
      .mockResolvedValueOnce(jsonResponse({ refreshed: true }))
      .mockResolvedValueOnce(jsonResponse({ value: "ok" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api<{ value: string }>("/me")).resolves.toEqual({ value: "ok" });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(String(fetchMock.mock.calls[1][0])).toContain("/auth/refresh");
    expect(String(fetchMock.mock.calls[2][0])).toContain("/me");
  });

  it("does not loop when the refresh itself fails", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ error: "unauthenticated" }, 401))
      .mockResolvedValueOnce(jsonResponse({ error: "expired" }, 401));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/me")).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("does not try to refresh a 401 from the refresh endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse({ error: "expired" }, 401));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/auth/refresh", { method: "POST" })).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("rejects with a timeout error once the default budget elapses", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", hangingFetch());

    const pending = api("/slow");
    const assertion = expect(pending).rejects.toMatchObject({ status: 0, code: "request_timeout" });
    await vi.advanceTimersByTimeAsync(DEFAULT_REQUEST_TIMEOUT_MS);

    await assertion;
  });

  it("reports a caller abort as AbortError rather than a timeout", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", hangingFetch());

    const controller = new AbortController();
    const pending = api("/slow", { signal: controller.signal });
    controller.abort();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

  it("skips the deadline when timeoutMs is 0 and forwards the caller signal", async () => {
    vi.useFakeTimers();
    const fetchMock = hangingFetch();
    vi.stubGlobal("fetch", fetchMock);

    const controller = new AbortController();
    const pending = api("/stream", { signal: controller.signal, timeoutMs: 0 });
    await vi.advanceTimersByTimeAsync(DEFAULT_REQUEST_TIMEOUT_MS * 4);

    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal);
    const assertion = expect(pending).rejects.toMatchObject({ name: "AbortError" });
    controller.abort();
    await assertion;
  });
});
