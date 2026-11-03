import { Alert, App, Button, Card, Empty, Flex, List, Space, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import { queryKeys, useConnectorRuns, useConnectors } from "../../api/hooks";
import type { Connector, ConnectorRun } from "../../api/types";
import { LoadingBlock, QueryFallback } from "../../components/QueryFallback";
import { StatusBadge } from "../../components/StatusBadge";
import { formatTime } from "../../utils/format";

type Props = {
  knowledgeBaseId: string;
  canOperate: boolean;
};

export function DataSourceSettings({ knowledgeBaseId, canOperate }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const connectors = useConnectors(knowledgeBaseId);

  if (connectors.isLoading) return <LoadingBlock />;
  if (connectors.error) return <QueryFallback error={connectors.error} retry={() => void connectors.refetch()} />;
  const rows = connectors.data ?? [];

  async function sync(connector: Connector) {
    try {
      await api(`/connectors/${connector.id}/sync`, { method: "POST" });
      message.success(t("dataSource.syncStarted"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.connectors(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.connectorRuns(connector.id) }),
      ]);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("dataSource.syncFailed"));
    }
  }

  return (
    <div className="settings-section">
      {!canOperate && <Alert type="info" showIcon message={t("dataSource.readOnlyHint")} className="settings-alert" />}
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("dataSource.title")}</Typography.Title>
            <Typography.Text type="secondary">{t("dataSource.hint")}</Typography.Text>
          </div>
          <Button size="small" onClick={() => void connectors.refetch()}>{t("common.refresh")}</Button>
        </header>
        {rows.length === 0
          ? <Empty description={t("dataSource.empty")} />
          : (
            <Flex vertical gap={12}>
              {rows.map((connector) => (
                <ConnectorCard
                  key={connector.id}
                  connector={connector}
                  canOperate={canOperate}
                  onSync={() => void sync(connector)}
                />
              ))}
            </Flex>
          )}
      </section>
    </div>
  );
}

function ConnectorCard({ connector, canOperate, onSync }: { connector: Connector; canOperate: boolean; onSync: () => void }) {
  const { t } = useTranslation();
  const runs = useConnectorRuns(connector.id);
  const connectorRuns = runs.data ?? [];

  return (
    <Card size="small" className="connector-card">
      <Flex justify="space-between" align="flex-start" gap={12} wrap="wrap">
        <div>
          <Space size={8} wrap>
            <Typography.Text strong>{connector.name}</Typography.Text>
            <Tag>{connector.type}</Tag>
            <Tag color={connector.enabled ? "green" : "default"}>{connector.enabled ? t("common.enabled") : t("common.disabled")}</Tag>
            {connector.hasCredential && <Tag color="blue">{t("dataSource.credential")}</Tag>}
          </Space>
          <div className="setting-help">{t("dataSource.url")}: <code>{connector.config.manifestUrl}</code></div>
          <div className="setting-help">
            {t("dataSource.lastSync")}: {formatTime(connector.lastSyncAt)} · {t("dataSource.interval")}: {connector.syncIntervalSeconds || 0}s
          </div>
        </div>
        <Space>
          {canOperate && <Button size="small" onClick={onSync} disabled={!connector.enabled}>{t("dataSource.sync")}</Button>}
        </Space>
      </Flex>
      {runs.error && <Typography.Text type="danger">{runs.error instanceof Error ? runs.error.message : t("common.networkError")}</Typography.Text>}
      {runs.isLoading && <LoadingBlock />}
      {!runs.isLoading && connectorRuns.length === 0 && <Typography.Text type="secondary">{t("dataSource.noRuns")}</Typography.Text>}
      {connectorRuns.length > 0 && (
        <List
          size="small"
          className="connector-runs"
          dataSource={connectorRuns}
          renderItem={(run: ConnectorRun) => (
            <List.Item>
              <Flex justify="space-between" align="center" gap={12} wrap="wrap" className="connector-run-row">
                <Space size={8} wrap>
                  <StatusBadge status={run.status} />
                  <span>{formatTime(run.createdAt)}</span>
                  <span>{t("dataSource.items")}: {run.itemsTotal}</span>
                  {run.itemsFailed > 0 && <span className="doc-error">{t("dataSource.failed", { count: run.itemsFailed })}</span>}
                </Space>
                {run.errorSummary && <Typography.Text type="danger" className="connector-run-error">{run.errorSummary}</Typography.Text>}
              </Flex>
            </List.Item>
          )}
        />
      )}
    </Card>
  );
}
