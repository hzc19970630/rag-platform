import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, devHeaders } from "./client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("api client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
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
});
