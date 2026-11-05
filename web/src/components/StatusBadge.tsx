import { Tag } from "antd";

const success = new Set(["EVIDENCE_READY", "SUCCEEDED", "COMPLETED", "PUBLISHED", "ACTIVE", "RELATION_READY", "KNOWLEDGE_READY", "RESOLVED"]);
const warning = new Set(["PENDING", "PENDING_REVIEW", "REVIEW_REQUIRED", "KNOWLEDGE_PARTIAL", "BUILDING", "RUNNING", "QUEUED"]);
const danger = new Set(["FAILED", "CANCELLED", "DEAD", "DISCARDED", "STALE", "SUPERSEDED", "REJECTED"]);

function statusColor(status: string): string {
  if (success.has(status)) return "success";
  if (warning.has(status)) return "warning";
  if (danger.has(status)) return "error";
  return "default";
}

export function StatusBadge({ status }: { status: string }) {
  return (
    <Tag color={statusColor(status)} className="status-badge">
      <span className={`status-dot status-${statusColor(status)}`} aria-hidden="true" />
      {status}
    </Tag>
  );
}
