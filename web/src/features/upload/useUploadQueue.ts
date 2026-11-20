import { useCallback, useEffect, useRef, useState } from "react";
import {
  abortUploadSession,
  completeUploadSession,
  createUploadSession,
  getUploadSession,
  refreshUploadPartURLs,
  uploadSessionPart,
  type UploadSession,
} from "../../api/client";

export type UploadJobStatus = "queued" | "running" | "done" | "failed" | "canceled";

export type UploadJob = {
  id: string;
  file: File;
  name: string;
  size: number;
  status: UploadJobStatus;
  progress: number;
  uploadedBytes: number;
  error: string;
  uploadId: string;
};

const CONCURRENCY = 2;
const RESUME_PREFIX = "rag-upload-resume:";

export function resumeKey(knowledgeBaseId: string, file: File): string {
  return `${RESUME_PREFIX}${knowledgeBaseId}:${file.name}:${file.size}:${file.lastModified}`;
}

export function createUploadJobs(files: File[]): UploadJob[] {
  return files.map((file) => ({
    id: crypto.randomUUID(),
    file,
    name: file.name,
    size: file.size,
    status: "queued" as const,
    progress: 0,
    uploadedBytes: 0,
    error: "",
    uploadId: "",
  }));
}

export function summarizeJobs(jobs: UploadJob[]): { done: number; failed: number; pending: number; total: number } {
  let done = 0;
  let failed = 0;
  let pending = 0;
  for (const job of jobs) {
    if (job.status === "done") done += 1;
    else if (job.status === "failed" || job.status === "canceled") failed += 1;
    else pending += 1;
  }
  return { done, failed, pending, total: jobs.length };
}

function partSlice(file: File, partSize: number, partNumber: number): Blob {
  const start = partSize * (partNumber - 1);
  return file.slice(start, Math.min(start + partSize, file.size));
}

function errorMessage(cause: unknown): string {
  if (cause instanceof Error) return cause.message;
  return String(cause);
}

export type UploadQueue = ReturnType<typeof useUploadQueue>;

export function useUploadQueue(options: { knowledgeBaseId: string; folderId: string; onUploaded: () => Promise<void> | void }) {
  const { knowledgeBaseId, folderId, onUploaded } = options;
  const [jobs, setJobs] = useState<UploadJob[]>([]);
  const [open, setOpen] = useState(false);
  // A finished job frees a slot after its state update, so the scheduler needs
  // its own trigger; waiting only on `jobs` leaves later batches queued forever.
  const [scheduleTick, setScheduleTick] = useState(0);
  const controllers = useRef(new Map<string, AbortController>());
  const running = useRef(new Set<string>());
  const started = useRef(new Set<string>());
  const callbacks = useRef({ knowledgeBaseId, folderId, onUploaded });
  callbacks.current = { knowledgeBaseId, folderId, onUploaded };

  const patch = useCallback((jobId: string, changes: Partial<UploadJob>) => {
    setJobs((current) => current.map((job) => (job.id === jobId ? { ...job, ...changes } : job)));
  }, []);

  const runJob = useCallback(async (job: UploadJob) => {
    const { knowledgeBaseId: kbId, folderId: currentFolder, onUploaded: uploaded } = callbacks.current;
    const controller = new AbortController();
    controllers.current.set(job.id, controller);
    running.current.add(job.id);
    patch(job.id, { status: "running", progress: 0, uploadedBytes: 0, error: "" });
    let session: UploadSession | undefined;
    try {
      const storedId = job.uploadId || localStorage.getItem(resumeKey(kbId, job.file)) || "";
      localStorage.removeItem(resumeKey(kbId, job.file));
      if (storedId) {
        try {
          const resumed = await getUploadSession(storedId);
          if (resumed.status === "completed") {
            patch(job.id, { status: "done", progress: 1, uploadedBytes: job.size });
            await uploaded();
            return;
          }
          if (["pending", "uploading", "completing"].includes(resumed.status)) session = resumed;
        } catch {
          session = undefined;
        }
      }
      if (!session) {
        session = await createUploadSession(kbId, {
          filename: job.file.name,
          sizeBytes: job.file.size,
          mimeType: job.file.type,
          folderId: currentFolder || undefined,
        }, `web-${crypto.randomUUID()}`);
      }
      localStorage.setItem(resumeKey(kbId, job.file), session.id);
      patch(job.id, { uploadId: session.id });
      let parts = session.parts ?? (await getUploadSession(session.id)).parts ?? [];
      if (session.transport === "direct" && parts.some((part) => !part.uploaded && !part.url)) {
        session = await refreshUploadPartURLs(session.id);
        parts = session.parts ?? [];
      }
      const uploadedBefore = parts.filter((part) => part.uploaded).reduce((sum, part) => sum + part.sizeBytes, 0);
      let uploadedBytes = uploadedBefore;
      patch(job.id, { uploadedBytes, progress: job.size > 0 ? uploadedBytes / job.size : 0 });
      for (const part of parts) {
        if (part.uploaded) continue;
        if (controller.signal.aborted) throw new DOMException("upload aborted", "AbortError");
        const blob = partSlice(job.file, session.partSize, part.number);
        const send = (url?: string) => uploadSessionPart(session!, part, url, blob, (loaded) => {
          patch(job.id, {
            uploadedBytes: uploadedBytes + loaded,
            progress: job.size > 0 ? (uploadedBytes + loaded) / job.size : 0,
          });
        }, controller.signal);
        try {
          await send(part.url);
        } catch (cause) {
          if (controller.signal.aborted) throw cause;
          // Presigned URLs expire; re-sign once and retry the same part.
          session = await refreshUploadPartURLs(session.id);
          const refreshed = session.parts?.find((candidate) => candidate.number === part.number);
          await send(refreshed?.url);
        }
        uploadedBytes += part.sizeBytes;
        patch(job.id, { uploadedBytes, progress: job.size > 0 ? uploadedBytes / job.size : 0 });
      }
      await completeUploadSession(session.id);
      localStorage.removeItem(resumeKey(kbId, job.file));
      patch(job.id, { status: "done", progress: 1, uploadedBytes: job.size });
      await uploaded();
    } catch (cause) {
      if (controller.signal.aborted || (cause instanceof DOMException && cause.name === "AbortError")) {
        patch(job.id, { status: "canceled", error: "" });
      } else {
        patch(job.id, { status: "failed", error: errorMessage(cause) });
      }
    } finally {
      running.current.delete(job.id);
      controllers.current.delete(job.id);
      setScheduleTick((tick) => tick + 1);
    }
  }, [patch]);

  useEffect(() => {
    if (running.current.size >= CONCURRENCY) return;
    const next = jobs.find((job) => job.status === "queued" && !started.current.has(job.id));
    if (!next) return;
    started.current.add(next.id);
    void runJob(next);
  }, [jobs, runJob, scheduleTick]);

  const enqueue = useCallback((files: File[]) => {
    if (files.length === 0) return;
    const next = createUploadJobs(files);
    setJobs((current) => [...current, ...next]);
    setOpen(true);
  }, []);

  const cancel = useCallback((jobId: string) => {
    controllers.current.get(jobId)?.abort();
    setJobs((current) => {
      const job = current.find((candidate) => candidate.id === jobId);
      if (job?.uploadId) void abortUploadSession(job.uploadId).catch(() => undefined);
      return current.map((candidate) => candidate.id === jobId
        ? { ...candidate, status: candidate.status === "done" ? candidate.status : "canceled" }
        : candidate);
    });
  }, []);

  const retry = useCallback((jobId: string) => {
    started.current.delete(jobId);
    setJobs((current) => current.map((job) => (job.id === jobId && job.status !== "running" && job.status !== "done"
      ? { ...job, status: "queued", progress: 0, uploadedBytes: 0, error: "" }
      : job)));
  }, []);

  const clearFinished = useCallback(() => {
    setJobs((current) => current.filter((job) => job.status === "running" || job.status === "queued"));
  }, []);

  return { jobs, open, setOpen, enqueue, cancel, retry, clearFinished, summary: summarizeJobs(jobs) };
}
