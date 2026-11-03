import { ArrowLeftOutlined, FileSearchOutlined, ReloadOutlined, SyncOutlined } from "@ant-design/icons";
import { Alert, App, Button, Card, Flex, List, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, openDocumentPreview } from "../api/client";
import {
  queryKeys,
  useChunk,
  useChunks,
  useDocumentContent,
  useDocumentStatus,
  useDocumentVersions,
} from "../api/hooks";
import type { DocumentVersionView } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { StatusBadge } from "../components/StatusBadge";
import { usePermissions } from "../session";
import { formatSize, formatTime } from "../utils/format";

export function DocumentPage() {
  const { documentId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const from = searchParams.get("from") ?? "";
  const chunkId = searchParams.get("chunk") ?? "";
  const { t } = useTranslation();
  const { message } = App.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { canWrite } = usePermissions();
  const status = useDocumentStatus(documentId);
  const versions = useDocumentVersions(documentId);
  const contentReady = Boolean(status.data && status.data.document.id === documentId && status.data.contentReady);
  const content = useDocumentContent(documentId, contentReady);
  const chunks = useChunks(documentId, contentReady);
  const focused = useChunk(chunkId || undefined);
  const [restoring, setRestoring] = useState("");
  const [reparsing, setReparsing] = useState(false);

  if (status.isLoading) return <LoadingBlock />;
  if (status.error) return <QueryFallback error={status.error} retry={() => void status.refetch()} />;
  const data = status.data;
  if (!data) return null;

  function back() {
    navigate(from ? `/knowledge-bases/${from}` : "/knowledge-bases");
  }

  async function reparse() {
    setReparsing(true);
    try {
      await api(`/documents/${documentId}/reparse`, { method: "POST" });
      message.success(t("document.reparseStarted"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.documentStatus(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.documentVersions(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.documentContent(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.chunks(documentId) }),
      ]);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("document.reparseFailed"));
    } finally {
      setReparsing(false);
    }
  }

  async function activate(version: DocumentVersionView) {
    setRestoring(version.id);
    try {
      await api(`/documents/${documentId}/versions/${version.id}/activate`, { method: "POST" });
      message.success(t("document.restoreStarted", { version: version.version }));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.documentStatus(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.documentVersions(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.documentContent(documentId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.chunks(documentId) }),
      ]);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("document.restoreFailed"));
    } finally {
      setRestoring("");
    }
  }

  async function preview() {
    try {
      await openDocumentPreview(documentId);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("document.previewFailed"));
    }
  }

  const versionColumns: TableColumnsType<DocumentVersionView> = [
    {
      title: t("document.version"),
      key: "version",
      render: (_, version) => (
        <Space size={8} wrap>
          <Typography.Text strong>v{version.version}</Typography.Text>
          {version.current && <Tag color="green">{t("document.current")}</Tag>}
          <StatusBadge status={version.current ? "ACTIVE" : version.status} />
        </Space>
      ),
    },
    { title: t("document.size"), key: "size", width: 110, render: (_, version) => formatSize(version.sizeBytes) },
    { title: t("common.createdAt"), key: "createdAt", width: 190, render: (_, version) => formatTime(version.createdAt) },
    {
      title: t("common.actions"),
      key: "actions",
      width: 180,
      render: (_, version) => canWrite && !version.current && version.restorable
        ? <Button size="small" loading={restoring === version.id} onClick={() => void activate(version)}>{t("document.restore")}</Button>
        : null,
    },
  ];

  const chunkItems = chunks.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <div className="page">
      <PageHeader
        breadcrumbs={[
          { title: t("knowledgeBase.title"), to: "/knowledge-bases" },
          ...(from ? [{ title: t("knowledgeBase.tabs.documents"), to: `/knowledge-bases/${from}` }] : []),
          { title: data.document.filename },
        ]}
        title={data.document.filename}
        description={`${t("document.version")} ${data.version.id} · ${data.version.status}${data.version.errorCode ? ` · ${data.version.errorCode}` : ""}`}
        extra={(
          <Space wrap>
            <Button icon={<ArrowLeftOutlined />} onClick={back}>{t("common.back")}</Button>
            {canWrite && <Button icon={<ReloadOutlined />} loading={reparsing} onClick={() => void reparse()}>{t("document.reparse")}</Button>}
            {data.previewable && <Button icon={<FileSearchOutlined />} onClick={() => void preview()}>{t("document.original")}</Button>}
          </Space>
        )}
      />

      <Card className="detail-card" title={t("document.status")}>
        <ol className="stage-list">
          {data.stages.map((stage) => (
            <li key={stage.stage}>
              <span className="stage-name">{stage.stage}</span>
              <StatusBadge status={stage.status} />
              <span className="stage-attempts">{t("document.attempts", { count: stage.attempts })}</span>
              {stage.errorCode && <Typography.Text type="danger">{stage.errorCode}</Typography.Text>}
            </li>
          ))}
        </ol>
        {!data.contentReady && (
          <Alert
            className="settings-alert"
            type={data.version.status === "FAILED" ? "error" : "info"}
            showIcon
            message={data.version.status === "FAILED"
              ? `${t("document.failed")}: ${data.version.errorCode ?? "unknown"}${data.version.errorMessage ? ` · ${data.version.errorMessage}` : ""}`
              : t("document.processing")}
          />
        )}
      </Card>

      {focused.data && (
        <Card className="detail-card highlight" title={`${t("document.citation")} · ${focused.data.chunkId}`}>
          <Typography.Text type="secondary">
            {focused.data.headingPath || t("document.noHeading")} · {t("document.position", { page: focused.data.pageNumber })} · {focused.data.sourcePath || t("document.noSource")}
          </Typography.Text>
          <Typography.Paragraph className="quote">{focused.data.content}</Typography.Paragraph>
        </Card>
      )}
      {focused.error && <Alert type="warning" showIcon message={t("document.citationLoadFailed")} />}

      <Card className="detail-card" title={t("document.versions")} extra={<Tag>{versions.data?.items.length ?? 0}</Tag>}>
        {versions.isLoading && <LoadingBlock />}
        {versions.error && <QueryFallback error={versions.error} retry={() => void versions.refetch()} />}
        {versions.data && (
          <Table<DocumentVersionView>
            rowKey="id"
            size="small"
            pagination={false}
            columns={versionColumns}
            dataSource={versions.data.items}
            scroll={{ x: true }}
          />
        )}
      </Card>

      {contentReady && (
        <>
          <Card className="detail-card" title={t("document.content")} extra={content.data ? <Tag>{t("document.blockCount", { count: content.data.blocks.length })}</Tag> : undefined}>
            {content.isLoading && <LoadingBlock />}
            {content.error && <QueryFallback error={content.error} retry={() => void content.refetch()} />}
            {content.data && (
              <>
                {content.data.title && (
                  <Typography.Paragraph type="secondary">
                    {t("document.title")}: {content.data.title} · {t("document.language")}: {content.data.language || t("document.unknown")}
                  </Typography.Paragraph>
                )}
                <div className="blocks">
                  {content.data.blocks.map((block) => (
                    <div className="block" key={block.blockId}>
                      <Typography.Text type="secondary">
                        {block.headingPath || t("document.noHeading")} · {t("document.position", { page: block.pageNumber })} · {block.kind}
                      </Typography.Text>
                      <Typography.Paragraph>{block.text}</Typography.Paragraph>
                    </div>
                  ))}
                </div>
              </>
            )}
          </Card>

          <Card className="detail-card" title={t("document.chunks")} extra={<Tag>{t("document.chunkCount", { count: chunkItems.length })}</Tag>}>
            {chunks.isLoading && <LoadingBlock />}
            {chunks.error && <QueryFallback error={chunks.error} retry={() => void chunks.refetch()} />}
            {!chunks.isLoading && chunkItems.length === 0 && <Alert type="info" showIcon message={t("document.noChunks")} />}
            <List
              className="chunk-list"
              dataSource={chunkItems}
              renderItem={(chunk) => (
                <List.Item className={chunk.chunkId === chunkId ? "chunk focused" : "chunk"}>
                  <div className="chunk-body">
                    <Space size={8} wrap>
                      <Typography.Text code>{chunk.chunkId}</Typography.Text>
                      <Typography.Text type="secondary">
                        #{chunk.ordinal} · {t("document.position", { page: chunk.pageNumber })} · {chunk.headingPath || t("document.noHeading")}
                      </Typography.Text>
                    </Space>
                    <Typography.Paragraph className="chunk-text">{chunk.content}</Typography.Paragraph>
                  </div>
                </List.Item>
              )}
            />
            {chunks.hasNextPage && (
              <Flex justify="center" className="load-more">
                <Button loading={chunks.isFetchingNextPage} onClick={() => void chunks.fetchNextPage()}>{t("document.loadMore")}</Button>
              </Flex>
            )}
          </Card>
        </>
      )}
      {!data.contentReady && data.version.status !== "FAILED" && (
        <Card className="detail-card">
          <Flex align="center" gap={8}><SyncOutlined spin /> <Typography.Text type="secondary">{t("document.processing")}</Typography.Text></Flex>
        </Card>
      )}
    </div>
  );
}
