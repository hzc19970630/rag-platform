import { App, Button, Card, Col, Empty, Flex, Row, Space, Statistic, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import { queryKeys, useProjectionStatus } from "../../api/hooks";
import type { ProjectionRun } from "../../api/types";
import { LoadingBlock, QueryFallback } from "../../components/QueryFallback";
import { StatusBadge } from "../../components/StatusBadge";
import { formatTime } from "../../utils/format";

type Props = {
  knowledgeBaseId: string;
  canWrite: boolean;
  embedded?: boolean;
};

type RunKind = "wiki" | "graph";

// Runs in these states still have queued work; cancellation is the operator's
// way to stop them without waiting for the retry budget.
const ACTIVE_RUN_STATUSES = new Set([
  "WIKI_PENDING", "WIKI_GENERATING", "RETRY_WIKI",
  "RELATION_PENDING", "RELATION_EXTRACTING", "RETRY_RELATION",
]);

// Backend run errors carry a stable code; users see a localized reason instead
// of the developer-facing English message.
const PROJECTION_ERROR_KEYS: Record<string, string> = {
  empty_relation_projection: "projection.errorReason.emptyRelation",
  unsourced_relations: "projection.errorReason.unsourcedRelations",
  untraceable_relation: "projection.errorReason.untraceableRelation",
  stale_relation_source: "projection.errorReason.staleRelationSource",
  no_evidence: "projection.errorReason.noEvidence",
  graph_disabled: "projection.errorReason.graphDisabled",
  relation_extractor_missing: "projection.errorReason.extractorMissing",
};

export function ProjectionPanel({ knowledgeBaseId, canWrite, embedded = false }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const status = useProjectionStatus(knowledgeBaseId);
  const [busy, setBusy] = useState("");

  if (status.isLoading) return <LoadingBlock />;
  if (status.error) return <QueryFallback error={status.error} retry={() => void status.refetch()} />;
  const data = status.data;
  if (!data) return null;

  async function rebuild(kind: RunKind) {
    if (!canWrite || busy) return;
    const confirmed = await new Promise<boolean>((resolve) => {
      modal.confirm({
        title: kind === "wiki" ? t("projection.rebuildWiki") : t("projection.rebuildGraph"),
        content: t("projection.rebuildConfirm"),
        okText: t("common.confirm"),
        cancelText: t("common.cancel"),
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
    if (!confirmed) return;
    setBusy(kind);
    try {
      const result = await api<{ started: number }>(`/knowledge-bases/${knowledgeBaseId}/${kind}/rebuild`, { method: "POST" });
      if (result.started > 0) {
        message.success(t("projection.rebuildStarted"));
      } else {
        message.info(t("projection.rebuildCoalesced"));
      }
      await queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("projection.rebuildFailed"));
    } finally {
      setBusy("");
    }
  }

  async function retry(kind: RunKind, run: ProjectionRun) {
    if (!canWrite || busy) return;
    setBusy(run.id);
    try {
      await api(`/${kind}/runs/${run.id}/retry`, { method: "POST" });
      message.success(t("projection.retryStarted"));
      await queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("projection.retryFailed"));
    } finally {
      setBusy("");
    }
  }

  async function cancel(kind: RunKind, run: ProjectionRun) {
    if (!canWrite || busy) return;
    const confirmed = await new Promise<boolean>((resolve) => {
      modal.confirm({
        title: t("projection.cancel"),
        content: t("projection.cancelConfirm"),
        okText: t("common.confirm"),
        cancelText: t("common.cancel"),
        okButtonProps: { danger: true },
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
    if (!confirmed) return;
    setBusy(run.id);
    try {
      await api(`/${kind}/runs/${run.id}/cancel`, { method: "POST" });
      message.success(t("projection.cancelStarted"));
      await queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("projection.cancelFailed"));
    } finally {
      setBusy("");
    }
  }

  const columns = (kind: RunKind): ColumnsType<ProjectionRun> => {
    const progress: ColumnsType<ProjectionRun> = kind === "graph"
      ? [{
        title: t("projection.unitProgress"),
        key: "unitProgress",
        width: 120,
        render: (_, run) => {
          if (!run.unitTotal) return "-";
          const text = `${run.unitSucceeded ?? 0} / ${run.unitTotal}`;
          if (!run.unitFailed) return text;
          return <Typography.Text type="warning">{text} · {t("projection.unitFailed", { count: run.unitFailed })}</Typography.Text>;
        },
      }]
      : [];
    return [
    { title: t("projection.runStatus"), dataIndex: "status", key: "status", width: 110, render: (value: string) => <StatusBadge status={value} /> },
    ...progress,
    {
      title: t("projection.failureReason"),
      key: "failureReason",
      width: 240,
      render: (_, run) => {
        if (!run.status.startsWith("FAILED_")) return "-";
        const key = PROJECTION_ERROR_KEYS[run.errorCode ?? ""] ?? "projection.errorReason.fallback";
        return <Typography.Text type="secondary">{t(key)}</Typography.Text>;
      },
    },
    { title: t("projection.runModel"), dataIndex: "model", key: "model", width: 180, render: (value?: string) => value || "-" },
    { title: t("common.createdAt"), dataIndex: "createdAt", key: "createdAt", width: 150, render: (value?: string) => formatTime(value) },
    {
      title: t("common.actions"),
      key: "actions",
      width: 170,
      render: (_, run) => (
        <Space>
          {canWrite && ACTIVE_RUN_STATUSES.has(run.status) && (
            <Button size="small" danger loading={busy === run.id} onClick={() => void cancel(kind, run)}>{t("projection.cancel")}</Button>
          )}
          {canWrite && run.status.startsWith("FAILED") && (
            <Button size="small" loading={busy === run.id} onClick={() => void retry(kind, run)}>{t("projection.retry")}</Button>
          )}
        </Space>
      ),
    },
    ];
  };

  const runTable = (kind: RunKind, runs: ProjectionRun[]) => (
    <div className="projection-runs">
      <Flex justify="space-between" align="center" gap={8} wrap="wrap">
        <Typography.Title level={5}>{kind === "wiki" ? t("projection.wiki") : t("projection.graph")} · {t("projection.runStatus")}</Typography.Title>
        {canWrite && (
          <Button size="small" onClick={() => void rebuild(kind)} loading={busy === kind}>
            {kind === "wiki" ? t("projection.rebuildWiki") : t("projection.rebuildGraph")}
          </Button>
        )}
      </Flex>
      {runs.length === 0
        ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("projection.noRuns")} />
        : <Table<ProjectionRun> size="small" rowKey="id" pagination={false} columns={columns(kind)} dataSource={runs} tableLayout="fixed" scroll={{ x: kind === "graph" ? 970 : 850 }} />}
    </div>
  );

  const content = (
    <>
      <Flex justify="space-between" align="center" gap={8} wrap="wrap">
        <Space>
          <Typography.Text strong>{t("projection.joinStatus")}</Typography.Text>
          <StatusBadge status={data.joinStatus} />
          <Tag color={data.wikiEnabled ? "green" : "default"}>wiki {data.wikiEnabled ? t("common.enabled") : t("common.disabled")}</Tag>
          <Tag color={data.graphEnabled ? "green" : "default"}>graph {data.graphEnabled ? t("common.enabled") : t("common.disabled")}</Tag>
        </Space>
        <Button size="small" onClick={() => void status.refetch()}>{t("common.refresh")}</Button>
      </Flex>

      <Row gutter={[12, 12]}>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.pages")} value={data.pages.total} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.published")} value={data.pages.published} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.pendingReview")} value={data.pages.pendingReview} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.indexed")} value={data.pages.indexed} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.entities")} value={data.graph.entities} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.relations")} value={data.graph.relations} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.snapshots")} value={data.graph.snapshots} /></Card></Col>
        <Col xs={12} md={6}><Card size="small"><Statistic title={t("projection.stale")} value={data.pages.stale} /></Card></Col>
      </Row>

      {data.graph.activeSnapshot && (
        <Typography.Text type="secondary">{t("projection.activeSnapshot")}: <code>{data.graph.activeSnapshot}</code></Typography.Text>
      )}

      <div className="projection-grid">
        {data.wikiEnabled && runTable("wiki", data.runs ?? [])}
        {data.graphEnabled && runTable("graph", data.relationRuns ?? [])}
        {!data.wikiEnabled && !data.graphEnabled && <Empty description={t("projection.disabled")} />}
      </div>
    </>
  );

  if (embedded) {
    return <div className="settings-section"><section className="settings-block">{content}</section></div>;
  }
  return <Card className="projection-card">{content}</Card>;
}
