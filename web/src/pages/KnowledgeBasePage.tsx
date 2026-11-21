import {
  ArrowLeftOutlined,
  CloseOutlined,
  DeleteOutlined,
  FolderOutlined,
  HistoryOutlined,
  MessageOutlined,
  PlusOutlined,
  ReloadOutlined,
  SettingOutlined,
  SyncOutlined,
  TagsOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Drawer,
  Flex,
  Input,
  Modal,
  Popover,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, downloadDocument, jsonInit, uploadDocument } from "../api/client";
import { UploadQueueModal } from "../features/upload/UploadQueueModal";
import { useUploadQueue } from "../features/upload/useUploadQueue";
import { createConversation } from "../api/conversations";
import {
  queryKeys,
  terminalVersionStatuses,
  useConfiguration,
  useConversations,
  useDocuments,
  useFolders,
  useKnowledgeBase,
  useTags,
} from "../api/hooks";
import type { BatchDocumentsResult, DocumentPage, DocumentSummary, Folder, Tag as TagView } from "../api/types";
import { ChatConversation } from "../components/ChatConversation";
import { EmptyState } from "../components/EmptyState";
import { DocumentStatusBadge } from "../components/DocumentStatusBadge";
import { FOLDER_ALL, FOLDER_ROOT, FolderTree } from "../components/FolderTree";
import { FolderTreeSelect } from "../components/FolderTreeSelect";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { SettingsDrawer } from "../features/settings/SettingsDrawer";
import { MembersSettings } from "../features/settings/MembersSettings";
import { ProjectionPanel } from "../features/settings/ProjectionPanel";
import { usePermissions } from "../session";
import { formatSize, formatTime } from "../utils/format";

const accept = ".pdf,.docx,.xlsx,.csv,.pptx,.md,.txt,.html,.htm";
const EMPTY_FOLDERS: Folder[] = [];
const EMPTY_TAGS: TagView[] = [];
const CHAT_PANEL_QUERY = "(min-width: 1101px)";

function folderSubtreeIds(folders: Folder[], rootId: string): string[] {
  const ids = new Set([rootId]);
  let added = true;
  while (added) {
    added = false;
    for (const folder of folders) {
      if (folder.parentId && ids.has(folder.parentId) && !ids.has(folder.id)) {
        ids.add(folder.id);
        added = true;
      }
    }
  }
  return [...ids];
}

function useMinWidth(query: string) {
  const [matches, setMatches] = useState(() => window.matchMedia?.(query).matches ?? true);
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const onChange = () => setMatches(media.matches);
    media.addEventListener?.("change", onChange);
    return () => media.removeEventListener?.("change", onChange);
  }, [query]);
  return matches;
}

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
  const { message } = App.useApp();
  const conversations = useConversations();
  const wideChat = useMinWidth(CHAT_PANEL_QUERY);
  const [chatOpen, setChatOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [creatingSession, setCreatingSession] = useState(false);
  const [activeSessionId, setActiveSessionId] = useState("");

  const kbConversations = useMemo(
    () => (conversations.data ?? [])
      .filter((item) => item.knowledgeBaseId === knowledgeBaseId)
      .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt)),
    [conversations.data, knowledgeBaseId],
  );
  const activeConversation = kbConversations.find((item) => item.id === activeSessionId) ?? kbConversations[0];

  async function startSession() {
    if (creatingSession) return;
    setCreatingSession(true);
    try {
      const created = await createConversation(knowledgeBaseId);
      setActiveSessionId(created.id);
      setHistoryOpen(false);
      await queryClient.invalidateQueries({ queryKey: queryKeys.conversations });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("chat.createFailed"));
    } finally {
      setCreatingSession(false);
    }
  }

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
      children: <DocumentsTab knowledgeBaseId={knowledgeBaseId} knowledgeBaseName={kb.name} canWrite={canWrite} canDelete={canDelete} />,
    },
    ...(canManage ? [{
      key: "members",
      label: t("knowledgeBase.tabs.members"),
      children: (
        <div className="tab-panel kb-settings-tab">
          <MembersSettings
            knowledgeBaseId={knowledgeBaseId}
            ownerId={kb.ownerId}
            canManage={canManage}
            onChanged={changed}
          />
        </div>
      ),
    }] : []),
    ...(projectionEnabled ? [{
      key: "projection",
      label: t("knowledgeBase.tabs.projection"),
      children: (
        <div className="tab-panel kb-settings-tab">
          <ProjectionPanel knowledgeBaseId={knowledgeBaseId} canWrite={canWrite} embedded />
        </div>
      ),
    }] : []),
  ];

  const historyContent = (
    <div className="kb-chat-history" data-testid="kb-chat-history-list">
      {kbConversations.length === 0 && (
        <Typography.Text type="secondary">{t("chat.noConversations")}</Typography.Text>
      )}
      {kbConversations.map((conversation) => (
        <button
          key={conversation.id}
          type="button"
          className={conversation.id === activeConversation?.id ? "chat-session kb-chat-history-item active" : "chat-session kb-chat-history-item"}
          onClick={() => {
            setActiveSessionId(conversation.id);
            setHistoryOpen(false);
          }}
        >
          <Typography.Text ellipsis>{conversation.title || t("chat.untitled")}</Typography.Text>
        </button>
      ))}
    </div>
  );

  const chatBody = (
    <>
      <div className="kb-chat-head">
        <Typography.Text strong ellipsis className="kb-chat-title">
          {activeConversation ? activeConversation.title || t("chat.untitled") : t("knowledgeBase.chat")}
        </Typography.Text>
        <Space size={2}>
          <Tooltip title={t("chat.newConversation")}>
            <Button
              type="text"
              size="small"
              icon={<PlusOutlined />}
              loading={creatingSession}
              aria-label={t("chat.newConversation")}
              data-testid="kb-chat-new"
              onClick={() => void startSession()}
            />
          </Tooltip>
          <Popover
            trigger="click"
            placement="bottomRight"
            open={historyOpen}
            onOpenChange={setHistoryOpen}
            title={t("chat.history")}
            content={historyContent}
          >
            <Tooltip title={t("chat.history")}>
              <Button
                type="text"
                size="small"
                icon={<HistoryOutlined />}
                aria-label={t("chat.history")}
                data-testid="kb-chat-history"
              />
            </Tooltip>
          </Popover>
          <Tooltip title={t("chat.collapsePanel")}>
            <Button
              type="text"
              size="small"
              icon={<CloseOutlined />}
              aria-label={t("chat.collapsePanel")}
              onClick={() => setChatOpen(false)}
            />
          </Tooltip>
        </Space>
      </div>
      <div className="kb-chat-body">
        {conversations.isLoading ? <LoadingBlock /> : conversations.error ? (
          <QueryFallback error={conversations.error} retry={() => void conversations.refetch()} />
        ) : activeConversation ? (
          <ChatConversation conversation={activeConversation} compact />
        ) : (
          <div className="kb-chat-empty">
            <EmptyState
              description={t("chat.noConversations")}
              action={creatingSession ? undefined : () => void startSession()}
              actionLabel={t("chat.newConversation")}
            />
          </div>
        )}
      </div>
    </>
  );

  return (
    <div className="page kb-detail-page">
      <PageHeader
        breadcrumbs={[{ title: t("knowledgeBase.title"), to: "/knowledge-bases" }, { title: kb.name }]}
        title={kb.name}
        description={kb.description || t("knowledgeBase.noDescription")}
        extra={(
          <Space wrap>
            <Button icon={<ArrowLeftOutlined />} onClick={() => navigate("/knowledge-bases")}>{t("common.back")}</Button>
            {canChat && (
              <Button
                icon={<MessageOutlined />}
                data-testid="kb-chat-toggle"
                aria-expanded={chatOpen}
                onClick={() => setChatOpen((open) => !open)}
              >
                {t("knowledgeBase.chat")}
              </Button>
            )}
            <Button icon={<SettingOutlined />} onClick={openSettings} disabled={!configuration.data}>{t("knowledgeBase.settings")}</Button>
          </Space>
        )}
      />
      <Space size={8} wrap className="kb-summary">
        <Typography.Text type="secondary">{t("knowledgeBase.updated")} {formatTime(kb.updatedAt)}</Typography.Text>
      </Space>
      {configuration.error && <Alert type="error" showIcon message={t("settings.configLoadFailed")} description={configuration.error instanceof Error ? configuration.error.message : undefined} className="settings-alert" />}
      <div className="kb-detail-body">
        <Tabs activeKey={activeTab} onChange={setTab} items={tabItems} className="kb-tabs" />
        {canChat && wideChat && chatOpen && (
          <aside className="kb-chat-panel" data-testid="kb-chat-panel" aria-label={t("knowledgeBase.chat")}>
            {chatBody}
          </aside>
        )}
      </div>
      {canChat && !wideChat && (
        <Drawer
          open={chatOpen}
          onClose={() => setChatOpen(false)}
          placement="right"
          size={360}
          title={t("knowledgeBase.chat")}
          className="kb-chat-drawer"
          data-testid="kb-chat-drawer"
        >
          {chatBody}
        </Drawer>
      )}
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

function DocumentsTab({ knowledgeBaseId, knowledgeBaseName, canWrite, canDelete }: { knowledgeBaseId: string; knowledgeBaseName: string; canWrite: boolean; canDelete: boolean }) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [folderKey, setFolderKey] = useState(FOLDER_ALL);
  const [tagFilter, setTagFilter] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [batchBusy, setBatchBusy] = useState("");
  const [folderDialog, setFolderDialog] = useState<{ mode: "rename" | "move"; folder: Folder }>();
  const [folderName, setFolderName] = useState("");
  const [folderParent, setFolderParent] = useState("");
  const [tagManagerOpen, setTagManagerOpen] = useState(false);
  const [tagName, setTagName] = useState("");
  const [tagBusy, setTagBusy] = useState("");
  const [editingTag, setEditingTag] = useState<{ id: string; name: string }>();
  const [tagDialog, setTagDialog] = useState<"tag" | "untag">();
  const [tagSelection, setTagSelection] = useState<string[]>([]);
  const [moveOpen, setMoveOpen] = useState(false);
  const [moveTarget, setMoveTarget] = useState(FOLDER_ROOT);
  const [replacing, setReplacing] = useState("");
  const uploadRef = useRef<HTMLInputElement>(null);
  const replaceRef = useRef<HTMLInputElement>(null);
  const replaceTarget = useRef("");
  const folders = useFolders(knowledgeBaseId).data ?? EMPTY_FOLDERS;
  const tags = useTags(knowledgeBaseId).data ?? EMPTY_TAGS;
  const folderId = folderKey === FOLDER_ALL ? "" : folderKey === FOLDER_ROOT ? "root" : folderKey;
  const uploadQueue = useUploadQueue({
    knowledgeBaseId,
    folderId: folderId && folderId !== "root" ? folderId : "",
    onUploaded: async () => {
      await refresh();
      await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) });
    },
  });
  const documents = useDocuments(knowledgeBaseId, { folderId, tagIds: tagFilter });
  const items = documents.data?.pages.flatMap((page) => page.items) ?? [];
  const processing = items.some((item) => !terminalVersionStatuses.has(item.version.status));
  const filtered = folderId !== "" || tagFilter.length > 0;

  const disabledFolderIds = folderDialog ? folderSubtreeIds(folders, folderDialog.folder.id) : [];

  function refresh() {
    return Promise.all([
      documents.refetch(),
      queryClient.invalidateQueries({ queryKey: queryKeys.folders(knowledgeBaseId) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.tags(knowledgeBaseId) }),
    ]);
  }

  async function runBatch(action: string, extra: Record<string, unknown> = {}) {
    if (selected.length === 0) return;
    setBatchBusy(action);
    try {
      const result = await api<BatchDocumentsResult>(`/knowledge-bases/${knowledgeBaseId}/documents/batch`, jsonInit({
        action, documentIds: selected, ...extra,
      }));
      const failed = result.items.filter((item) => !item.ok).length;
      if (failed > 0) {
        message.warning(t("batch.partial", { failed }));
      } else {
        message.success(t("batch.done"));
      }
      setSelected([]);
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("batch.failed"));
    } finally {
      setBatchBusy("");
    }
  }

  async function createFolder(parentID: string, name: string) {
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/folders`, jsonInit({ name, parentId: parentID }));
      message.success(t("folder.createSuccess"));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("folder.saveFailed"));
      throw cause;
    }
  }

  function openFolderDialog(folder: Folder, mode: "rename" | "move") {
    setFolderName(folder.name);
    setFolderParent(folder.parentId ?? "");
    setFolderDialog({ mode, folder });
  }

  async function downloadFolderDocuments(folders: Folder[]) {
    setBatchBusy("download");
    let downloaded = 0;
    try {
      // A parent folder already includes its subtree, so selected parent/child
      // pairs must not download the same document twice.
      const files = new Map<string, string>();
      for (const folder of folders) {
        let cursor = "";
        do {
          const params = new URLSearchParams({ limit: "50", folder_id: folder.id });
          if (cursor) params.set("cursor", cursor);
          const page = await api<DocumentPage>(`/knowledge-bases/${knowledgeBaseId}/documents?${params.toString()}`);
          for (const item of page.items) {
            files.set(item.document.id, item.document.filename);
          }
          cursor = page.hasMore ? page.nextCursor : "";
        } while (cursor);
      }
      for (const [documentId, filename] of files) {
        await downloadDocument(documentId, filename);
        downloaded += 1;
        // Browsers rate-limit bursts of programmatic downloads.
        await new Promise((resolve) => window.setTimeout(resolve, 250));
      }
      message.success(t("folder.downloadDone", { count: downloaded }));
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("folder.downloadFailed"));
    } finally {
      setBatchBusy("");
    }
  }

  function batchRemoveFolders(folders: Folder[]) {
    modal.confirm({
      title: t("folder.batchDeleteTitle", { count: folders.length }),
      content: t("folder.batchDeleteConfirm"),
      okText: t("common.delete"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        let failed = 0;
        for (const folder of folders) {
          try {
            await api(`/folders/${folder.id}?recursive=true`, { method: "DELETE" });
          } catch {
            failed += 1;
          }
        }
        if (failed > 0) {
          message.warning(t("folder.batchDeletePartial", { failed }));
        } else {
          message.success(t("folder.batchDeleteDone"));
        }
        if (folders.some((folder) => folder.id === folderKey)) setFolderKey(FOLDER_ALL);
        await refresh();
      },
    });
  }

  async function saveFolder() {
    if (!folderDialog) return;
    try {
      if (folderDialog.mode === "rename") {
        await api(`/folders/${folderDialog.folder.id}`, jsonInit({ name: folderName, parentId: folderParent }, "PATCH"));
      } else {
        await api(`/folders/${folderDialog.folder.id}`, jsonInit({ parentId: folderParent }, "PATCH"));
      }
      message.success(t("folder.updateSuccess"));
      setFolderDialog(undefined);
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("folder.saveFailed"));
    }
  }

  function removeFolder(folder: Folder) {
    modal.confirm({
      title: t("folder.deleteTitle", { name: folder.name }),
      content: t("folder.deleteConfirm"),
      okText: t("common.delete"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        try {
          await api(`/folders/${folder.id}`, { method: "DELETE" });
          if (folderKey === folder.id) setFolderKey(FOLDER_ALL);
          message.success(t("folder.deleteSuccess"));
          await refresh();
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("folder.deleteFailed"));
        }
      },
    });
  }

  async function createTag() {
    if (!tagName.trim() || tagBusy) return;
    setTagBusy("create");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/tags`, jsonInit({ name: tagName }));
      setTagName("");
      message.success(t("tag.createSuccess"));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("tag.saveFailed"));
    } finally {
      setTagBusy("");
    }
  }

  async function renameTag() {
    if (!editingTag || tagBusy) return;
    setTagBusy("rename");
    try {
      await api(`/tags/${editingTag.id}`, jsonInit({ name: editingTag.name }, "PATCH"));
      setEditingTag(undefined);
      message.success(t("tag.updateSuccess"));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("tag.saveFailed"));
    } finally {
      setTagBusy("");
    }
  }

  function removeTag(tag: { id: string; name: string }) {
    modal.confirm({
      title: t("tag.deleteTitle", { name: tag.name }),
      content: t("tag.deleteConfirm"),
      okText: t("common.delete"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        try {
          await api(`/tags/${tag.id}`, { method: "DELETE" });
          setTagFilter((current) => current.filter((id) => id !== tag.id));
          message.success(t("tag.deleteSuccess"));
          await refresh();
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("tag.deleteFailed"));
        }
      },
    });
  }

  function upload(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? []);
    event.target.value = "";
    if (files.length > 0) uploadQueue.enqueue(files);
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
      width: 280,
      render: (_, item) => (
        <div className="doc-name">
          <span data-testid="document-name" data-document-id={item.document.id}>
            <Typography.Link onClick={() => navigate(`/documents/${item.document.id}?from=${encodeURIComponent(knowledgeBaseId)}`)}>
              {item.document.filename}
            </Typography.Link>
          </span>
        </div>
      ),
    },
    { title: t("document.status"), key: "status", width: 150, render: (_, item) => <DocumentStatusBadge status={item.version.status} /> },
    { title: t("document.version"), key: "version", width: 120, render: (_, item) => `v${item.version.version} · ${formatSize(item.version.sizeBytes)}` },
    {
      title: t("document.tags"),
      key: "tags",
      width: 200,
      render: (_, item) => item.tags.length === 0
        ? <Typography.Text type="secondary">-</Typography.Text>
        : (
          <Space size={4} wrap>
            {item.tags.slice(0, 2).map((tag) => <Tag key={tag.id} color="blue">{tag.name}</Tag>)}
            {item.tags.length > 2 && (
              <Tooltip title={item.tags.slice(2).map((tag) => tag.name).join(", ")}>
                <Tag>+{item.tags.length - 2}</Tag>
              </Tooltip>
            )}
          </Space>
        ),
    },
    { title: t("common.updatedAt"), key: "updated", width: 180, render: (_, item) => formatTime(item.document.updatedAt) },
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
      <div className="kb-documents-layout">
        <FolderTree
          folders={folders}
          knowledgeBaseName={knowledgeBaseName}
          selectedKey={folderKey}
          canWrite={canWrite}
          canDelete={canDelete}
          onSelect={(key) => { setFolderKey(key); setSelected([]); }}
          onCreate={createFolder}
          onRename={(folder) => openFolderDialog(folder, "rename")}
          onMove={(folder) => openFolderDialog(folder, "move")}
          onDelete={removeFolder}
          onDownload={(selection) => void downloadFolderDocuments(selection)}
          onBatchDelete={batchRemoveFolders}
        />
        <div className="kb-documents-main">
          <Flex justify="space-between" align="center" gap={12} wrap="wrap" className="tab-toolbar">
            <Space>
              <Typography.Text strong>{t("knowledgeBase.tabs.documents")}</Typography.Text>
              <Tag>{items.length}</Tag>
              {processing && <Tag color="processing">{t("document.processingTag")}</Tag>}
            </Space>
            <Space wrap>
              <Select
                mode="multiple"
                allowClear
                size="small"
                className="tag-filter"
                placeholder={t("tag.filter")}
                value={tagFilter}
                onChange={(values) => { setTagFilter(values); setSelected([]); }}
                options={tags.map((tag) => ({ value: tag.id, label: tag.name }))}
                data-testid="tag-filter"
              />
              <Button size="small" icon={<TagsOutlined />} onClick={() => setTagManagerOpen(true)}>{t("tag.manage")}</Button>
              <Button size="small" icon={<ReloadOutlined />} onClick={() => void refresh()}>{t("common.refresh")}</Button>
              {canWrite && (
                <>
                  <Button size="small" type="primary" icon={<UploadOutlined />} onClick={() => uploadRef.current?.click()}>
                    {t("knowledgeBase.upload")}
                  </Button>
                  {uploadQueue.jobs.length > 0 && (
                    <Button size="small" onClick={() => uploadQueue.setOpen(true)}>{t("upload.title")}</Button>
                  )}
                  <input ref={uploadRef} data-testid="document-upload-input" type="file" accept={accept} multiple hidden onChange={(event) => void upload(event)} />
                  <input ref={replaceRef} data-testid="document-replace-input" type="file" accept={accept} hidden onChange={(event) => void replace(event)} />
                </>
              )}
            </Space>
          </Flex>
          <Typography.Paragraph type="secondary">{t("knowledgeBase.uploadHint")}</Typography.Paragraph>
          {selected.length > 0 && (
            <Flex align="center" gap={8} wrap className="batch-bar">
              <Typography.Text strong>{t("batch.selected", { count: selected.length })}</Typography.Text>
              {canWrite && <Button size="small" icon={<FolderOutlined />} onClick={() => { setMoveTarget(FOLDER_ROOT); setMoveOpen(true); }}>{t("batch.move")}</Button>}
              {canWrite && <Button size="small" icon={<TagsOutlined />} onClick={() => { setTagSelection([]); setTagDialog("tag"); }}>{t("batch.tag")}</Button>}
              {canWrite && <Button size="small" icon={<TagsOutlined />} onClick={() => { setTagSelection([]); setTagDialog("untag"); }}>{t("batch.untag")}</Button>}
              {canWrite && <Button size="small" icon={<SyncOutlined />} loading={batchBusy === "reparse"} onClick={() => void runBatch("reparse")}>{t("batch.reparse")}</Button>}
              {canDelete && (
                <Button
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  loading={batchBusy === "delete"}
                  onClick={() => modal.confirm({
                    title: t("batch.deleteTitle", { count: selected.length }),
                    content: t("batch.deleteConfirm"),
                    okText: t("common.delete"),
                    okButtonProps: { danger: true },
                    cancelText: t("common.cancel"),
                    onOk: () => runBatch("delete"),
                  })}
                >
                  {t("batch.delete")}
                </Button>
              )}
              <Button size="small" type="text" onClick={() => setSelected([])}>{t("common.cancel")}</Button>
            </Flex>
          )}
          {documents.isLoading && <LoadingBlock />}
          {documents.error && <QueryFallback error={documents.error} retry={() => void refresh()} />}
          {!documents.isLoading && items.length === 0 && (
            filtered
              ? <EmptyState description={t("knowledgeBase.noMatchDocuments")} />
              : (
                <EmptyState
                  description={t("knowledgeBase.noDocuments")}
                  action={canWrite ? () => uploadRef.current?.click() : undefined}
                  actionLabel={canWrite ? t("knowledgeBase.upload") : undefined}
                />
              )
          )}
          {items.length > 0 && (
            <Table<DocumentSummary>
              rowKey={(item) => item.document.id}
              size="middle"
              columns={columns}
              dataSource={items}
              pagination={false}
              scroll={{ x: 1080 }}
              tableLayout="fixed"
              rowSelection={(canWrite || canDelete) ? {
                selectedRowKeys: selected,
                onChange: (keys) => setSelected(keys.map(String)),
              } : undefined}
            />
          )}
          {documents.hasNextPage && (
            <Flex justify="center" className="load-more">
              <Button loading={documents.isFetchingNextPage} onClick={() => void documents.fetchNextPage()}>{t("knowledgeBase.loadMore")}</Button>
            </Flex>
          )}
        </div>
      </div>

      <UploadQueueModal queue={uploadQueue} />

      <Modal
        open={Boolean(folderDialog)}
        title={folderDialog?.mode === "rename" ? t("folder.rename") : t("folder.move")}
        onCancel={() => setFolderDialog(undefined)}
        onOk={() => void saveFolder()}
        okText={t("common.save")}
        cancelText={t("common.cancel")}
      >
        {folderDialog?.mode !== "move" && (
          <Input
            value={folderName}
            onChange={(event) => setFolderName(event.target.value)}
            placeholder={t("folder.name")}
            className="modal-field"
          />
        )}
        <div className="modal-field">
          <Typography.Text type="secondary">{t("folder.parent")}</Typography.Text>
          <FolderTreeSelect
            folders={folders}
            knowledgeBaseName={knowledgeBaseName}
            value={folderParent || FOLDER_ROOT}
            disabledValues={disabledFolderIds}
            onChange={(value) => setFolderParent(value === FOLDER_ROOT ? "" : value)}
            placeholder={t("folder.parent")}
            ariaLabel={t("folder.parent")}
            style={{ width: "100%" }}
          />
        </div>
      </Modal>

      <Modal open={tagManagerOpen} title={t("tag.manage")} onCancel={() => setTagManagerOpen(false)} footer={null}>
        <Space.Compact style={{ width: "100%" }}>
          <Input value={tagName} onChange={(event) => setTagName(event.target.value)} placeholder={t("tag.name")} onPressEnter={() => void createTag()} />
          <Button type="primary" loading={tagBusy === "create"} disabled={tagBusy !== ""} onClick={() => void createTag()}>{t("tag.create")}</Button>
        </Space.Compact>
        <div className="tag-manager-list">
          {tags.length === 0 && <Typography.Text type="secondary">{t("tag.empty")}</Typography.Text>}
          {tags.map((tag) => (
            <Flex key={tag.id} justify="space-between" align="center" gap={8} className="tag-manager-row">
              {editingTag?.id === tag.id ? (
                <Space.Compact style={{ flex: 1 }}>
                  <Input value={editingTag.name} onChange={(event) => setEditingTag({ id: tag.id, name: event.target.value })} />
                  <Button loading={tagBusy === "rename"} disabled={tagBusy !== ""} onClick={() => void renameTag()}>{t("common.save")}</Button>
                </Space.Compact>
              ) : <Tag color="blue">{tag.name}</Tag>}
              <Space size={4}>
                <Button size="small" type="text" onClick={() => setEditingTag({ id: tag.id, name: tag.name })}>{t("tag.rename")}</Button>
                <Button size="small" type="text" danger onClick={() => removeTag(tag)}>{t("tag.delete")}</Button>
              </Space>
            </Flex>
          ))}
        </div>
      </Modal>

      <Modal
        open={moveOpen}
        title={t("batch.moveTitle")}
        onCancel={() => setMoveOpen(false)}
        okText={t("common.confirm")}
        cancelText={t("common.cancel")}
        onOk={() => {
          setMoveOpen(false);
          void runBatch("move", { folderId: moveTarget === FOLDER_ROOT ? "" : moveTarget });
        }}
      >
        <FolderTreeSelect
          folders={folders}
          knowledgeBaseName={knowledgeBaseName}
          value={moveTarget}
          onChange={setMoveTarget}
          placeholder={t("batch.moveTitle")}
          ariaLabel={t("batch.moveTitle")}
          style={{ width: "100%" }}
        />
      </Modal>

      <Modal
        open={Boolean(tagDialog)}
        title={tagDialog === "untag" ? t("batch.untagTitle") : t("batch.tagTitle")}
        onCancel={() => setTagDialog(undefined)}
        okText={t("common.confirm")}
        cancelText={t("common.cancel")}
        okButtonProps={{ disabled: tagSelection.length === 0 }}
        onOk={() => {
          const mode = tagDialog;
          setTagDialog(undefined);
          if (mode) void runBatch(mode, { tagIds: tagSelection });
        }}
      >
        <Select
          mode="multiple"
          style={{ width: "100%" }}
          value={tagSelection}
          onChange={setTagSelection}
          options={tags.map((tag) => ({ value: tag.id, label: tag.name }))}
          placeholder={t("tag.select")}
        />
      </Modal>
    </div>
  );
}
