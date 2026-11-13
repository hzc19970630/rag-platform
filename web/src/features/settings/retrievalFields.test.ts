import { describe, expect, it } from "vitest";
import { normalizedStrategy } from "./retrievalFields";
import { buildOverride, fieldError, initialValues, isDirty } from "./schemaForm";
import { configDomainFixture, strategyFixture } from "../../test/fixtures";

function schemaField(key: string) {
  const found = configDomainFixture().schema.find((item) => item.key === key);
  if (!found) throw new Error(`missing schema field ${key}`);
  return found;
}

describe("schema field validation", () => {
  it("treats an empty value as inherited rather than invalid", () => {
    expect(fieldError(schemaField("top_k"), null)).toBeUndefined();
  });

  it("rejects fractional integers, non-positive values and out-of-range values", () => {
    expect(fieldError(schemaField("top_k"), 1.5)).toBe("integer");
    expect(fieldError(schemaField("context_token_limit"), 0)).toBe("positive");
    expect(fieldError(schemaField("vector_threshold"), 1.2)).toBe("range");
    expect(fieldError(schemaField("rerank_threshold"), -11)).toBe("range");
    expect(fieldError(schemaField("top_k"), 10)).toBeUndefined();
  });

  it("keeps ef_search at zero valid and rejects an out-of-range oversample", () => {
    expect(fieldError(schemaField("vector_ef_search"), 0)).toBeUndefined();
    expect(fieldError(schemaField("vector_ef_search"), 1001)).toBe("range");
    expect(fieldError(schemaField("vector_oversample"), 0)).toBe("range");
    expect(fieldError(schemaField("vector_oversample"), 21)).toBe("range");
    expect(fieldError(schemaField("vector_oversample"), 4)).toBeUndefined();
  });
});

describe("schema-driven override serialization", () => {
  it("omits inherited values but keeps an explicit zero", () => {
    const domain = configDomainFixture();
    const values = initialValues(domain, {});
    values.top_k = 0;
    expect(buildOverride(domain, values)).toEqual({ top_k: 0 });
  });

  it("returns an empty object when every field is inherited so PUT clears overrides", () => {
    const domain = configDomainFixture();
    expect(buildOverride(domain, initialValues(domain, {}))).toEqual({});
  });

  it("includes a boolean and a string list only when explicitly overridden", () => {
    const domain = configDomainFixture();
    domain.schema = [...domain.schema, {
      key: "split_markers",
      domain: "chunking",
      group: "chunking",
      kind: "string_list",
      default: ["\n\n"],
      applyMode: "next_ingest",
      sensitivity: "normal",
      labelKey: "chunking.splitMarkers",
    }];
    const values = initialValues(domain, {});
    values.rerank_enabled = false;
    values.split_markers = ["\n\n"];
    expect(buildOverride(domain, values)).toEqual({ rerank_enabled: false, split_markers: ["\n\n"] });
  });

  it("detects dirty state against the stored override", () => {
    const domain = configDomainFixture();
    const values = initialValues(domain, { top_k: 5 });
    expect(isDirty(domain, { top_k: 5 }, values)).toBe(false);
    values.top_k = 6;
    expect(isDirty(domain, { top_k: 5 }, values)).toBe(true);
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
