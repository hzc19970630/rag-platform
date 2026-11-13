import type { ConfigChangePreview } from "../../api/types";

export type PreviewDecision = "apply" | "confirm" | "blocked";

// previewDecision keeps the save flow policy in one place: data-contract
// changes are blocked, writes that only affect future ingestion need an
// explicit confirmation, and query-time changes save directly.
export function previewDecision(preview: ConfigChangePreview): PreviewDecision {
  if (preview.blockers.length > 0 || preview.rebuildRequired || preview.restartRequired) return "blocked";
  if (preview.impact === "next_ingest") return "confirm";
  return "apply";
}

export function impactLabelKey(impact: string): string {
  switch (impact) {
    case "next_ingest":
      return "configuration.impactNextIngest";
    case "rebuild":
      return "configuration.impactRebuild";
    case "restart":
      return "configuration.impactRestart";
    case "none":
      return "configuration.impactNone";
    default:
      return "configuration.impactHot";
  }
}

export function previewValue(value: unknown): string {
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return String(value);
  if (typeof value === "number") return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(4)));
  return String(value ?? "—");
}
