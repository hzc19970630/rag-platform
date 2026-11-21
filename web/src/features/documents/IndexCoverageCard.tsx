import { App, Alert, Button, Card, Descriptions, Space, Tag, Typography } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useDocumentIndexEstimate, useDocumentStatus } from "../../api/hooks";

const LABELS: Record<string, string> = {
  standard: "indexCoverage.modeStandard",
  deep: "indexCoverage.modeDeep",
  lexical: "indexCoverage.modeLexical",
};

export function IndexCoverageCard({ documentId, canWrite }: { documentId: string; canWrite: boolean }) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState("");
  const status = useDocumentStatus(documentId);
  const ready = Boolean(status.data?.contentReady);
  const estimate = useDocumentIndexEstimate(documentId, ready);
  const data = estimate.data;
  if (!data || data.tableCount === 0) return null;

  async function apply(mode: string) {
    setBusy(mode);
    try {
      await api(`/documents/${documentId}/index-mode`, jsonInit({ mode }));
      message.success(t("indexCoverage.switchStarted"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.documentStatus(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.documentVersions(documentId) }),
        queryClient.invalidateQueries({ queryKey: ["document-index-estimate", documentId] }),
      ]);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("indexCoverage.switchFailed"));
    } finally {
      setBusy("");
    }
  }

  function confirmDeep() {
    const minutes = data!.deepEstimatedMinutes;
    modal.confirm({
      title: t("indexCoverage.deepConfirmTitle"),
      content: t("indexCoverage.deepConfirmBody", {
        tokens: data!.deepEstimatedTokens,
        minutes: minutes > 0 ? Math.ceil(minutes) : "?",
      }),
      okText: t("indexCoverage.deepConfirmOk"),
      cancelText: t("common.cancel"),
      onOk: () => apply("deep"),
    });
  }

  return (
    <Card
      className="detail-card"
      title={t("indexCoverage.title")}
      extra={<Tag>{t(LABELS[data.indexMode] ?? "indexCoverage.modeStandard")}</Tag>}
    >
      <Descriptions size="small" column={{ xs: 1, sm: 2, md: 3 }}>
        <Descriptions.Item label={t("indexCoverage.coverage")}>
          {t("indexCoverage.coverageValue", {
            vectorRows: data.vectorRows,
            totalRows: data.totalRows,
            vectorSheets: data.vectorSheets,
            totalSheets: data.totalSheets,
          })}
        </Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.vectorTokens")}>{data.currentVectorTokens}</Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.tables")}>{data.tableCount}</Descriptions.Item>
      </Descriptions>
      {data.vectorRows < data.totalRows && (
        <Typography.Text type="secondary">{t("indexCoverage.keywordHint")}</Typography.Text>
      )}
      {canWrite && (
        <Space style={{ marginTop: 12 }} wrap>
          <Button size="small" loading={busy === "standard"} disabled={data.indexMode === "standard"} onClick={() => void apply("standard")}>
            {t("indexCoverage.useStandard")}
          </Button>
          <Button size="small" loading={busy === "deep"} disabled={data.indexMode === "deep"} onClick={confirmDeep}>
            {t("indexCoverage.useDeep")}
          </Button>
          <Button size="small" loading={busy === "lexical"} disabled={data.indexMode === "lexical"} onClick={() => void apply("lexical")}>
            {t("indexCoverage.useLexical")}
          </Button>
        </Space>
      )}
      {data.tpmBudget === 0 && (
        <Alert style={{ marginTop: 12 }} type="info" showIcon message={t("indexCoverage.noTPM")} />
      )}
    </Card>
  );
}
