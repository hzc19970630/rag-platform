import {
  CloudServerOutlined,
  DashboardOutlined,
  DownloadOutlined,
  FileSearchOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  TeamOutlined,
  UserDeleteOutlined,
} from "@ant-design/icons";
import {
  Alert,
  App,
  Grid,
  Button,
  Card,
  Col,
  Descriptions,
  Flex,
  Input,
  InputNumber,
  Modal,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../api/client";
import {
  queryKeys,
  useAuditEvents,
  useTenantMembers,
  useTenantOverview,
  useTenantQuota,
  type AuditFilters,
} from "../api/hooks";
import type {
  AuditEvent,
  TenantMember,
  TenantQuota,
  TenantQuotaUpdateRequest,
} from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { ModelProvidersTab } from "../features/models/ModelProvidersTab";
import { TenantConfigurationTab } from "../features/settings/TenantConfigurationTab";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { usePermissions } from "../session";
import { AUDIT_ACTION_VALUES, AUDIT_RESOURCE_VALUES, auditActionLabel, auditResourceLabel, roleLabel } from "../utils/enums";
import { formatSize, formatTime } from "../utils/format";

const tenantRoles = ["owner", "admin", "member", "viewer"] as const;
const megabyte = 1024 * 1024;
const auditExportLimit = 500;

export type QuotaDraft = {
  maxDocuments: number | null;
  maxStorageMb: number | null;
  chatRequestsPerDay: number | null;
};

export function quotaDraftFrom(quota: TenantQuota): QuotaDraft {
  return {
    maxDocuments: quota.maxDocuments.source === "tenant" ? quota.maxDocuments.value : null,
    maxStorageMb: quota.maxStorageBytes.source === "tenant" ? Math.round(quota.maxStorageBytes.value / megabyte) : null,
    chatRequestsPerDay: quota.chatRequestsPerDay.source === "tenant" ? quota.chatRequestsPerDay.value : null,
  };
}

export function quotaPatchFrom(draft: QuotaDraft): TenantQuotaUpdateRequest {
  const patch: TenantQuotaUpdateRequest = {};
  if (draft.maxDocuments !== null) patch.maxDocuments = draft.maxDocuments;
  if (draft.maxStorageMb !== null) patch.maxStorageBytes = draft.maxStorageMb * megabyte;
  if (draft.chatRequestsPerDay !== null) patch.chatRequestsPerDay = draft.chatRequestsPerDay;
  return patch;
}

function csvCell(value: unknown): string {
  const raw = value === null || value === undefined ? "" : String(value);
  // Audit fields such as reason and user-agent are request-controlled; keep a
  // spreadsheet from treating them as formulas.
  const text = /^[=+\-@\t\r]/.test(raw) ? `'${raw}` : raw;
  return /[",\r\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text;
}

export function buildAuditCsv(rows: AuditEvent[]): string {
  const header = [
    "id",
    "createdAt",
    "tenantId",
    "actorId",
    "action",
    "resourceType",
    "resourceId",
    "allowed",
    "reason",
    "requestId",
    "ipAddress",
    "userAgent",
  ];
  const lines = [header.join(",")];
  for (const row of rows) {
    lines.push(
      [
        row.id,
        row.createdAt,
        row.tenantId,
        row.actorId,
        row.action,
        row.resourceType,
        row.resourceId,
        row.allowed,
        row.reason,
        row.requestId,
        row.ipAddress,
        row.userAgent,
      ].map(csvCell).join(","),
    );
  }
  return `\uFEFF${lines.join("\r\n")}`;
}

function downloadAuditCsv(rows: AuditEvent[], tenantId: string): void {
  const blob = new Blob([buildAuditCsv(rows)], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  const stamp = new Date().toISOString().slice(0, 19).replaceAll(":", "-");
  anchor.href = url;
  anchor.download = `audit-${tenantId || "tenant"}-${stamp}.csv`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function formatQuotaValue(value: number, unlimited: string, formatter: (value: number) => string): string {
  return value <= 0 ? unlimited : formatter(value);
}

export function AdminPage() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const { me, canAdmin } = usePermissions();
  const screens = Grid.useBreakpoint();
  const overview = useTenantOverview(canAdmin);
  const quota = useTenantQuota(canAdmin);
  const members = useTenantMembers(canAdmin);

  const [tab, setTab] = useState("overview");
  const [draft, setDraft] = useState<QuotaDraft>({ maxDocuments: null, maxStorageMb: null, chatRequestsPerDay: null });
  const [savingQuota, setSavingQuota] = useState(false);
  const [busyMember, setBusyMember] = useState("");
  const [memberOpen, setMemberOpen] = useState(false);
  const [memberUserId, setMemberUserId] = useState("");
  const [memberRole, setMemberRole] = useState<(typeof tenantRoles)[number]>("viewer");
  const [addingMember, setAddingMember] = useState(false);
  const [auditDraft, setAuditDraft] = useState<{
    actorId: string;
    action: string;
    resourceType: string;
    allowed: "" | "true" | "false";
  }>({ actorId: "", action: "", resourceType: "", allowed: "" });
  const [auditFilters, setAuditFilters] = useState<AuditFilters>({});
  const [auditPage, setAuditPage] = useState(1);
  const [auditPageSize, setAuditPageSize] = useState(20);
  const [auditExporting, setAuditExporting] = useState(false);
  const appliedAuditFilters = useMemo<AuditFilters>(() => ({
    ...auditFilters,
    limit: auditPageSize,
    offset: (auditPage - 1) * auditPageSize,
  }), [auditFilters, auditPage, auditPageSize]);
  const audit = useAuditEvents(appliedAuditFilters, canAdmin);

  useEffect(() => {
    if (quota.data) setDraft(quotaDraftFrom(quota.data));
  }, [quota.data]);

  if (!canAdmin) {
    return (
      <div className="page">
        <Alert type="error" showIcon title={t("common.noPermission")} description={t("admin.noPermission")} />
      </div>
    );
  }

  const auditRows = audit.data?.items ?? [];
  const auditTotal = audit.data?.total ?? 0;

  async function refreshOverview() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.tenantOverview }),
      queryClient.invalidateQueries({ queryKey: queryKeys.tenantQuota }),
      queryClient.invalidateQueries({ queryKey: queryKeys.tenantMembers }),
    ]);
  }

  async function saveQuota() {
    setSavingQuota(true);
    try {
      await api<TenantQuota>("/tenant/quota", jsonInit(quotaPatchFrom(draft), "PATCH"));
      message.success(t("admin.quota.saved"));
      await refreshOverview();
      await queryClient.invalidateQueries({ queryKey: ["audit"] });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("admin.quota.saveFailed"));
    } finally {
      setSavingQuota(false);
    }
  }

  function resetQuota() {
    modal.confirm({
      title: t("admin.quota.resetTitle"),
      content: t("admin.quota.resetConfirm"),
      okText: t("admin.quota.reset"),
      cancelText: t("common.cancel"),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await api<TenantQuota>("/tenant/quota", jsonInit({}, "PATCH"));
          message.success(t("admin.quota.resetDone"));
          await refreshOverview();
          await queryClient.invalidateQueries({ queryKey: ["audit"] });
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("admin.quota.resetFailed"));
        }
      },
    });
  }

  async function updateRole(member: TenantMember, role: string) {
    setBusyMember(member.userId);
    try {
      await api(`/tenant/members/${encodeURIComponent(member.userId)}`, jsonInit({ role }, "PATCH"));
      message.success(t("admin.members.updated"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.tenantMembers }),
        queryClient.invalidateQueries({ queryKey: queryKeys.tenantOverview }),
        queryClient.invalidateQueries({ queryKey: ["audit"] }),
      ]);
      if (member.userId === me.principal.subjectId) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.me });
      }
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("admin.members.updateFailed"));
    } finally {
      setBusyMember("");
    }
  }

  function removeMember(member: TenantMember) {
    modal.confirm({
      title: t("admin.members.removeTitle", { user: member.displayName || member.email || member.userId }),
      content: t("admin.members.removeConfirm"),
      okText: t("admin.members.remove"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        setBusyMember(member.userId);
        try {
          await api(`/tenant/members/${encodeURIComponent(member.userId)}`, { method: "DELETE" });
          message.success(t("admin.members.removed"));
          await Promise.all([
            queryClient.invalidateQueries({ queryKey: queryKeys.tenantMembers }),
            queryClient.invalidateQueries({ queryKey: queryKeys.tenantOverview }),
            queryClient.invalidateQueries({ queryKey: ["audit"] }),
          ]);
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("admin.members.removeFailed"));
        } finally {
          setBusyMember("");
        }
      },
    });
  }

  async function addMember() {
    const userId = memberUserId.trim();
    if (!userId) return;
    setAddingMember(true);
    try {
      await api("/tenant/members", jsonInit({ userId, role: memberRole }));
      message.success(t("admin.members.added"));
      setMemberOpen(false);
      setMemberUserId("");
      setMemberRole("viewer");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.tenantMembers }),
        queryClient.invalidateQueries({ queryKey: queryKeys.tenantOverview }),
        queryClient.invalidateQueries({ queryKey: ["audit"] }),
      ]);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("admin.members.addFailed"));
    } finally {
      setAddingMember(false);
    }
  }

  function applyAuditFilters() {
    setAuditPage(1);
    setAuditFilters({
      actorId: auditDraft.actorId.trim() || undefined,
      action: auditDraft.action.trim() || undefined,
      resourceType: auditDraft.resourceType.trim() || undefined,
      allowed: auditDraft.allowed || undefined,
    });
  }

  function resetAuditFilters() {
    setAuditDraft({ actorId: "", action: "", resourceType: "", allowed: "" });
    setAuditFilters({});
    setAuditPage(1);
  }

  async function exportAudit() {
    setAuditExporting(true);
    try {
      const params = new URLSearchParams();
      if (auditFilters.actorId) params.set("actorId", auditFilters.actorId);
      if (auditFilters.action) params.set("action", auditFilters.action);
      if (auditFilters.resourceType) params.set("resourceType", auditFilters.resourceType);
      if (auditFilters.allowed) params.set("allowed", auditFilters.allowed);
      params.set("limit", String(auditExportLimit));
      const data = await api<{ items: AuditEvent[] }>(`/audit?${params.toString()}`);
      downloadAuditCsv(data.items, overview.data?.id ?? me.principal.tenantId);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("admin.audit.exportFailed"));
    } finally {
      setAuditExporting(false);
    }
  }

  const roleOptions = tenantRoles.map((role) => ({ value: role, label: roleLabel(t, role) }));

  const memberColumns: TableColumnsType<TenantMember> = [
    {
      title: t("admin.members.user"),
      key: "user",
      render: (_, row) => (
        <Flex vertical gap={2}>
          <Space size={6} wrap>
            <Typography.Text strong>{row.displayName || row.email || row.userId}</Typography.Text>
            {row.userId === me.principal.subjectId && <Tag color="blue">{t("admin.members.self")}</Tag>}
          </Space>
          <Typography.Text type="secondary" className="mono-small">
            {row.email ? `${row.email} · ${row.userId}` : row.userId}
          </Typography.Text>
        </Flex>
      ),
    },
    {
      title: t("admin.members.role"),
      key: "role",
      width: 180,
      render: (_, row) => (
        <Select
          size="small"
          value={row.role}
          options={roleOptions}
          style={{ width: 120 }}
          disabled={busyMember === row.userId}
          onChange={(role) => void updateRole(row, role)}
          aria-label={t("admin.members.role")}
        />
      ),
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 120,
      render: (_, row) => (
        <Button
          size="small"
          danger
          type="link"
          icon={<UserDeleteOutlined />}
          disabled={row.userId === me.principal.subjectId}
          title={row.userId === me.principal.subjectId ? t("admin.members.cannotRemoveSelf") : undefined}
          loading={busyMember === row.userId}
          onClick={() => removeMember(row)}
        >
          {t("admin.members.remove")}
        </Button>
      ),
    },
  ];

  const auditColumns: TableColumnsType<AuditEvent> = [
    { title: t("admin.audit.time"), dataIndex: "createdAt", key: "createdAt", width: 180, render: (value: string) => formatTime(value) },
    {
      title: t("admin.audit.actor"),
      key: "actor",
      width: 190,
      render: (_, row) => (
        <Flex vertical gap={2}>
          <Typography.Text>{row.actorId || "-"}</Typography.Text>
          <Typography.Text type="secondary" className="mono-small">{row.ipAddress || "-"}</Typography.Text>
        </Flex>
      ),
    },
    { title: t("admin.audit.action"), dataIndex: "action", key: "action", width: 190, ellipsis: true, render: (value: string) => auditActionLabel(t, value) },
    {
      title: t("admin.audit.resource"),
      key: "resource",
      width: 220,
      render: (_, row) => (
        <Flex vertical gap={2}>
          <Typography.Text>{row.resourceType ? auditResourceLabel(t, row.resourceType) : "-"}</Typography.Text>
          <Typography.Text type="secondary" className="mono-small">{row.resourceId || "-"}</Typography.Text>
        </Flex>
      ),
    },
    {
      title: t("admin.audit.allowed"),
      key: "allowed",
      width: 100,
      render: (_, row) => (row.allowed ? <Tag color="green">{t("admin.audit.allow")}</Tag> : <Tag color="red">{t("admin.audit.deny")}</Tag>),
    },
    {
      title: t("admin.audit.reason"),
      dataIndex: "reason",
      key: "reason",
      width: 300,
      ellipsis: true,
      render: (value: string) => <Typography.Text ellipsis={{ tooltip: value }}>{value || "-"}</Typography.Text>,
    },
    { title: t("admin.audit.requestId"), dataIndex: "requestId", key: "requestId", width: 190, render: (value: string) => <span className="mono-small">{value || "-"}</span> },
  ];

  const quotaFields = quota.data
    ? [
        {
          key: "maxDocuments",
          label: t("admin.quota.maxDocuments"),
          unit: t("admin.quota.unitDocuments"),
          field: quota.data.maxDocuments,
          input: (
            <InputNumber
              min={0}
              precision={0}
              controls={false}
              value={draft.maxDocuments}
              placeholder={t("admin.quota.inheritPlaceholder")}
              onChange={(value) => setDraft((current) => ({ ...current, maxDocuments: typeof value === "number" ? value : null }))}
            />
          ),
          effective: formatQuotaValue(quota.data.maxDocuments.value, t("admin.quota.unlimited"), String),
          defaultValue: formatQuotaValue(quota.data.maxDocuments.default, t("admin.quota.unlimited"), String),
        },
        {
          key: "maxStorageBytes",
          label: t("admin.quota.maxStorage"),
          unit: t("admin.quota.unitStorage"),
          field: quota.data.maxStorageBytes,
          input: (
            <InputNumber
              min={0}
              precision={0}
              controls={false}
              value={draft.maxStorageMb}
              placeholder={t("admin.quota.inheritPlaceholder")}
              onChange={(value) => setDraft((current) => ({ ...current, maxStorageMb: typeof value === "number" ? value : null }))}
            />
          ),
          effective: formatQuotaValue(quota.data.maxStorageBytes.value, t("admin.quota.unlimited"), formatSize),
          defaultValue: formatQuotaValue(quota.data.maxStorageBytes.default, t("admin.quota.unlimited"), formatSize),
        },
        {
          key: "chatRequestsPerDay",
          label: t("admin.quota.chatRequestsPerDay"),
          unit: t("admin.quota.unitRequests"),
          field: quota.data.chatRequestsPerDay,
          input: (
            <InputNumber
              min={0}
              precision={0}
              controls={false}
              value={draft.chatRequestsPerDay}
              placeholder={t("admin.quota.inheritPlaceholder")}
              onChange={(value) => setDraft((current) => ({ ...current, chatRequestsPerDay: typeof value === "number" ? value : null }))}
            />
          ),
          effective: formatQuotaValue(quota.data.chatRequestsPerDay.value, t("admin.quota.unlimited"), String),
          defaultValue: formatQuotaValue(quota.data.chatRequestsPerDay.default, t("admin.quota.unlimited"), String),
        },
      ]
    : [];

  const overviewContent = (
    <div className="admin-stack">
      {overview.isLoading && <LoadingBlock />}
      {overview.error && <QueryFallback error={overview.error} retry={() => void overview.refetch()} />}
      {overview.data && (
        <>
          <div className="admin-stats">
            <Card size="small" className="admin-stat"><Statistic title={t("admin.overview.knowledgeBases")} value={overview.data.knowledgeBases} /></Card>
            <Card size="small" className="admin-stat"><Statistic title={t("admin.overview.documents")} value={overview.data.documents} /></Card>
            <Card size="small" className="admin-stat"><Statistic title={t("admin.overview.storage")} value={formatSize(overview.data.storageBytes)} /></Card>
            <Card size="small" className="admin-stat"><Statistic title={t("admin.overview.chatToday")} value={overview.data.chatQuestionsToday} /></Card>
            <Card size="small" className="admin-stat"><Statistic title={t("admin.overview.members")} value={overview.data.members.total} /></Card>
          </div>
          <Card size="small" title={t("admin.overview.title")}>
            <Descriptions size="small" column={{ xs: 1, md: 2 }} bordered className="admin-overview-descriptions">
              <Descriptions.Item label={t("admin.overview.name")}>{overview.data.name || "-"}</Descriptions.Item>
              <Descriptions.Item label={t("admin.overview.id")}><span className="mono-small">{overview.data.id}</span></Descriptions.Item>
              <Descriptions.Item label={t("admin.overview.createdAt")}>{formatTime(overview.data.createdAt)}</Descriptions.Item>
              <Descriptions.Item label={t("admin.overview.members")}>
                <Space size={4} wrap>
                  <Tag>{roleLabel(t, "owner")} {overview.data.members.owners}</Tag>
                  <Tag>{roleLabel(t, "admin")} {overview.data.members.admins}</Tag>
                  <Tag>{roleLabel(t, "member")} {overview.data.members.members}</Tag>
                  <Tag>{roleLabel(t, "viewer")} {overview.data.members.viewers}</Tag>
                </Space>
              </Descriptions.Item>
            </Descriptions>
          </Card>
        </>
      )}

      <Card
        title={t("admin.quota.title")}
        extra={(
          <Space wrap>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void quota.refetch()}>{t("common.refresh")}</Button>
            <Button size="small" danger onClick={resetQuota}>{t("admin.quota.reset")}</Button>
            <Button size="small" type="primary" loading={savingQuota} onClick={() => void saveQuota()}>{t("common.save")}</Button>
          </Space>
        )}
      >
        <Typography.Paragraph type="secondary">{t("admin.quota.description")}</Typography.Paragraph>
        {quota.isLoading && <LoadingBlock />}
        {quota.error && <QueryFallback error={quota.error} retry={() => void quota.refetch()} />}
        {quota.data && (
          <div className="admin-quota-grid">
            {quotaFields.map((item) => (
              <div className="admin-quota-field" key={item.key}>
                <div className="admin-quota-meta">
                  <Typography.Text strong>{item.label}</Typography.Text>
                  <Tag color={item.field.source === "tenant" ? "blue" : "default"}>
                    {item.field.source === "tenant" ? t("admin.quota.overridden") : t("admin.quota.inherited")}
                  </Tag>
                </div>
                {item.input}
                <Typography.Text type="secondary" className="settings-help">
                  {t("admin.quota.unitLabel", { unit: item.unit })} · {t("admin.quota.effective", { value: item.effective })} · {t("admin.quota.default", { value: item.defaultValue })}
                </Typography.Text>
              </div>
            ))}
          </div>
        )}
        <Alert className="settings-alert admin-quota-hint" type="info" showIcon title={t("admin.quota.hint")} />
      </Card>
    </div>
  );

  const membersContent = (
    <Card
      title={t("admin.members.title")}
      extra={<Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setMemberOpen(true)}>{t("admin.members.add")}</Button>}
    >
      <Typography.Paragraph type="secondary">{t("admin.members.description")}</Typography.Paragraph>
      {members.isLoading && <LoadingBlock />}
      {members.error && <QueryFallback error={members.error} retry={() => void members.refetch()} />}
      {members.data && members.data.length === 0 && <Alert type="info" showIcon title={t("admin.members.empty")} />}
      {members.data && members.data.length > 0 && (
        <Table<TenantMember> size="middle" rowKey="userId" columns={memberColumns} dataSource={members.data} pagination={false} scroll={{ x: 760 }} />
      )}
    </Card>
  );

  const auditContent = (
    <Card
      title={t("admin.audit.title")}
      extra={(
        <Space wrap>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void audit.refetch()}>{t("common.refresh")}</Button>
          <Button
            size="small"
            type="primary"
            icon={<DownloadOutlined />}
            disabled={auditTotal === 0}
            loading={auditExporting}
            onClick={() => void exportAudit()}
          >
            {t("admin.audit.export")}
          </Button>
        </Space>
      )}
    >
      <Typography.Paragraph type="secondary" className="admin-audit-description">{t("admin.audit.description")}</Typography.Paragraph>
      <div className="admin-filters">
        <label className="admin-filter-field">
          <Typography.Text type="secondary">{t("admin.audit.actor")}</Typography.Text>
          <Input value={auditDraft.actorId} onChange={(event) => setAuditDraft((current) => ({ ...current, actorId: event.target.value }))} placeholder={t("admin.audit.actorPlaceholder")} />
        </label>
        <label className="admin-filter-field">
          <Typography.Text type="secondary">{t("admin.audit.action")}</Typography.Text>
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            value={auditDraft.action || undefined}
            placeholder={t("admin.audit.allActions")}
            options={AUDIT_ACTION_VALUES.map((value) => ({ value, label: auditActionLabel(t, value) }))}
            onChange={(value) => setAuditDraft((current) => ({ ...current, action: value ?? "" }))}
          />
        </label>
        <label className="admin-filter-field">
          <Typography.Text type="secondary">{t("admin.audit.resource")}</Typography.Text>
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            value={auditDraft.resourceType || undefined}
            placeholder={t("admin.audit.allResources")}
            options={AUDIT_RESOURCE_VALUES.map((value) => ({ value, label: auditResourceLabel(t, value) }))}
            onChange={(value) => setAuditDraft((current) => ({ ...current, resourceType: value ?? "" }))}
          />
        </label>
        <label className="admin-filter-field">
          <Typography.Text type="secondary">{t("admin.audit.allowed")}</Typography.Text>
          <Select
            allowClear
            value={auditDraft.allowed || undefined}
            placeholder={t("admin.audit.all")}
            options={[
              { value: "true", label: t("admin.audit.allow") },
              { value: "false", label: t("admin.audit.deny") },
            ]}
            onChange={(value) => setAuditDraft((current) => ({ ...current, allowed: (value as "true" | "false" | undefined) ?? "" }))}
          />
        </label>
        <Space wrap className="admin-filter-actions">
          <Button type="primary" onClick={applyAuditFilters}>{t("admin.audit.query")}</Button>
          <Button onClick={resetAuditFilters}>{t("admin.audit.reset")}</Button>
        </Space>
      </div>
      <Alert className="settings-alert admin-audit-hint" type="info" showIcon title={t("admin.audit.exportHint", { count: auditExportLimit })} />
      {audit.isLoading && <LoadingBlock />}
      {audit.error && <QueryFallback error={audit.error} retry={() => void audit.refetch()} />}
      {audit.data && auditTotal === 0 && <Alert type="info" showIcon title={t("admin.audit.empty")} />}
      {auditTotal > 0 && (
        <Table<AuditEvent>
          size="middle"
          rowKey="id"
          columns={auditColumns}
          dataSource={auditRows}
          pagination={{
            current: auditPage,
            pageSize: auditPageSize,
            total: auditTotal,
            showTotal: (total) => t("admin.audit.total", { total }),
            showSizeChanger: true,
            pageSizeOptions: [20, 50, 100],
            onChange: (page, size) => {
              setAuditPage(size === auditPageSize ? page : 1);
              setAuditPageSize(size);
            },
          }}
          scroll={{ x: 1340 }}
        />
      )}
    </Card>
  );

  return (
    <div className="page">
      <PageHeader title={t("admin.title")} description={t("admin.description")} />
      <Tabs
        className="admin-tabs"
        activeKey={tab}
        onChange={setTab}
        tabBarGutter={screens.md ? 16 : 0}
        items={[
          { key: "overview", label: t("admin.tabs.overview"), icon: <DashboardOutlined />, children: overviewContent },
          { key: "members", label: t("admin.tabs.members"), icon: <TeamOutlined />, children: membersContent },
          { key: "audit", label: t("admin.tabs.audit"), icon: <FileSearchOutlined />, children: auditContent },
          { key: "models", label: t("admin.tabs.models"), icon: <CloudServerOutlined />, children: <ModelProvidersTab /> },
          { key: "configuration", label: t("admin.tabs.configuration"), icon: <SettingOutlined />, children: <TenantConfigurationTab /> },
        ]}
      />

      <Modal
        open={memberOpen}
        title={t("admin.members.addTitle")}
        okText={t("admin.members.add")}
        cancelText={t("common.cancel")}
        confirmLoading={addingMember}
        onCancel={() => setMemberOpen(false)}
        onOk={() => void addMember()}
        okButtonProps={{ disabled: !memberUserId.trim() }}
      >
        <Flex vertical gap={16}>
          <label className="field-label">
            {t("admin.members.userId")}
            <Input value={memberUserId} onChange={(event) => setMemberUserId(event.target.value)} placeholder={t("admin.members.userIdPlaceholder")} />
          </label>
          <label className="field-label">
            {t("admin.members.role")}
            <Select value={memberRole} options={roleOptions} style={{ width: "100%" }} onChange={(role) => setMemberRole(role)} />
          </label>
        </Flex>
      </Modal>
    </div>
  );
}
