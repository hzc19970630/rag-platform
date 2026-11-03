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
      await api(`/knowledge-bases/${knowledgeBaseId}/${kind}/rebuild`, { method: "POST" });
      message.success(t("projection.rebuildStarted"));
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

  const columns = (kind: RunKind): ColumnsType<ProjectionRun> => [
    { title: t("projection.runStatus"), dataIndex: "status", key: "status", render: (value: string) => <StatusBadge status={value} /> },
    { title: t("projection.runModel"), dataIndex: "model", key: "model", render: (value?: string) => value || "-" },
    { title: t("common.createdAt"), dataIndex: "createdAt", key: "createdAt", render: (value?: string) => formatTime(value) },
    {
      title: t("common.actions"),
      key: "actions",
      render: (_, run) => (
        <Space>
          {canWrite && run.status === "FAILED" && (
            <Button size="small" loading={busy === run.id} onClick={() => void retry(kind, run)}>{t("projection.retry")}</Button>
          )}
        </Space>
      ),
    },
  ];

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
        : <Table<ProjectionRun> size="small" rowKey="id" pagination={false} columns={columns(kind)} dataSource={runs} scroll={{ x: true }} />}
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
