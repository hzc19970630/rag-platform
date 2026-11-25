import { Alert, App, Button, Descriptions, Drawer, Empty, Input, Select, Space, Table, Tag, Typography } from "antd";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { useTableAssets } from "../../api/hooks";
import type { TableAskResult, TableAskSQLResult, TableRowView } from "../../api/types";

type Action = "" | "ask" | "preview" | "execute";

function aborted(cause: unknown) {
  return cause instanceof DOMException ? cause.name === "AbortError" : (cause as Error)?.name === "AbortError";
}

export function TableQaDrawer({
  open,
  onClose,
  knowledgeBaseId,
  initialAssetId,
}: {
  open: boolean;
  onClose: () => void;
  knowledgeBaseId: string;
  initialAssetId?: string;
}) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const assets = useTableAssets(knowledgeBaseId, open);
  const [assetId, setAssetId] = useState(initialAssetId ?? "");
  const [question, setQuestion] = useState("");
  const [action, setAction] = useState<Action>("");
  const [error, setError] = useState("");
  const [answer, setAnswer] = useState<TableAskResult | null>(null);
  const [preview, setPreview] = useState<TableAskSQLResult | null>(null);
  const [executed, setExecuted] = useState<TableAskSQLResult | null>(null);
  const controller = useRef<AbortController | null>(null);

  const sheetOptions = (() => {
    const seen = new Set<string>();
    return (assets.data ?? [])
      .filter((asset) => {
        if (!asset.sheet || seen.has(asset.sheet)) return false;
        seen.add(asset.sheet);
        return true;
      })
      .map((asset) => ({ value: asset.id ?? "", label: `${asset.sheet} · ${asset.rowCount ?? 0}` }));
  })();

  function resetResults() {
    setError("");
    setAnswer(null);
    setPreview(null);
    setExecuted(null);
  }

  async function run(kind: Action) {
    if (!question.trim()) {
      message.warning(t("tableQa.questionRequired"));
      return;
    }
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setAction(kind);
    setError("");
    try {
      if (kind === "ask") {
        const result = await api<TableAskResult>(`/knowledge-bases/${knowledgeBaseId}/tables/ask`, {
          ...jsonInit({ question, assetId }), signal: request.signal,
        });
        setAnswer(result);
        setPreview(null);
        setExecuted(null);
      } else {
        const result = await api<TableAskSQLResult>(`/knowledge-bases/${knowledgeBaseId}/tables/ask/sql`, {
          ...jsonInit({ question, assetId, preview: kind === "preview" }), signal: request.signal,
        });
        if (kind === "preview") {
          setPreview(result);
          setExecuted(null);
        } else {
          setExecuted(result);
        }
        setAnswer(null);
      }
    } catch (cause) {
      if (aborted(cause)) {
        message.info(t("tableQa.cancelled"));
      } else {
        setError(cause instanceof Error ? cause.message : t("tableQa.failed"));
      }
    } finally {
      if (controller.current === request) {
        setAction("");
        controller.current = null;
      }
    }
  }

  function cancel() {
    controller.current?.abort();
    controller.current = null;
    setAction("");
  }

  function close() {
    cancel();
    onClose();
  }

  const shown = answer ?? executed?.result ?? null;
  const sql = executed ?? preview;
  const fallbackReason = sql?.fallback ? sql.fallbackReason : "";

  const columns = [
    {
      title: t("tableQa.rowNumber", { sheet: shown?.sheet ?? "" }),
      dataIndex: "rowNumber",
      key: "rowNumber",
      width: 130,
      render: (value: number) => `#${value}`,
    },
    ...((shown?.header ?? []).map((column) => ({
      title: column,
      key: column,
      render: (_: unknown, row: TableRowView) => row.cells?.[column] ?? "",
    }))),
  ];

  return (
    <Drawer
      open={open}
      onClose={close}
      title={t("tableQa.title")}
      size="min(640px, 100vw)"
      extra={action ? <Button onClick={cancel}>{t("common.cancel")}</Button> : undefined}
    >
      <Space orientation="vertical" size={12} style={{ width: "100%" }}>
        <Typography.Text type="secondary">{t("tableQa.hint")}</Typography.Text>
        <Select
          allowClear
          showSearch
          style={{ width: "100%" }}
          placeholder={t("tableQa.autoSheet")}
          value={assetId || undefined}
          options={sheetOptions}
          onChange={(value) => setAssetId(value ?? "")}
          loading={assets.isLoading}
        />
        <Input.TextArea
          rows={3}
          value={question}
          placeholder={t("tableQa.placeholder")}
          onChange={(event) => setQuestion(event.target.value)}
          onPressEnter={(event) => {
            if (!event.shiftKey) {
              event.preventDefault();
              void run("ask");
            }
          }}
        />
        <Space wrap>
          <Button type="primary" loading={action === "ask"} disabled={Boolean(action) && action !== "ask"} onClick={() => void run("ask")}>
            {t("tableQa.ask")}
          </Button>
          <Button loading={action === "preview"} disabled={Boolean(action) && action !== "preview"} onClick={() => void run("preview")}>
            {t("tableQa.previewSql")}
          </Button>
        </Space>
        {error && <Alert type="error" showIcon message={error} />}
        {fallbackReason && (
          <Alert
            type="warning"
            showIcon
            message={t("tableQa.fallbackTitle")}
            description={t("tableQa.fallbackBody", { reason: fallbackReason })}
          />
        )}
        {shown && (
          <Space orientation="vertical" size={8} style={{ width: "100%" }}>
            <Space wrap>
              {shown.route && <Tag color="blue">{shown.route}</Tag>}
              {shown.operation && <Tag>{shown.operation}</Tag>}
              {shown.column && <Tag>{shown.column}</Tag>}
              {typeof shown.confidence === "number" && <Tag>{Math.round(shown.confidence * 100)}%</Tag>}
              <Tag>{t("tableQa.matchedRows", { count: shown.matchedRows ?? 0 })}</Tag>
            </Space>
            {typeof shown.value === "number" && (
              <Typography.Title level={4} style={{ margin: 0 }}>
                {shown.value}
              </Typography.Title>
            )}
            {(shown.rows?.length ?? 0) > 0 && (
              <Table<TableRowView>
                size="small"
                rowKey={(row) => String(row.rowNumber)}
                pagination={(shown.rows?.length ?? 0) > 10 ? { pageSize: 10 } : false}
                scroll={{ x: "max-content" }}
                columns={columns}
                dataSource={shown.rows ?? []}
              />
            )}
            {(shown.sample?.length ?? 0) > 0 && (
              <pre style={preStyle}>{shown.sample?.join("\n")}</pre>
            )}
            {(shown.notes?.length ?? 0) > 0 && (
              <ul style={{ margin: 0, paddingInlineStart: 18 }}>
                {shown.notes?.map((note) => <li key={note}><Typography.Text type="secondary">{note}</Typography.Text></li>)}
              </ul>
            )}
            {!shown.value && !(shown.rows?.length ?? 0) && !(shown.sample?.length ?? 0) && shown.value !== 0 && (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("tableQa.noRows")} />
            )}
          </Space>
        )}
        {sql && sql.sql && (
          <Space orientation="vertical" size={8} style={{ width: "100%" }}>
            <Typography.Text strong>{t("tableQa.sqlPreview")}</Typography.Text>
            <Descriptions size="small" column={2}>
              <Descriptions.Item label={t("tableQa.operation")}>{sql.plan?.operation ?? "-"}</Descriptions.Item>
              <Descriptions.Item label={t("tableQa.column")}>{sql.plan?.column ?? "-"}</Descriptions.Item>
              <Descriptions.Item label={t("tableQa.filters")}>
                {sql.plan?.filters?.map((filter) => `${filter.column} ${filter.op} ${filter.value}`).join(", ") || "-"}
              </Descriptions.Item>
              <Descriptions.Item label={t("tableQa.estimatedRows")}>{sql.estimatedRows ?? 0}</Descriptions.Item>
              <Descriptions.Item label={t("tableQa.confidence")}>
                {Math.round((sql.plan?.confidence ?? 0) * 100)}%
              </Descriptions.Item>
            </Descriptions>
            <pre style={preStyle}>{sql.sql}</pre>
            {preview && !executed && !preview.fallback && (
              <Button
                type="primary"
                loading={action === "execute"}
                disabled={Boolean(action) && action !== "execute"}
                onClick={() => void run("execute")}
              >
                {t("tableQa.executeSql")}
              </Button>
            )}
            {executed && (
              <Typography.Text type="secondary">{t("tableQa.executed", { milliseconds: executed.elapsedMs ?? 0 })}</Typography.Text>
            )}
          </Space>
        )}
      </Space>
    </Drawer>
  );
}

const preStyle = {
  margin: 0,
  padding: 12,
  borderRadius: 8,
  background: "rgba(0, 0, 0, 0.04)",
  whiteSpace: "pre-wrap" as const,
  wordBreak: "break-word" as const,
  fontSize: 12,
};
