import { describe, expect, it } from "vitest";
import type { ConfigChangePreview } from "../../api/types";
import { impactLabelKey, previewDecision, previewValue } from "./configPreview";

function preview(overrides: Partial<ConfigChangePreview> = {}): ConfigChangePreview {
  return {
    scope: "tenant",
    changes: [],
    impact: "hot",
    rebuildRequired: false,
    rebuildOptional: false,
    restartRequired: false,
    blockers: [],
    affected: { knowledgeBases: 1, documents: 2, chunks: 3 },
    estimatedDurationTier: "low",
    estimatedCostTier: "low",
    actions: ["apply_now"],
    ...overrides,
  };
}

describe("config preview decision", () => {
  it("applies query-time changes without a dialog", () => {
    expect(previewDecision(preview())).toBe("apply");
  });

  it("confirms changes that only affect future ingestion", () => {
    expect(previewDecision(preview({ impact: "next_ingest", rebuildOptional: true }))).toBe("confirm");
  });

  it("blocks data-contract changes and reports blockers", () => {
    expect(previewDecision(preview({
      impact: "rebuild",
      rebuildRequired: true,
      blockers: [{ code: "embedding_contract_requires_rebuild", message: "m", remediation: "r" }],
    }))).toBe("blocked");
  });

  it("blocks a restart-required change", () => {
    expect(previewDecision(preview({ impact: "restart", restartRequired: true }))).toBe("blocked");
  });
});

describe("config preview formatting", () => {
  it("maps impact labels", () => {
    expect(impactLabelKey("next_ingest")).toBe("configuration.impactNextIngest");
    expect(impactLabelKey("rebuild")).toBe("configuration.impactRebuild");
    expect(impactLabelKey("none")).toBe("configuration.impactNone");
  });

  it("formats arrays, booleans and numbers", () => {
    expect(previewValue(["\n\n", "\n"])).toBe("\n\n, \n");
    expect(previewValue(false)).toBe("false");
    expect(previewValue(0.5)).toBe("0.5");
    expect(previewValue(undefined)).toBe("—");
  });
});
