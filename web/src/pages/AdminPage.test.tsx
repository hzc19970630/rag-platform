import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AuditEvent, TenantQuota } from "../api/types";
import i18n from "../i18n";
import { SessionProvider } from "../session";
import { meWith } from "../test/fixtures";
import { AdminPage, buildAuditCsv, quotaDraftFrom, quotaPatchFrom } from "./AdminPage";

const quota: TenantQuota = {
  maxDocuments: { value: 100, default: 50, source: "tenant", unlimited: false },
  maxStorageBytes: { value: 0, default: 1073741824, source: "tenant", unlimited: true },
  chatRequestsPerDay: { value: 20, default: 20, source: "deployment", unlimited: false },
};

const auditRow: AuditEvent = {
  id: 7,
  tenantId: "tenant-1",
  actorId: "user-1",
  action: "update_tenant_quota",
  resourceType: "tenant",
  resourceId: "tenant-1",
  allowed: true,
  reason: 'quota before={} after="{}"',
  requestId: "req-1",
  ipAddress: "127.0.0.1",
  userAgent: "vitest",
  createdAt: "2026-11-07T10:00:00Z",
};

describe("admin quota draft", () => {
  it("maps inherited fields to empty inputs and keeps explicit zero", () => {
    expect(quotaDraftFrom(quota)).toEqual({
      maxDocuments: 100,
      maxStorageMb: 0,
      chatRequestsPerDay: null,
    });
  });

  it("omits empty fields so PATCH restores inheritance", () => {
    expect(quotaPatchFrom({ maxDocuments: 0, maxStorageMb: 5, chatRequestsPerDay: null })).toEqual({
      maxDocuments: 0,
      maxStorageBytes: 5 * 1024 * 1024,
    });
  });
});

describe("audit CSV export", () => {
  it("adds a BOM, escapes quotes and keeps the tenant fields", () => {
    const csv = buildAuditCsv([auditRow]);
    expect(csv.charCodeAt(0)).toBe(0xfeff);
    expect(csv).toContain("tenantId");
    expect(csv).toContain('"quota before={} after=""{}"""');
    expect(csv.split("\r\n")).toHaveLength(2);
  });

  it("neutralizes spreadsheet formulas in request-controlled fields", () => {
    const csv = buildAuditCsv([{ ...auditRow, reason: '=HYPERLINK("https://example.invalid")' }]);
    expect(csv).toContain(`"'=HYPERLINK(""https://example.invalid"")"`);
  });
});

describe("admin page access", () => {
  it("shows the permission notice to non-operators without querying", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AntdApp>
          <SessionProvider value={meWith(["knowledge_base:read"])}>
            <AdminPage />
          </SessionProvider>
        </AntdApp>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(i18n.t("admin.noPermission"))).toBeInTheDocument();
  });
});
