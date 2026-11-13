import type { TFunction } from "i18next";

// One registry for every enum the workbench renders. Components must not map
// enum values to i18n keys themselves: adding a backend state or capability is
// a change to this file plus the two translation blocks, nothing else.

export type EnumGroup = "status" | "role" | "capability" | "source" | "connector_type" | "provider_kind" | "audit_action" | "audit_resource" | "api_key_status" | "queue";
export type EnumTone = "success" | "warning" | "error" | "default";

export const STATUS_VALUES = [
  // Document, version and ingestion pipeline.
  "RECEIVED", "STORED", "PARSING", "PARSED", "NORMALIZING", "NORMALIZED",
  "CHUNKING", "CHUNKED", "EMBEDDING", "EMBEDDED", "INDEXING", "EVIDENCE_READY",
  "PARENT", "STALE", "FAILED", "CANCELLED",
  // Stage, inbox, outbox and dead-letter.
  "PENDING", "PROCESSING", "RUNNING", "SUCCEEDED", "RETRYING", "RESOLVED", "DISCARDED", "DEAD",
  // Wiki and relation projections.
  "WIKI_PENDING", "WIKI_GENERATING", "WIKI_READY", "FAILED_WIKI", "RETRY_WIKI",
  "RELATION_PENDING", "RELATION_EXTRACTING", "RELATION_READY", "FAILED_RELATION", "RETRY_RELATION",
  // Review, wiki page, knowledge index and graph snapshot.
  "APPROVED", "REJECTED", "DRAFT", "PUBLISHED", "REVIEW_REQUIRED", "ARCHIVED", "ACTIVE",
  "BUILDING", "PENDING_REVIEW", "SUPERSEDED",
  // Connector, knowledge join and durable answers.
  "PARTIAL", "TOMBSTONED", "KNOWLEDGE_READY", "KNOWLEDGE_PARTIAL", "QUEUED", "COMPLETED",
  // Online answer lifecycle.
  "AUTHORIZED", "PLANNED", "RETRIEVING", "GROUNDED", "GENERATING",
  "CITATION_VALIDATING", "DELIVERING", "REFUSED", "TIMEOUT",
  // Configuration health statuses (lower-case).
  "ok", "warning", "error", "unknown", "skipped",
] as const;

export const ROLE_VALUES = ["owner", "admin", "editor", "member", "viewer"] as const;

export const CAPABILITY_VALUES = [
  "evidence", "wiki", "graph", "relation", "chat", "embedding", "rerank",
  "knowledge_base:read", "knowledge_base:write", "knowledge_base:delete",
  "knowledge_base:manage_members", "tenant:admin",
] as const;

export const SOURCE_VALUES = ["env", "yaml", "local", "remote", "lexical", "none"] as const;
export const CONNECTOR_TYPE_VALUES = ["http_manifest"] as const;
export const PROVIDER_KIND_VALUES = ["openai_compatible", "ollama"] as const;

export const API_KEY_STATUS_VALUES = ["active", "expired", "revoked"] as const;

export const QUEUE_VALUES = ["ingestion", "interactive", "projection", "maintenance"] as const;

export const AUDIT_ACTION_VALUES = [
  "create", "delete", "discard", "login", "manage_members", "purge", "retry", "revoke",
  "rollback", "update_tenant_config", "update_tenant_quota", "write",
] as const;

export const AUDIT_RESOURCE_VALUES = [
  "api_key", "connector", "dead_letter", "document", "graph_run", "graph_snapshot",
  "indexing_strategy", "path", "retrieval_config", "session", "tenant", "tenant_config", "wiki_page",
] as const;

export type StatusValue = (typeof STATUS_VALUES)[number];

const STATUS_TONES: Record<string, EnumTone> = {
  RECEIVED: "default",
  STORED: "default",
  PARSING: "warning",
  PARSED: "default",
  NORMALIZING: "warning",
  NORMALIZED: "default",
  CHUNKING: "warning",
  CHUNKED: "default",
  EMBEDDING: "warning",
  EMBEDDED: "default",
  INDEXING: "warning",
  EVIDENCE_READY: "success",
  PARENT: "default",
  STALE: "error",
  FAILED: "error",
  CANCELLED: "error",
  PENDING: "warning",
  PROCESSING: "warning",
  RUNNING: "warning",
  SUCCEEDED: "success",
  RETRYING: "warning",
  RESOLVED: "success",
  DISCARDED: "error",
  DEAD: "error",
  WIKI_PENDING: "warning",
  WIKI_GENERATING: "warning",
  WIKI_READY: "success",
  FAILED_WIKI: "error",
  RETRY_WIKI: "warning",
  RELATION_PENDING: "warning",
  RELATION_EXTRACTING: "warning",
  RELATION_READY: "success",
  FAILED_RELATION: "error",
  RETRY_RELATION: "warning",
  APPROVED: "success",
  REJECTED: "error",
  DRAFT: "default",
  PUBLISHED: "success",
  REVIEW_REQUIRED: "warning",
  ARCHIVED: "default",
  ACTIVE: "success",
  BUILDING: "warning",
  PENDING_REVIEW: "warning",
  SUPERSEDED: "error",
  PARTIAL: "warning",
  TOMBSTONED: "error",
  KNOWLEDGE_READY: "success",
  KNOWLEDGE_PARTIAL: "warning",
  QUEUED: "warning",
  COMPLETED: "success",
  AUTHORIZED: "default",
  PLANNED: "default",
  RETRIEVING: "warning",
  GROUNDED: "success",
  GENERATING: "warning",
  CITATION_VALIDATING: "warning",
  DELIVERING: "warning",
  REFUSED: "error",
  TIMEOUT: "error",
  ok: "success",
  warning: "warning",
  error: "error",
  unknown: "default",
  skipped: "default",
};

export function enumLabelKey(group: EnumGroup, value: string): string {
  if (group === "capability") {
    return `enum.capability.${value.replace(/[:.]/g, "_")}`;
  }
  return `enum.${group}.${value}`;
}

export function enumLabel(t: TFunction, group: EnumGroup, value: string): string {
  return t(enumLabelKey(group, value), { defaultValue: value });
}

export function statusLabel(t: TFunction, status: string): string {
  return enumLabel(t, "status", status);
}

export function roleLabel(t: TFunction, role: string): string {
  return enumLabel(t, "role", role);
}

export function capabilityLabel(t: TFunction, capability: string): string {
  return enumLabel(t, "capability", capability);
}

export function sourceLabel(t: TFunction, source: string): string {
  return enumLabel(t, "source", source);
}

export function connectorTypeLabel(t: TFunction, connectorType: string): string {
  return enumLabel(t, "connector_type", connectorType);
}

export function providerKindLabel(t: TFunction, kind: string): string {
  return enumLabel(t, "provider_kind", kind);
}

export function apiKeyStatusLabel(t: TFunction, status: string): string {
  return enumLabel(t, "api_key_status", status);
}

export function queueLabel(t: TFunction, queue: string): string {
  return enumLabel(t, "queue", queue);
}

export function toneColor(tone: EnumTone): string {
  switch (tone) {
    case "success":
      return "green";
    case "warning":
      return "orange";
    case "error":
      return "red";
    default:
      return "default";
  }
}

export function auditActionLabel(t: TFunction, action: string): string {
  return enumLabel(t, "audit_action", action);
}

export function auditResourceLabel(t: TFunction, resource: string): string {
  return enumLabel(t, "audit_resource", resource);
}

export function statusTone(status: string): EnumTone {
  return STATUS_TONES[status] ?? "default";
}
