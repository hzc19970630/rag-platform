import { App, Alert, Button, Card, Collapse, Descriptions, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useDocumentIndexEstimate, useDocumentStatus } from "../../api/hooks";
import { TableQaDrawer } from "./TableQaDrawer";
import { tableMaterializeLabel, tableMaterializeTone, tableParseStatusLabel, tableStorageLabel } from "../../utils/enums";

const LABELS: Record<string, string> = {
  standard: "indexCoverage.modeStandard",
  deep: "indexCoverage.modeDeep",
  lexical: "indexCoverage.modeLexical",
};

type SheetRow = {
  sheet: string;
  parseStatus: string;
  indexMode: string;
  parsedRows: number;
  totalRows: number;
  materializeStatus: string;
  materializeError: string;
  materializedRows: number;
  byteSize: number;
};

function formatBytes(value: number): string {
  if (!value || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  const digits = size >= 10 || unit === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${units[unit]}`;
}

export function IndexCoverageCard({ documentId, knowledgeBaseId, canWrite }: { documentId: string; knowledgeBaseId: string; canWrite: boolean }) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState("");
  const [qaOpen, setQaOpen] = useState(false);
  const status = useDocumentStatus(documentId);
  const ready = Boolean(status.data?.contentReady);
  const estimate = useDocumentIndexEstimate(documentId, ready);
  const data = estimate.data;
  if (!data || data.tableCount === 0) return null;

  const fullStorage = !data.storageMode || data.storageMode === "full";
  const coverageKnown = data.coverageKnown !== false;
  const totalSheets = data.totalSheets || data.sheets.length;
  const readySheets = data.materializeReadySheets ?? 0;
  const failedSheets = data.materializeFailedSheets ?? 0;
  const missingSheets = data.materializeMissingSheets ?? 0;
  const partialSheets = data.materializePartialSheets ?? 0;
  const versionFailed = status.data?.version?.status === "FAILED";
  const needsRebuild = failedSheets > 0 || missingSheets > 0 || versionFailed;
  const hasCoverageGap = Boolean(data.parseTruncated) || failedSheets > 0 || missingSheets > 0 || partialSheets > 0;
  const failedSheetDetails = data.sheets.filter((sheet) => sheet.materializeStatus === "failed");
  const skippedMedia = (data.skippedImages ?? 0) + (data.skippedCharts ?? 0) + (data.skippedNotes ?? 0);

  function invalidateCoverage() {
    return Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.documentStatus(documentId) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.documentVersions(documentId) }),
      queryClient.invalidateQueries({ queryKey: ["document-index-estimate", documentId] }),
    ]);
  }

  async function apply(mode: string) {
    setBusy(mode);
    try {
      await api(`/documents/${documentId}/index-mode`, jsonInit({ mode }));
      message.success(t("indexCoverage.switchStarted"));
      await invalidateCoverage();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("indexCoverage.switchFailed"));
    } finally {
      setBusy("");
    }
  }

  async function rebuildTables() {
    setBusy("rebuild");
    try {
      await api(`/documents/${documentId}/tables/rebuild`, { method: "POST" });
      message.success(t("indexCoverage.rebuildStarted"));
      await invalidateCoverage();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("indexCoverage.rebuildFailed"));
    } finally {
      setBusy("");
    }
  }

  function confirmRebuild() {
    modal.confirm({
      title: t("indexCoverage.rebuildConfirmTitle"),
      content: t("indexCoverage.rebuildConfirmBody"),
      okText: t("indexCoverage.rebuildTables"),
      cancelText: t("common.cancel"),
      onOk: rebuildTables,
    });
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
      title: t("indexCoverage.materializeColumn"),
      key: "materialize",
      render: (_: unknown, row: SheetRow) => (
        <Space orientation="vertical" size={0}>
          <Tag color={tableMaterializeTone(row.materializeStatus)}>
            {tableMaterializeLabel(t, row.materializeStatus)}
          </Tag>
          {row.materializeStatus === "failed" && row.materializeError && (
            <Typography.Text type="danger" ellipsis={{ tooltip: row.materializeError }} style={{ maxWidth: 220 }}>
              {row.materializeError}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("indexCoverage.rows"),
      key: "rows",
      render: (_: unknown, row: SheetRow) => `${row.parsedRows}/${row.totalRows}`,
    },
    {
      title: t("indexCoverage.materializeRowsColumn"),
      key: "materializeRows",
      render: (_: unknown, row: SheetRow) =>
        row.materializeStatus === "ready" || row.materializeStatus === "partial"
          ? `${row.materializedRows} · ${formatBytes(row.byteSize)}`
          : "-",
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
          {knowledgeBaseId && (
            <Button size="small" onClick={() => setQaOpen(true)}>
              {t("tableQa.open")}
            </Button>
          )}
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
        <Descriptions.Item label={t("indexCoverage.materializeCoverage")}>
          {t("indexCoverage.materializeCoverageValue", {
            ready: readySheets,
            total: totalSheets,
            rows: data.materializeRows,
            size: formatBytes(data.materializeBytes),
          })}
          {(partialSheets > 0 || failedSheets > 0 || missingSheets > 0) && (
            <Typography.Text type="secondary">
              {" "}
              ({t("indexCoverage.materializeGapValue", {
                partial: partialSheets,
                failed: failedSheets,
                missing: missingSheets,
              })})
            </Typography.Text>
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.keywordRows")}>{data.keywordRows}</Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.vectorTokens")}>{data.currentVectorTokens}</Descriptions.Item>
        <Descriptions.Item label={t("indexCoverage.tables")}>{data.tableCount}</Descriptions.Item>
        {skippedMedia > 0 && (
          <Descriptions.Item label={t("indexCoverage.skippedMedia")}>
            {t("indexCoverage.skippedMediaValue", {
              images: data.skippedImages ?? 0,
              charts: data.skippedCharts ?? 0,
              notes: data.skippedNotes ?? 0,
            })}
          </Descriptions.Item>
        )}
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
      {!fullStorage && !hasCoverageGap && (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={t("indexCoverage.columnarQueryHint")}
        />
      )}
      {skippedMedia > 0 && (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={t("indexCoverage.skippedMediaTitle")}
          description={t("indexCoverage.skippedMediaBody")}
        />
      )}
      {failedSheets > 0 && (
        <Alert
          style={{ marginTop: 12 }}
          type="error"
          showIcon
          message={t("indexCoverage.materializeFailedTitle")}
          description={
            <Space orientation="vertical" size={2}>
              <Typography.Text>{t("indexCoverage.materializeFailedBody")}</Typography.Text>
              {failedSheetDetails.slice(0, 5).map((sheet) => (
                <Typography.Text key={sheet.sheet || "default"} type="danger">
                  {sheet.sheet || t("indexCoverage.defaultSheet")}: {sheet.materializeError || "-"}
                </Typography.Text>
              ))}
            </Space>
          }
        />
      )}
      {missingSheets > 0 && (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message={t("indexCoverage.materializeMissingTitle")}
          description={t("indexCoverage.materializeMissingBody")}
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
          {needsRebuild && (
            <Button size="small" loading={busy === "rebuild"} onClick={confirmRebuild}>
              {t("indexCoverage.rebuildTables")}
            </Button>
          )}
        </Space>
      )}
      {fullStorage && data.tpmBudget === 0 && (
        <Alert style={{ marginTop: 12 }} type="info" showIcon message={t("indexCoverage.noTPM")} />
      )}
      <TableQaDrawer open={qaOpen} onClose={() => setQaOpen(false)} knowledgeBaseId={knowledgeBaseId} />
    </Card>
  );
}