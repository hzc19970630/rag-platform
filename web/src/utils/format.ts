import type { IndexingStrategy } from "../api/types";

export function formatTime(value?: string): string {
  if (!value) return "-";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export function parseIndexingStrategy(raw?: string): IndexingStrategy | undefined {
  if (!raw) return undefined;
  try {
    const parsed = JSON.parse(raw) as Partial<IndexingStrategy>;
    return {
      vector_enabled: Boolean(parsed.vector_enabled),
      keyword_enabled: Boolean(parsed.keyword_enabled),
      wiki_enabled: Boolean(parsed.wiki_enabled),
      graph_enabled: Boolean(parsed.graph_enabled),
      required_capabilities: Array.isArray(parsed.required_capabilities) ? parsed.required_capabilities : [],
    };
  } catch {
    return undefined;
  }
}
