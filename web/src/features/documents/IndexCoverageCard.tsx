import { App, Alert, Button, Card, Collapse, Descriptions, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useDocumentIndexEstimate, useDocumentStatus } from "../../api/hooks";
import { tableParseStatusLabel, tableStorageLabel } from "../../utils/enums";

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

  const fullStorage = !data.storageMode || data.storageMode === "full";
  const coverageKnown = data.coverageKnown !== false;
  const hasCoverageGap = Boolean(data.parseTruncated) || !fullStorage;

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

  const sheetColumns = [
    {
      title: t("indexCoverage.sheet"),
      dataIndex: "sheet",
      key: "sheet",
      render: (value: string) => <Typography.Text>{value || t("indexCoverage.defaultSheet")}</Typography.Text>,
    },
    {
      title: t("indexCoverage.parseStatus"),
      dataIndex: "parseStatus",
      key: "parseStatus",
      render: (value: string) => <Tag>{tableParseStatusLabel(t, value || "parsed")}</Tag>,
    },
    {
      title: t("indexCoverage.rows"),
      key: "rows",
      render: (_: unknown, row: { parsedRows: number; totalRows: number }) =>
        `${row.parsedRows}/${row.totalRows}`,
    },
    {
      title: t("indexCoverage.mode"),
      dataIndex: "indexMode",
      key: "indexMode",
      render: (value: string) => t(LABELS[value] ?? "indexCoverage.modeStandard"),
    },
  ];

  return (
    <Card
      className="detail-card"
      title={t("indexCoverage.title")}
      extra={
        <Space size={4} wrap>
          <Tag>{t(LABELS[data.indexMode] ?? "indexCoverage.modeStandard")}</Tag>
          <Tag color={fullStorage ? "green" : "orange"}>
            {tableStorageLabel(t, data.storageMode || "full")}
          </Tag>
        </Space>
      }
    >
      <Descriptions size="small" column={{ xs: 1, sm: 2, md: 3 }}>
        <Descriptions.Item label={t("indexCoverage.parseCoverage")}>
          {coverageKnown
            ? t("indexCoverage.parseCoverageValue", {
                parsedSheets: data.parsedSheets,
                totalSheets: data.totalSheets,
                parsedRows: data.parsedRows,
                totalRows: data.totalRows,
              })
            : t("indexCoverage.coverageUnknown")}
        </Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.vectorCoverage")}>
          {t("indexCoverage.vectorCoverageValue", {
            vectorRows: data.vectorRows,
            parsedRows: data.parsedRows,
            vectorSheets: data.vectorSheets,
            totalSheets: data.totalSheets,
          })}
        </Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.keywordRows")}>{data.keywordRows}</Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.vectorTokens")}>{data.currentVectorTokens}</Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.tables")}>{data.tableCount}</Descriptions.Item>
      </Descriptions>
      {data.parsedRows < data.totalRows && fullStorage && (
        <Typography.Text type="secondary">{t("indexCoverage.keywordHint")}</Typography.Text>
      )}
      {hasCoverageGap && (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message={t("indexCoverage.coverageGapTitle")}
          description={t("indexCoverage.coverageGapBody", {
            storageMode: tableStorageLabel(t, data.storageMode || "full"),
          })}
        />
      )}
      {data.sheets.length > 0 && (
        <Collapse
          style={{ marginTop: 12 }}
          size="small"
          items={[
            {
              key: "sheets",
              label: t("indexCoverage.sheetDetails"),
              children: (
                <Table
                  size="small"
                  rowKey="sheet"
                  pagination={data.sheets.length > 12 ? { pageSize: 12 } : false}
                  columns={sheetColumns}
                  dataSource={data.sheets}
                />
              ),
            },
          ]}
        />
      )}
      {canWrite && (
        <Space style={{ marginTop: 12 }} wrap>
          <Button
            size="small"
            loading={busy === "standard"}
            disabled={!fullStorage || data.indexMode === "standard"}
            onClick={() => void apply("standard")}
          >
            {t("indexCoverage.useStandard")}
          </Button>
          <Button
            size="small"
            loading={busy === "deep"}
            disabled={!fullStorage || data.indexMode === "deep"}
            onClick={confirmDeep}
          >
            {t("indexCoverage.useDeep")}
          </Button>
          <Button
            size="small"
            loading={busy === "lexical"}
            disabled={!fullStorage || data.indexMode === "lexical"}
            onClick={() => void apply("lexical")}
          >
            {t("indexCoverage.useLexical")}
          </Button>
        </Space>
      )}
      {fullStorage && data.tpmBudget === 0 && (
        <Alert style={{ marginTop: 12 }} type="info" showIcon message={t("indexCoverage.noTPM")} />
      )}
    </Card>
  );
}
