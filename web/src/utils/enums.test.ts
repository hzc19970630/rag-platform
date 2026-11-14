import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";
import i18n from "../i18n";
import {
  CAPABILITY_VALUES,
  ROLE_VALUES,
  API_KEY_STATUS_VALUES,
  AUDIT_ACTION_VALUES,
  AUDIT_RESOURCE_VALUES,
  CONNECTOR_TYPE_VALUES,
  EMBEDDING_MIGRATION_STATUS_VALUES,
  PROVIDER_KIND_VALUES,
  QUEUE_VALUES,
  SOURCE_VALUES,
  STATUS_VALUES,
  enumLabel,
  enumLabelKey,
  statusLabel,
  statusTone,
} from "./enums";

const languages = ["zh-CN", "en-US"];

describe("enum registry", () => {
  it("has a translation for every registered status in both languages", () => {
    for (const language of languages) {
      for (const status of STATUS_VALUES) {
        expect(i18n.exists(enumLabelKey("status", status), { lng: language }), `${language} status.${status}`).toBe(true);
      }
    }
  });

  it("has a translation for every role and capability", () => {
    for (const language of languages) {
      for (const role of ROLE_VALUES) {
        expect(i18n.exists(enumLabelKey("role", role), { lng: language }), `${language} role.${role}`).toBe(true);
      }
      for (const capability of CAPABILITY_VALUES) {
        expect(i18n.exists(enumLabelKey("capability", capability), { lng: language }), `${language} capability.${capability}`).toBe(true);
      }
      for (const source of SOURCE_VALUES) {
        expect(i18n.exists(enumLabelKey("source", source), { lng: language }), `${language} source.${source}`).toBe(true);
      }
      for (const connectorType of CONNECTOR_TYPE_VALUES) {
        expect(i18n.exists(enumLabelKey("connector_type", connectorType), { lng: language }), `${language} connector_type.${connectorType}`).toBe(true);
      }
      for (const kind of PROVIDER_KIND_VALUES) {
        expect(i18n.exists(enumLabelKey("provider_kind", kind), { lng: language }), `${language} provider_kind.${kind}`).toBe(true);
      }
      for (const action of AUDIT_ACTION_VALUES) {
        expect(i18n.exists(enumLabelKey("audit_action", action), { lng: language }), `${language} audit_action.${action}`).toBe(true);
      }
      for (const resource of AUDIT_RESOURCE_VALUES) {
        expect(i18n.exists(enumLabelKey("audit_resource", resource), { lng: language }), `${language} audit_resource.${resource}`).toBe(true);
      }
      for (const status of API_KEY_STATUS_VALUES) {
        expect(i18n.exists(enumLabelKey("api_key_status", status), { lng: language }), `${language} api_key_status.${status}`).toBe(true);
      }
      for (const queue of QUEUE_VALUES) {
        expect(i18n.exists(enumLabelKey("queue", queue), { lng: language }), `${language} queue.${queue}`).toBe(true);
      }
      for (const status of EMBEDDING_MIGRATION_STATUS_VALUES) {
        expect(i18n.exists(enumLabelKey("embedding_migration_status", status), { lng: language }), `${language} embedding_migration_status.${status}`).toBe(true);
      }
    }
  });

  it("normalizes capability separators into one key namespace", () => {
    expect(enumLabelKey("capability", "knowledge_base:read")).toBe("enum.capability.knowledge_base_read");
  });

  it("falls back to the raw value for an unknown enum member", () => {
    const t = i18n.t as TFunction;
    expect(statusLabel(t, "NEW_BACKEND_STATE")).toBe("NEW_BACKEND_STATE");
    expect(enumLabel(t, "role", "auditor")).toBe("auditor");
  });

  it("maps status tones centrally", () => {
    expect(statusTone("EVIDENCE_READY")).toBe("success");
    expect(statusTone("PARSING")).toBe("warning");
    expect(statusTone("FAILED")).toBe("error");
    expect(statusTone("UNREGISTERED")).toBe("default");
    expect(statusTone("ok")).toBe("success");
  });
});
