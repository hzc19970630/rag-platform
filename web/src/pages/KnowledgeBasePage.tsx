import { ArrowLeftOutlined, MessageOutlined, ReloadOutlined, SettingOutlined, UploadOutlined } from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Flex,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ChangeEvent } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, uploadDocument } from "../api/client";
import { queryKeys, terminalVersionStatuses, useConfiguration, useDocuments, useKnowledgeBase } from "../api/hooks";
import type { DocumentSummary } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { StatusBadge } from "../components/StatusBadge";
import { SettingsDrawer } from "../features/settings/SettingsDrawer";
import { MembersSettings } from "../features/settings/MembersSettings";
import { ProjectionPanel } from "../features/settings/ProjectionPanel";
import { usePermissions } from "../session";
import { formatSize, formatTime } from "../utils/format";

const accept = ".pdf,.docx,.xlsx,.pptx,.md,.txt,.html,.htm";

export function KnowledgeBasePage() {
  const { knowledgeBaseId = "" } = useParams();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const knowledgeBase = useKnowledgeBase(knowledgeBaseId);
  const configuration = useConfiguration(knowledgeBaseId, Boolean(knowledgeBaseId));
  const [tab, setTab] = useState("documents");
  const { canWrite, canManage, canDelete, canChat, canOperate } = usePermissions();
  const queryClient = useQueryClient();
  const settingsOpen = searchParams.get("settings") === "1";

  if (knowledgeBase.isLoading) return <LoadingBlock />;
  if (knowledgeBase.error) return <QueryFallback error={knowledgeBase.error} retry={() => void knowledgeBase.refetch()} />;
  const kb = knowledgeBase.data;
  if (!kb) return null;

  function openSettings() {
    const next = new URLSearchParams(searchParams);
    next.set("settings", "1");
    setSearchParams(next, { replace: true });
  }

  function closeSettings() {
    const next = new URLSearchParams(searchParams);
    next.delete("settings");
    setSearchParams(next, { replace: true });
  }

  function changed() {
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases }),
      queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) }),
    ]);
  }

  const strategy = configuration.data?.indexingStrategy;
  const projectionEnabled = Boolean(strategy?.wiki_enabled || strategy?.graph_enabled);
  const activeTab = tab === "members" && !canManage ? "documents" : tab === "projection" && !projectionEnabled ? "documents" : tab;

  const tabItems = [
    {
      key: "documents",
      label: t("knowledgeBase.tabs.documents"),
      children: <DocumentsTab knowledgeBaseId={knowledgeBaseId} canWrite={canWrite} canDelete={canDelete} />,
    },
    ...(canManage ? [{
      key: "members",
      label: t("knowledgeBase.tabs.members"),
      children: (
        <MembersSettings
          knowledgeBaseId={knowledgeBaseId}
          ownerId={kb.ownerId}
          canManage={canManage}
          onChanged={changed}
        />
      ),
    }] : []),
    ...(projectionEnabled ? [{
      key: "projection",
      label: t("knowledgeBase.tabs.projection"),
      children: <ProjectionPanel knowledgeBaseId={knowledgeBaseId} canWrite={canWrite} embedded />,
    }] : []),
  ];

  return (
    <div className="page">
      <PageHeader
        breadcrumbs={[{ title: t("knowledgeBase.title"), to: "/knowledge-bases" }, { title: kb.name }]}
        title={kb.name}
        description={kb.description || t("knowledgeBase.noDescription")}
        extra={(
          <Space wrap>
            <Button icon={<ArrowLeftOutlined />} onClick={() => navigate("/knowledge-bases")}>{t("common.back")}</Button>
            {canChat && <Button icon={<MessageOutlined />} onClick={() => navigate(`/chat?kb=${encodeURIComponent(kb.id)}`)}>{t("knowledgeBase.chat")}</Button>}
            <Button icon={<SettingOutlined />} onClick={openSettings} disabled={!configuration.data}>{t("knowledgeBase.settings")}</Button>
          </Space>
        )}
      />
      <Space size={8} wrap className="kb-summary">
        <Tag>ACL {kb.aclVersion}</Tag>
        <Typography.Text type="secondary">{t("knowledgeBase.updated")} {formatTime(kb.updatedAt)}</Typography.Text>
        <Typography.Text type="secondary" copyable={{ text: kb.id }} className="kb-id">{kb.id}</Typography.Text>
      </Space>
      {configuration.error && <Alert type="error" showIcon message={t("settings.configLoadFailed")} description={configuration.error instanceof Error ? configuration.error.message : undefined} className="settings-alert" />}
      <Tabs activeKey={activeTab} onChange={setTab} items={tabItems} className="kb-tabs" />
      {configuration.data && (
        <SettingsDrawer
          open={settingsOpen}
          onClose={closeSettings}
          knowledgeBase={kb}
          configuration={configuration.data}
          canWrite={canWrite}
          canManage={canManage}
          canDelete={canDelete}
          canOperate={canOperate}
          onChanged={changed}
        />
      )}
    </div>
  );
}

function DocumentsTab({ knowledgeBaseId, canWrite, canDelete }: { knowledgeBaseId: string; canWrite: boolean; canDelete: boolean }) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const documents = useDocuments(knowledgeBaseId);
  const [uploading, setUploading] = useState(false);
  const [replacing, setReplacing] = useState("");
  const uploadRef = useRef<HTMLInputElement>(null);
  const replaceRef = useRef<HTMLInputElement>(null);
  const replaceTarget = useRef("");
  const items = documents.data?.pages.flatMap((page) => page.items) ?? [];
  const processing = items.some((item) => !terminalVersionStatuses.has(item.version.status));

  function refresh() {
    return documents.refetch();
  }

  async function upload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setUploading(true);
    try {
      await uploadDocument(knowledgeBaseId, file);
      message.success(t("document.uploaded", { name: file.name }));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("document.uploadFailed"));
    } finally {
      setUploading(false);
    }
  }

  async function replace(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    const documentId = replaceTarget.current;
    replaceTarget.current = "";
    if (!file || !documentId) return;
    const previous = items.find((item) => item.document.id === documentId);
    setReplacing(documentId);
    try {
      const result = await uploadDocument(knowledgeBaseId, file, documentId);
      if (previous && result.version.id === previous.document.latestVersionId) {
        message.info(t("document.replaceUnchanged", { name: file.name }));
      } else {
        message.success(t("document.replaceStarted", { name: file.name }));
      }
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("document.replaceFailed"));
    } finally {
      setReplacing("");
    }
  }

  function startReplace(documentId: string) {
    replaceTarget.current = documentId;
    replaceRef.current?.click();
  }

  function remove(item: DocumentSummary) {
    modal.confirm({
      title: t("document.deleteTitle", { name: item.document.filename }),
      content: t("document.deleteConfirm"),
      okText: t("common.delete"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        try {
          await api(`/documents/${item.document.id}`, { method: "DELETE" });
          message.success(t("document.deleted", { name: item.document.filename }));
          await refresh();
          await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) });
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("document.deleteFailed"));
        }
      },
    });
  }

  const columns: TableColumnsType<DocumentSummary> = [
    {
      title: t("document.name"),
      key: "name",
      render: (_, item) => (
        <div className="doc-name">
          <Typography.Link onClick={() => navigate(`/documents/${item.document.id}?from=${encodeURIComponent(knowledgeBaseId)}`)}>
            {item.document.filename}
          </Typography.Link>
          <Typography.Text type="secondary" className="doc-id">{item.document.id}</Typography.Text>
        </div>
      ),
    },
    { title: t("document.status"), key: "status", width: 160, render: (_, item) => <StatusBadge status={item.version.status} /> },
    { title: t("document.version"), key: "version", width: 120, render: (_, item) => `v${item.version.version} · ${formatSize(item.version.sizeBytes)}` },
    { title: t("common.updatedAt"), key: "updated", width: 190, render: (_, item) => formatTime(item.document.updatedAt) },
    {
      title: t("common.actions"),
      key: "actions",
      width: 230,
      render: (_, item) => (
        <Space wrap>
          <Button size="small" onClick={() => navigate(`/documents/${item.document.id}?from=${encodeURIComponent(knowledgeBaseId)}`)}>{t("knowledgeBase.details")}</Button>
          {canWrite && <Button size="small" loading={replacing === item.document.id} onClick={() => startReplace(item.document.id)}>{t("knowledgeBase.replace")}</Button>}
          {canDelete && <Button size="small" danger onClick={() => remove(item)}>{t("common.delete")}</Button>}
        </Space>
      ),
    },
  ];

  return (
    <div className="tab-panel">
      <Flex justify="space-between" align="center" gap={12} wrap="wrap" className="tab-toolbar">
        <Space>
          <Typography.Text strong>{t("knowledgeBase.tabs.documents")}</Typography.Text>
          <Tag>{items.length}</Tag>
          {processing && <Tag color="processing">{t("document.processingTag")}</Tag>}
        </Space>
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void refresh()}>{t("common.refresh")}</Button>
          {canWrite && (
            <>
              <Button size="small" type="primary" icon={<UploadOutlined />} loading={uploading} onClick={() => uploadRef.current?.click()}>
                {t("knowledgeBase.upload")}
              </Button>
              <input ref={uploadRef} type="file" accept={accept} hidden onChange={(event) => void upload(event)} />
              <input ref={replaceRef} type="file" accept={accept} hidden onChange={(event) => void replace(event)} />
            </>
          )}
        </Space>
      </Flex>
      <Typography.Paragraph type="secondary">{t("knowledgeBase.uploadHint")}</Typography.Paragraph>
      {documents.isLoading && <LoadingBlock />}
      {documents.error && <QueryFallback error={documents.error} retry={() => void refresh()} />}
      {!documents.isLoading && items.length === 0 && (
        <EmptyState
          description={t("knowledgeBase.noDocuments")}
          action={canWrite ? () => uploadRef.current?.click() : undefined}
          actionLabel={canWrite ? t("knowledgeBase.upload") : undefined}
        />
      )}
      {items.length > 0 && (
        <Table<DocumentSummary>
          rowKey={(item) => item.document.id}
          size="middle"
          columns={columns}
          dataSource={items}
          pagination={false}
          scroll={{ x: true }}
          expandable={{
            expandedRowRender: (item) => (
              <div className="doc-expanded">
                <Typography.Text type="secondary">{t("document.version")} {item.version.id}</Typography.Text>
                {item.version.errorCode && <Typography.Text type="danger">{item.version.errorCode}{item.version.errorMessage ? ` · ${item.version.errorMessage}` : ""}</Typography.Text>}
              </div>
            ),
          }}
        />
      )}
      {documents.hasNextPage && (
        <Flex justify="center" className="load-more">
          <Button loading={documents.isFetchingNextPage} onClick={() => void documents.fetchNextPage()}>{t("knowledgeBase.loadMore")}</Button>
        </Flex>
      )}
    </div>
  );
}
