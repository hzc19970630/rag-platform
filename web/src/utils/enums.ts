import type { TFunction } from "i18next";

// One registry for every enum the workbench renders. Components must not map
// enum values to i18n keys themselves: adding a backend state or capability is
// a change to this file plus the two translation blocks, nothing else.

export type EnumGroup = "status" | "role" | "capability" | "source" | "connector_type" | "provider_kind" | "audit_action" | "audit_resource" | "api_key_status" | "queue" | "embedding_migration_status" | "table_storage" | "table_parse_status";
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
  "read", "create", "update", "delete", "discard", "login", "manage_members", "purge", "retry", "revoke",
  "rollback", "update_tenant_config", "update_tenant_quota", "write", "cancel",
  "add_member", "update_member", "remove_member",
  "model_provider_created", "model_provider_updated", "model_provider_deleted", "model_provider_tested",
  "wiki_retry", "graph_retry", "wiki_cancel", "graph_cancel",
  "embedding_migration_started", "embedding_migration_cutover",
  "embedding_migration_rolled_back", "embedding_migration_cancelled", "embedding_migration_finalized",
] as const;

export const AUDIT_RESOURCE_VALUES = [
  "api_key", "connector", "dead_letter", "document", "folder", "graph_run", "graph_snapshot",
  "indexing_strategy", "path", "retrieval_config", "session", "tag", "tenant", "tenant_config", "wiki_page",
  "embedding_migration", "membership", "model_binding", "model_provider", "pipeline", "upload_session",
] as const;

export const EMBEDDING_MIGRATION_STATUS_VALUES = [
  "pending", "building_index", "reembedding", "verifying", "ready",
  "cutover", "finalized", "rolled_back", "cancelled", "failed",
] as const;

export const TABLE_STORAGE_VALUES = ["full", "sampled", "metadata"] as const;
export const TABLE_PARSE_STATUS_VALUES = [
  "parsed", "truncated", "sampled", "metadata", "skipped", "failed", "partial",
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

const EMBEDDING_MIGRATION_TONES: Record<string, EnumTone> = {
  pending: "warning",
  building_index: "warning",
  reembedding: "warning",
  verifying: "warning",
  ready: "success",
  cutover: "success",
  finalized: "default",
  rolled_back: "warning",
  cancelled: "default",
  failed: "error",
};

export function embeddingMigrationStatusLabel(t: TFunction, status: string): string {
  return enumLabel(t, "embedding_migration_status", status);
}

export function embeddingMigrationStatusTone(status: string): EnumTone {
  return EMBEDDING_MIGRATION_TONES[status] ?? "default";
}

export function tableStorageLabel(t: TFunction, value: string): string {
  return enumLabel(t, "table_storage", value);
}

export function tableParseStatusLabel(t: TFunction, value: string): string {
  return enumLabel(t, "table_parse_status", value);
}

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

const DOCUMENT_STATUS_GROUPS: Record<string, string> = {
  RECEIVED: "pending",
  STORED: "pending",
  PENDING: "pending",
  QUEUED: "pending",
  PARSING: "processing",
  PARSED: "processing",
  NORMALIZING: "processing",
  NORMALIZED: "processing",
  CHUNKING: "processing",
  CHUNKED: "processing",
  EMBEDDING: "processing",
  EMBEDDED: "processing",
  INDEXING: "processing",
  PROCESSING: "processing",
  RUNNING: "processing",
  RETRYING: "processing",
  EVIDENCE_READY: "ready",
  ACTIVE: "ready",
  KNOWLEDGE_READY: "ready",
  SUCCEEDED: "ready",
  COMPLETED: "ready",
  FAILED: "failed",
  FAILED_WIKI: "failed",
  FAILED_RELATION: "failed",
  DEAD: "failed",
  CANCELLED: "cancelled",
  STALE: "stale",
};

export function documentStatusGroup(status: string): string {
  return DOCUMENT_STATUS_GROUPS[status] ?? "";
}

export function documentStatusLabel(t: TFunction, status: string): string {
  const group = documentStatusGroup(status);
  return group ? t(`document.summary.${group}`) : statusLabel(t, status);
}

export function documentStatusTone(status: string): EnumTone {
  switch (documentStatusGroup(status)) {
    case "ready":
      return "success";
    case "failed":
    case "cancelled":
      return "error";
    case "pending":
    case "processing":
      return "warning";
    default:
      return "default";
  }
}

export function documentStageLabel(t: TFunction, stage: string): string {
  return t(`document.stageNames.${stage}`, { defaultValue: stage });
}

const DOCUMENT_FAILURE_KEYS: Record<string, string> = {
  unsupported_scanned_pdf: "document.failures.scannedPdf",
  unsupported_legacy_office: "document.failures.legacyOffice",
  unsupported_corrupt_document: "document.failures.corrupt",
  document_too_complex: "document.failures.tooComplex",
  file_too_large: "document.failures.tooLarge",
  unsupported_mime_type: "document.failures.unsupportedMime",
  empty_document: "document.failures.empty",
  parser_error: "document.failures.parser",
};

export function documentFailureText(t: TFunction, code?: string): string {
  return t((code && DOCUMENT_FAILURE_KEYS[code]) || "document.failures.generic");
}

const ANSWER_FAILURE_KEYS: Record<string, string> = {
  insufficient_evidence: "chat.failures.insufficientEvidence",
  citation_rejected: "chat.failures.citationRejected",
  ungrounded_answer: "chat.failures.ungrounded",
  model_error: "chat.failures.modelError",
  timeout: "chat.failures.timeout",
  client_disconnected: "chat.failures.disconnected",
  cancelled: "chat.failures.cancelled",
  worker_lost: "chat.failures.interrupted",
  worker_error: "chat.failures.workerError",
  queue_unavailable: "chat.failures.queueUnavailable",
  job_invalid: "chat.failures.invalid",
  forbidden: "chat.failures.forbidden",
};

export function answerFailureText(t: TFunction, code?: string): string {
  return t((code && ANSWER_FAILURE_KEYS[code]) || "chat.failures.generic");
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
