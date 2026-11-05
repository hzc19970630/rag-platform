import { describe, expect, it } from "vitest";
import {
  booleanFields,
  buildOverride,
  fieldError,
  normalizedStrategy,
  numericFields,
} from "./retrievalFields";
import { strategyFixture } from "../../test/fixtures";

function field(key: string) {
  const found = numericFields.find((item) => item.key === key);
  if (!found) throw new Error(`missing numeric field ${key}`);
  return found;
}

describe("retrieval field validation", () => {
  it("treats an empty value as inherited rather than invalid", () => {
    expect(fieldError(field("top_k"), null)).toBeUndefined();
  });

  it("rejects fractional integers, non-positive values and out-of-range values", () => {
    expect(fieldError(field("top_k"), 1.5)).toBe("integer");
    expect(fieldError(field("context_token_limit"), 0)).toBe("positive");
    expect(fieldError(field("vector_threshold"), 1.2)).toBe("range");
    expect(fieldError(field("rerank_threshold"), -11)).toBe("range");
    expect(fieldError(field("top_k"), 10)).toBeUndefined();
  });

  it("keeps ef_search at zero valid and rejects an out-of-range oversample", () => {
    expect(fieldError(field("vector_ef_search"), 0)).toBeUndefined();
    expect(fieldError(field("vector_ef_search"), 1001)).toBe("range");
    expect(fieldError(field("vector_oversample"), 0)).toBe("range");
    expect(fieldError(field("vector_oversample"), 21)).toBe("range");
    expect(fieldError(field("vector_oversample"), 4)).toBeUndefined();
  });
});

describe("retrieval override serialization", () => {
  it("omits inherited values but keeps an explicit zero", () => {
    const numbers = Object.fromEntries(numericFields.map((item) => [item.key, null])) as Record<string, number | null>;
    numbers.top_k = 0;
    const booleans = Object.fromEntries(booleanFields.map((item) => [item.key, ""])) as Record<string, "" | "true" | "false">;
    expect(buildOverride(numbers, booleans)).toEqual({ top_k: 0 });
  });

  it("returns an empty object when every field is inherited so PUT clears overrides", () => {
    const numbers = Object.fromEntries(numericFields.map((item) => [item.key, null])) as Record<string, number | null>;
    const booleans = Object.fromEntries(booleanFields.map((item) => [item.key, ""])) as Record<string, "" | "true" | "false">;
    expect(buildOverride(numbers, booleans)).toEqual({});
  });

  it("includes a boolean only when it is explicitly overridden", () => {
    const numbers = Object.fromEntries(numericFields.map((item) => [item.key, null])) as Record<string, number | null>;
    expect(buildOverride(numbers, { rerank_enabled: "false" })).toEqual({ rerank_enabled: false });
    expect(buildOverride(numbers, { rerank_enabled: "true" })).toEqual({ rerank_enabled: true });
  });
});

describe("indexing strategy normalization", () => {
  it("always requires evidence and prunes disabled projection capabilities", () => {
    const strategy = normalizedStrategy(strategyFixture({ required_capabilities: ["evidence", "wiki", "graph", "relation"] }));
    expect(strategy.required_capabilities.sort()).toEqual(["evidence"]);
  });

  it("keeps wiki and graph requirements while those switches are enabled", () => {
    const strategy = normalizedStrategy(strategyFixture({
      wiki_enabled: true,
      graph_enabled: true,
      required_capabilities: ["evidence", "wiki", "graph"],
    }));
    expect(strategy.required_capabilities.sort()).toEqual(["evidence", "graph", "wiki"]);
  });
});