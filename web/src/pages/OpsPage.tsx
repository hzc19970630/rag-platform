import {
  ApiOutlined,
  CloudSyncOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Col,
  Descriptions,
  Flex,
  Input,
  Modal,
  Row,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../api/client";
import { queryKeys, useAPIKeys, useDeadLetters, useOpsMetrics } from "../api/hooks";
import type { APIKeyView, DeadLetter } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { StatusBadge } from "../components/StatusBadge";
import { apiKeyStatusLabel, capabilityLabel, queueLabel } from "../utils/enums";
import { usePermissions } from "../session";
import { formatTime } from "../utils/format";

const capabilityOptions = [
  "knowledge_base:read",
  "knowledge_base:write",
  "knowledge_base:delete",
  "knowledge_base:manage_members",
  "chat",
];

export function OpsPage() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const { canAdmin } = usePermissions();
  const metrics = useOpsMetrics();
  const deadLetters = useDeadLetters();
  const apiKeys = useAPIKeys();

  const [tab, setTab] = useState("runtime");
  const [reconciling, setReconciling] = useState(false);
  const [reconcileReport, setReconcileReport] = useState("");
  const [busyLetter, setBusyLetter] = useState(0);
  const [keyOpen, setKeyOpen] = useState(false);
  const [keyName, setKeyName] = useState("");
  const [selectedCapabilities, setSelectedCapabilities] = useState<string[]>([]);
  const [creatingKey, setCreatingKey] = useState(false);
  const [issuedToken, setIssuedToken] = useState("");

  if (!canAdmin) {
    return (
      <div className="page">
        <Alert type="error" showIcon message={t("common.noPermission")} description={t("ops.noPermission")} />
      </div>
    );
  }

  async function refreshAll() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.metrics }),
      queryClient.invalidateQueries({ queryKey: queryKeys.deadLetters }),
    ]);
  }

  async function reconcile() {
    setReconciling(true);
    try {
      const report = await api<Record<string, number>>("/ops/reconcile", { method: "POST" });
      setReconcileReport(JSON.stringify(report, null, 2));
      message.success(t("ops.reconcileDone"));
      await refreshAll();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("ops.reconcileFailed"));
    } finally {
      setReconciling(false);
    }
  }

  async function act(letter: DeadLetter, action: "retry" | "discard") {
    setBusyLetter(letter.id);
    try {
      await api(`/ops/dead-letters/${letter.id}/${action}`, { method: "POST" });
      message.success(action === "retry" ? t("ops.retried") : t("ops.discarded"));
      await refreshAll();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("ops.actionFailed"));
    } finally {
      setBusyLetter(0);
    }
  }

  async function createKey() {
    if (!keyName.trim()) return;
    setCreatingKey(true);
    try {
      const result = await api<{ apiKey: APIKeyView; token: string }>("/api-keys", jsonInit({
        name: keyName.trim(),
        capabilities: selectedCapabilities,
      }));
      setIssuedToken(result.token);
      setKeyName("");
      setSelectedCapabilities([]);
      setKeyOpen(false);
      await queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("ops.createKeyFailed"));
    } finally {
      setCreatingKey(false);
    }
  }

  function revoke(row: APIKeyView) {
    modal.confirm({
      title: t("ops.revokeConfirm", { name: row.name }),
      okText: t("ops.revoke"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        try {
          await api(`/api-keys/${row.id}`, { method: "DELETE" });
          message.success(t("ops.revokedDone"));
          await queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys });
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("ops.revokeFailed"));
        }
      },
    });
  }

  const deadLetterColumns: TableColumnsType<DeadLetter> = [
    {
      title: t("ops.task"),
      key: "task",
      width: 360,
      render: (_, row) => (
        <Flex vertical gap={2}>
          <Typography.Text strong>{row.taskType}</Typography.Text>
          <Typography.Text type="secondary" className="mono-small">{row.stage} · {row.idempotencyKey}</Typography.Text>
        </Flex>
      ),
    },
    { title: t("ops.attempts"), dataIndex: "attempts", key: "attempts", width: 100 },
    { title: t("document.status"), dataIndex: "status", key: "status", width: 130, render: (value: string) => <StatusBadge status={value} /> },
    {
      title: t("ops.lastError"),
      dataIndex: "lastError",
      key: "lastError",
      width: 320,
      render: (value?: string) => value ? <Typography.Text type="danger" className="mono-small">{value}</Typography.Text> : "-",
    },
    { title: t("common.createdAt"), dataIndex: "createdAt", key: "createdAt", width: 180, render: (value: string) => formatTime(value) },
    {
      title: t("common.actions"),
      key: "actions",
      width: 180,
      render: (_, row) => row.status === "RESOLVED" || row.status === "DISCARDED" ? null : (
        <Space>
          <Button size="small" loading={busyLetter === row.id} onClick={() => void act(row, "retry")}>{t("ops.retry")}</Button>
          <Button size="small" danger loading={busyLetter === row.id} onClick={() => void act(row, "discard")}>{t("ops.discard")}</Button>
        </Space>
      ),
    },
  ];

  const keyColumns: TableColumnsType<APIKeyView> = [
    {
      title: t("ops.keyName"),
      key: "name",
      render: (_, row) => (
        <Flex vertical gap={2}>
          <Typography.Text strong>{row.name}</Typography.Text>
          <Typography.Text type="secondary" className="mono-small">{row.keyPrefix}…</Typography.Text>
        </Flex>
      ),
    },
    {
      title: t("ops.capabilities"),
      key: "capabilities",
      render: (_, row) => (
        <Space size={4} wrap>
          {row.capabilities.map((capability) => <Tag key={capability}>{capabilityLabel(t, capability)}</Tag>)}
        </Space>
      ),
    },
    {
      title: t("document.status"),
      key: "status",
      width: 110,
      render: (_, row) => row.revokedAt
        ? <Tag color="red">{apiKeyStatusLabel(t, "revoked")}</Tag>
        : row.expiresAt && new Date(row.expiresAt).getTime() <= Date.now()
          ? <Tag color="orange">{apiKeyStatusLabel(t, "expired")}</Tag>
          : <Tag color="green">{apiKeyStatusLabel(t, "active")}</Tag>,
    },
    {
      title: t("ops.expiresAt"),
      key: "expiresAt",
      width: 180,
      render: (_, row) => row.expiresAt ? formatTime(row.expiresAt) : t("ops.neverExpires"),
    },
    { title: t("ops.lastUsed"), dataIndex: "lastUsedAt", key: "lastUsedAt", width: 180, render: (value?: string) => value ? formatTime(value) : "-" },
    { title: t("common.createdAt"), dataIndex: "createdAt", key: "createdAt", width: 180, render: (value: string) => formatTime(value) },
    {
      title: t("common.actions"),
      key: "actions",
      width: 110,
      render: (_, row) => row.revokedAt ? null : (
        <Button size="small" danger icon={<DeleteOutlined />} onClick={() => revoke(row)}>{t("ops.revoke")}</Button>
      ),
    },
  ];

  const metricsContent = (
    <div className="ops-stack">
      <Card
        title={t("ops.metrics")}
        extra={(
          <Space wrap>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void refreshAll()}>{t("common.refresh")}</Button>
            <Button size="small" type="primary" icon={<CloudSyncOutlined />} loading={reconciling} onClick={() => void reconcile()}>
              {t("ops.reconcile")}
            </Button>
          </Space>
        )}
      >
        {metrics.isLoading && <LoadingBlock />}
        {metrics.error && <QueryFallback error={metrics.error} retry={() => void metrics.refetch()} />}
        {metrics.data && (
          <div className="ops-stack">
            <Descriptions size="small" column={{ xs: 1, md: 3 }} bordered>
              <Descriptions.Item label={t("ops.startedAt")}>{formatTime(metrics.data.startedAt)}</Descriptions.Item>
              <Descriptions.Item label={t("ops.queues")}>{Object.keys(metrics.data.queues ?? {}).length}</Descriptions.Item>
              <Descriptions.Item label={t("ops.queueError")}>{metrics.data.queueError || t("common.none")}</Descriptions.Item>
            </Descriptions>

            <Typography.Title level={5}>{t("ops.queues")}</Typography.Title>
            {metrics.data.queues ? (
              <Row gutter={[12, 12]}>
                {Object.entries(metrics.data.queues).map(([name, depth]) => (
                  <Col xs={24} md={12} xl={8} key={name}>
                    <Card size="small" title={queueLabel(t, name)}>
                      <Space size={4} wrap>
                        <Tag>{t("ops.pending")} {depth.pending ?? 0}</Tag>
                        <Tag color="processing">{t("ops.queueActive")} {depth.active ?? 0}</Tag>
                        <Tag color="warning">{t("ops.retry")} {depth.retry ?? 0}</Tag>
                        <Tag color="error">{t("ops.archived")} {depth.dead ?? depth.archived ?? 0}</Tag>
                      </Space>
                    </Card>
                  </Col>
                ))}
              </Row>
            ) : <Alert type="warning" showIcon message={metrics.data.queueError || t("ops.queueUnavailable")} />}

            <Row gutter={[12, 12]}>
              <Col xs={24} lg={12}>
                <Typography.Title level={5}>{t("ops.counters")}</Typography.Title>
                <Table
                  size="small"
                  rowKey="name"
                  pagination={false}
                  dataSource={Object.entries(metrics.data.counters).map(([name, value]) => ({ name, value }))}
                  columns={[
                    { title: t("ops.metricName"), dataIndex: "name", key: "name" },
                    { title: t("ops.value"), dataIndex: "value", key: "value", width: 120, align: "right" },
                  ]}
                />
              </Col>
              <Col xs={24} lg={12}>
                <Typography.Title level={5}>{t("ops.timings")}</Typography.Title>
                <Table
                  size="small"
                  rowKey="name"
                  pagination={false}
                  dataSource={Object.entries(metrics.data.timings).map(([name, timing]) => ({ name, ...timing }))}
                  columns={[
                    { title: t("ops.metricName"), dataIndex: "name", key: "name" },
                    { title: t("ops.count"), dataIndex: "count", key: "count", width: 80, align: "right" },
                    { title: t("ops.avg"), key: "avg", width: 90, align: "right", render: (_, row) => `${(row.avgMs ?? 0).toFixed(1)}ms` },
                    { title: t("ops.max"), dataIndex: "maxMs", key: "maxMs", width: 90, align: "right", render: (value: number) => `${value}ms` },
                  ]}
                />
              </Col>
            </Row>

            {reconcileReport && (
              <>
                <Typography.Title level={5}>{t("ops.reconcileReport")}</Typography.Title>
                <pre className="ops-report">{reconcileReport}</pre>
              </>
            )}
          </div>
        )}
      </Card>
    </div>
  );

  const deadLetterContent = (
    <Card title={t("ops.deadLetters")} extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void deadLetters.refetch()}>{t("common.refresh")}</Button>}>
      {deadLetters.isLoading && <LoadingBlock />}
      {deadLetters.error && <QueryFallback error={deadLetters.error} retry={() => void deadLetters.refetch()} />}
      {deadLetters.data && deadLetters.data.length === 0 && <Alert type="success" showIcon message={t("ops.noDeadLetters")} />}
      {deadLetters.data && deadLetters.data.length > 0 && (
        <Table<DeadLetter>
          size="middle"
          rowKey="id"
          columns={deadLetterColumns}
          dataSource={deadLetters.data}
          pagination={false}
          scroll={{ x: 1090, y: 360 }}
          virtual
        />
      )}
    </Card>
  );

  const keysContent = (
    <Card
      title={t("ops.apiKeys")}
      extra={<Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setKeyOpen(true)}>{t("ops.createKey")}</Button>}
    >
      <Alert type="info" showIcon message={t("ops.capabilityNote")} className="settings-alert" />
      {apiKeys.isLoading && <LoadingBlock />}
      {apiKeys.error && <QueryFallback error={apiKeys.error} retry={() => void apiKeys.refetch()} />}
      {apiKeys.data && apiKeys.data.length === 0 && <Alert type="info" showIcon message={t("ops.noKeys")} />}
      {apiKeys.data && apiKeys.data.length > 0 && (
        <Table<APIKeyView> size="middle" rowKey="id" columns={keyColumns} dataSource={apiKeys.data} pagination={false} scroll={{ x: true }} />
      )}
    </Card>
  );

  return (
    <div className="page">
      <PageHeader title={t("ops.title")} description={t("ops.description")} />
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          { key: "runtime", label: t("ops.metrics"), icon: <ApiOutlined />, children: metricsContent },
          { key: "deadLetters", label: t("ops.deadLetters"), icon: <WarningOutlined />, children: deadLetterContent },
          { key: "apiKeys", label: t("ops.apiKeys"), icon: <ApiOutlined />, children: keysContent },
        ]}
      />

      <Modal
        open={keyOpen}
        title={t("ops.createKey")}
        okText={t("ops.createKey")}
        cancelText={t("common.cancel")}
        confirmLoading={creatingKey}
        onCancel={() => setKeyOpen(false)}
        onOk={() => void createKey()}
        okButtonProps={{ disabled: !keyName.trim() }}
      >
        <Flex vertical gap={16}>
          <div>
            <Typography.Text strong>{t("ops.keyName")}</Typography.Text>
            <Input value={keyName} onChange={(event) => setKeyName(event.target.value)} placeholder={t("ops.keyNamePlaceholder")} />
          </div>
          <div>
            <Typography.Text strong>{t("ops.capabilities")}</Typography.Text>
            <Checkbox.Group
              className="capability-grid"
              value={selectedCapabilities}
              onChange={(values) => setSelectedCapabilities(values as string[])}
              options={capabilityOptions.map((capability) => ({ value: capability, label: capabilityLabel(t, capability) }))}
            />
          </div>
        </Flex>
      </Modal>

      <Modal
        open={Boolean(issuedToken)}
        title={t("ops.tokenTitle")}
        onCancel={() => setIssuedToken("")}
        onOk={() => setIssuedToken("")}
        okText={t("common.close")}
        cancelButtonProps={{ style: { display: "none" } }}
      >
        <Alert type="warning" showIcon message={t("ops.tokenOnce")} className="settings-alert" />
        <Typography.Paragraph className="token-box" copyable={{ text: issuedToken }}>
          <code>{issuedToken}</code>
        </Typography.Paragraph>
      </Modal>
    </div>
  );
}
