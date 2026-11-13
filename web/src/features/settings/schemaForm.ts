import type { ConfigFieldDefinition, ConfigFieldState } from "../../api/types";

// The workbench renders configuration fields from the server schema. These
// helpers are the only place that knows how a schema kind maps to a form value,
// so adding a parameter never requires a new hand-written field row.

// SchemaDomain is the subset both the tenant configuration and the knowledge-base
// configuration response expose.
export type SchemaDomain = {
  schema: ConfigFieldDefinition[];
  fields?: Record<string, ConfigFieldState>;
};

export type ConfigValue = number | boolean | string[] | null;

export function initialValues(domain: SchemaDomain, override: Record<string, unknown>): Record<string, ConfigValue> {
  const values: Record<string, ConfigValue> = {};
  for (const field of domain.schema) {
    const current = override[field.key];
    if (field.kind === "bool") {
      values[field.key] = typeof current === "boolean" ? current : null;
    } else if (field.kind === "string_list") {
      values[field.key] = Array.isArray(current) ? current.filter((item): item is string => typeof item === "string") : null;
    } else {
      values[field.key] = typeof current === "number" ? current : null;
    }
  }
  return values;
}

export function buildOverride(domain: SchemaDomain, values: Record<string, ConfigValue>): Record<string, unknown> {
  const override: Record<string, unknown> = {};
  for (const field of domain.schema) {
    const value = values[field.key];
    if (value === null || value === undefined) continue;
    override[field.key] = value;
  }
  return override;
}

export function fieldError(field: ConfigFieldDefinition, value: ConfigValue): "integer" | "positive" | "range" | "empty" | undefined {
  if (value === null || value === undefined) return undefined;
  if (field.kind === "string_list") {
    if (!Array.isArray(value) || value.some((item) => item.trim() === "")) return "empty";
    return undefined;
  }
  if (typeof value !== "number") return undefined;
  if (field.kind === "int" && !Number.isInteger(value)) return "integer";
  if (field.max !== undefined) {
    const min = field.min ?? 0;
    if (value < min || value > field.max) return "range";
    return undefined;
  }
  if (field.min !== undefined) {
    if (field.min === 0 && value < 0) return "range";
    if (field.min === 1 && field.kind === "int" && value <= 0) return "positive";
    if (value < field.min) return "range";
  }
  return undefined;
}

export function normalizeOverride(domain: SchemaDomain, override: Record<string, unknown>): string {
  return JSON.stringify(buildOverride(domain, initialValues(domain, override)));
}

export function isDirty(domain: SchemaDomain, override: Record<string, unknown>, values: Record<string, ConfigValue>): boolean {
  return JSON.stringify(buildOverride(domain, values)) !== normalizeOverride(domain, override);
}

export function isOverridden(override: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(override, key);
}

export function displayValue(value: unknown): string {
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return String(value);
  if (typeof value === "number") return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(4)));
  return String(value ?? "");
}

export function groupLabelKey(group: string): string {
  switch (group) {
    case "recall":
      return "retrieval.groupRecall";
    case "fusion":
      return "retrieval.groupFusion";
    case "expansion":
      return "retrieval.groupExpansion";
    case "rerank":
      return "retrieval.groupRerank";
    case "context":
      return "retrieval.groupContext";
    case "chunking":
      return "configuration.groupChunking";
    default:
      return group;
  }
}

export function sourceLabelKey(source: string): string {
  switch (source) {
    case "deployment":
      return "configuration.sourceDeployment";
    case "tenant":
      return "configuration.sourceTenant";
    case "knowledge_base":
      return "configuration.sourceKnowledgeBase";
    case "session":
      return "configuration.sourceSession";
    default:
      return "configuration.sourceDefault";
  }
}

export function applyModeLabelKey(mode: string): string {
  return mode === "next_ingest" ? "configuration.applyNextIngest" : "configuration.applyNextRetrieval";
}
