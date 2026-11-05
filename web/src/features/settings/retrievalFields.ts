import type { IndexingStrategy, RetrievalParamsV1, RetrievalOverride } from "../../api/types";

export type RetrievalFieldKey = keyof RetrievalParamsV1;
export type BoolValue = "" | "true" | "false";

type FieldBase = {
  key: RetrievalFieldKey;
  group: "recall" | "fusion" | "expansion" | "rerank" | "context";
  labelKey: string;
  unitKey?: string;
  min?: number;
  max?: number;
  step?: number;
  integer?: boolean;
  positive?: boolean;
};

export type NumericField = FieldBase & { kind: "number" };
export type BooleanField = FieldBase & { kind: "boolean" };

export const numericFields: NumericField[] = [
  { key: "top_k", group: "fusion", labelKey: "retrieval.top_k", unitKey: "retrieval.unitCount", min: 0, max: 200, integer: true, kind: "number" },
  { key: "embedding_top_k", group: "recall", labelKey: "retrieval.embedding_top_k", unitKey: "retrieval.unitCount", min: 0, max: 200, integer: true, kind: "number" },
  { key: "keyword_top_k", group: "recall", labelKey: "retrieval.keyword_top_k", unitKey: "retrieval.unitCount", min: 0, max: 200, integer: true, kind: "number" },
  { key: "vector_threshold", group: "recall", labelKey: "retrieval.vector_threshold", min: 0, max: 1, step: 0.01, kind: "number" },
  { key: "keyword_threshold", group: "recall", labelKey: "retrieval.keyword_threshold", min: 0, step: 0.01, kind: "number" },
  { key: "vector_ef_search", group: "recall", labelKey: "retrieval.vector_ef_search", min: 0, max: 1000, integer: true, kind: "number" },
  { key: "vector_oversample", group: "recall", labelKey: "retrieval.vector_oversample", min: 1, max: 20, integer: true, kind: "number" },
  { key: "rrf_k", group: "fusion", labelKey: "retrieval.rrf_k", unitKey: "retrieval.unitRrf", min: 1, max: 200, integer: true, kind: "number" },
  { key: "adjacent_expansion", group: "expansion", labelKey: "retrieval.adjacent_expansion", unitKey: "retrieval.unitChunks", min: 0, integer: true, kind: "number" },
  { key: "max_adjacent_chunks", group: "expansion", labelKey: "retrieval.max_adjacent_chunks", unitKey: "retrieval.unitChunks", min: 0, integer: true, kind: "number" },
  { key: "expansion_score_factor", group: "expansion", labelKey: "retrieval.expansion_score_factor", min: 0, max: 1, step: 0.05, kind: "number" },
  { key: "rerank_top_k", group: "rerank", labelKey: "retrieval.rerank_top_k", unitKey: "retrieval.unitCount", min: 0, max: 200, integer: true, kind: "number" },
  { key: "rerank_threshold", group: "rerank", labelKey: "retrieval.rerank_threshold", min: -10, max: 10, step: 0.01, kind: "number" },
  { key: "context_token_limit", group: "context", labelKey: "retrieval.context_token_limit", unitKey: "retrieval.unitTokens", min: 1, integer: true, positive: true, kind: "number" },
];

export const booleanFields: BooleanField[] = [
  { key: "vector_iterative_scan", group: "recall", labelKey: "retrieval.vector_iterative_scan", kind: "boolean" },
  { key: "rerank_enabled", group: "rerank", labelKey: "retrieval.rerank_enabled", kind: "boolean" },
];

export function fieldError(field: NumericField, value: number | null): string | undefined {
  if (value === null) return undefined;
  if (field.integer && !Number.isInteger(value)) return "integer";
  if (field.positive && value <= 0) return "positive";
  if (field.min !== undefined && value < field.min) return "range";
  if (field.max !== undefined && value > field.max) return "range";
  return undefined;
}

// Required capabilities are the query requirements for this knowledge base.
// Evidence is always required; a projection capability is only kept while its
// switch is enabled. Enabling wiki/graph does not silently make every query
// depend on that projection, which keeps capability failures isolated.
export function normalizedStrategy(strategy: IndexingStrategy): IndexingStrategy {
  const capabilities = new Set(strategy.required_capabilities.length > 0 ? strategy.required_capabilities : ["evidence"]);
  capabilities.add("evidence");
  if (!strategy.wiki_enabled) capabilities.delete("wiki");
  if (!strategy.graph_enabled) {
    capabilities.delete("graph");
    capabilities.delete("relation");
  }
  return { ...strategy, required_capabilities: Array.from(capabilities) };
}

// null and "" mean inherit, so they must never serialize as 0/false; an empty
// override serializes as {} and clears every knowledge-base override on PUT.
export function buildOverride(numbers: Record<string, number | null>, booleans: Record<string, BoolValue>): RetrievalOverride {
  const override: RetrievalOverride = {};
  for (const field of numericFields) {
    const value = numbers[field.key];
    if (value !== null && value !== undefined) override[field.key as keyof RetrievalParamsV1] = value as never;
  }
  for (const field of booleanFields) {
    const value = booleans[field.key];
    if (value === "true" || value === "false") override[field.key as keyof RetrievalParamsV1] = (value === "true") as never;
  }
  return override;
}
