import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createUploadJobs, resumeKey, summarizeJobs, useUploadQueue } from "./useUploadQueue";

vi.mock("../../api/client", () => ({
  createUploadSession: vi.fn(async (_kb: string, input: { filename: string; sizeBytes: number }) => ({
    id: `upl-${input.filename}`,
    knowledgeBaseId: "kb-1",
    documentId: `doc-${input.filename}`,
    filename: input.filename,
    sizeBytes: input.sizeBytes,
    partSize: input.sizeBytes,
    partCount: 1,
    transport: "proxy" as const,
    status: "pending",
    parts: [{ number: 1, sizeBytes: input.sizeBytes, uploaded: false }],
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    createdAt: new Date().toISOString(),
  })),
  getUploadSession: vi.fn(),
  refreshUploadPartURLs: vi.fn(),
  completeUploadSession: vi.fn(async () => ({})),
  abortUploadSession: vi.fn(async () => undefined),
  uploadSessionPart: vi.fn(async (_session: unknown, _part: unknown, _url: unknown, blob: Blob, onProgress?: (loaded: number, total: number) => void) => {
    await new Promise((resolve) => setTimeout(resolve, 5));
    onProgress?.(blob.size, blob.size);
  }),
}));

function makeFile(name: string, size: number, lastModified = 1_700_000_000_000): File {
  return new File([new Uint8Array(size)], name, { type: "text/plain", lastModified });
}

describe("upload queue helpers", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("creates queued jobs for a batch", () => {
    const jobs = createUploadJobs([makeFile("a.txt", 4), makeFile("b.txt", 6)]);
    expect(jobs).toHaveLength(2);
    expect(jobs.every((job) => job.status === "queued" && job.progress === 0 && job.error === "")).toBe(true);
    expect(jobs[0].name).toBe("a.txt");
    expect(jobs[1].size).toBe(6);
    expect(new Set(jobs.map((job) => job.id)).size).toBe(2);
  });

  it("summarizes done, failed and pending jobs", () => {
    const jobs = createUploadJobs([makeFile("a.txt", 1), makeFile("b.txt", 1), makeFile("c.txt", 1), makeFile("d.txt", 1)]);
    jobs[0].status = "done";
    jobs[1].status = "failed";
    jobs[2].status = "canceled";
    expect(summarizeJobs(jobs)).toEqual({ done: 1, failed: 2, pending: 1, total: 4 });
    expect(summarizeJobs([])).toEqual({ done: 0, failed: 0, pending: 0, total: 0 });
  });

  it("keys resume entries by knowledge base, name, size and modification time", () => {
    const first = resumeKey("kb-1", makeFile("a.txt", 4));
    expect(first).toContain("kb-1:a.txt:4:");
    expect(resumeKey("kb-2", makeFile("a.txt", 4))).not.toBe(first);
    expect(resumeKey("kb-1", makeFile("a.txt", 5))).not.toBe(first);
    expect(resumeKey("kb-1", makeFile("a.txt", 4, 1_700_000_000_001))).not.toBe(first);
  });

  it("keeps scheduling after the first concurrency wave finishes", async () => {
    const onUploaded = vi.fn();
    const { result } = renderHook(() => useUploadQueue({ knowledgeBaseId: "kb-1", folderId: "", onUploaded }));
    await act(async () => {
      result.current.enqueue([makeFile("a.txt", 4), makeFile("b.txt", 4), makeFile("c.txt", 4)]);
    });
    await waitFor(() => expect(result.current.summary.done).toBe(3), { timeout: 3000 });
    expect(result.current.summary.failed).toBe(0);
    expect(onUploaded).toHaveBeenCalledTimes(3);
  });
});
