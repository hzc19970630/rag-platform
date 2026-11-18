import {
  ArrowLeftOutlined,
  CloseOutlined,
  DeleteOutlined,
  FolderOutlined,
  MessageOutlined,
  PushpinOutlined,
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
import { EmptyState } from "../components/EmptyState";
import { DocumentStatusBadge } from "../components/DocumentStatusBadge";
import { FOLDER_ALL, FOLDER_ROOT, FolderTree } from "../components/FolderTree";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { SessionList } from "../components/SessionList";
import { SettingsDrawer } from "../features/settings/SettingsDrawer";
import { MembersSettings } from "../features/settings/MembersSettings";
import { ProjectionPanel } from "../features/settings/ProjectionPanel";
import { usePermissions } from "../session";
import { clampKbChatWidth, usePreferences } from "../store/preferences";
import { formatSize, formatTime } from "../utils/format";

const accept = ".pdf,.docx,.xlsx,.pptx,.md,.txt,.html,.htm";
const EMPTY_FOLDERS: Folder[] = [];
const EMPTY_TAGS: TagView[] = [];
const CHAT_PANEL_QUERY = "(min-width: 1101px)";

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
  const chatPinned = usePreferences((state) => state.kbChatPinned);
  const chatWidth = usePreferences((state) => state.kbChatWidth);
  const setChatPinned = usePreferences((state) => state.setKbChatPinned);
  const setChatWidth = usePreferences((state) => state.setKbChatWidth);
  const [chatOpen, setChatOpen] = useState(false);
  const [creatingSession, setCreatingSession] = useState(false);
  const [dragWidth, setDragWidth] = useState<number>();
  const dragging = useRef(false);
  const latestDragWidth = useRef<number | undefined>(undefined);

  const kbConversations = useMemo(
    () => (conversations.data ?? []).filter((item) => item.knowledgeBaseId === knowledgeBaseId),
    [conversations.data, knowledgeBaseId],
  );

  useEffect(() => {
    if (chatPinned) setChatOpen(true);
  }, [chatPinned]);

  useEffect(() => {
    const move = (event: PointerEvent) => {
      if (!dragging.current) return;
      const next = clampKbChatWidth(window.innerWidth - event.clientX);
      latestDragWidth.current = next;
      setDragWidth(next);
    };
    const up = () => {
      if (!dragging.current) return;
      dragging.current = false;
      document.body.classList.remove("resizing");
      if (latestDragWidth.current !== undefined) setChatWidth(latestDragWidth.current);
      latestDragWidth.current = undefined;
      setDragWidth(undefined);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
  }, [setChatWidth]);

  async function startSession() {
    if (creatingSession) return;
    setCreatingSession(true);
    try {
      const created = await createConversation(knowledgeBaseId);
      await queryClient.invalidateQueries({ queryKey: queryKeys.conversations });
      navigate(`/chat/${created.id}`);
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

  const chatSessions = (
    <SessionList
      items={kbConversations}
      loading={conversations.isLoading}
      error={conversations.error}
      onRetry={() => void conversations.refetch()}
      onSelect={(conversation) => navigate(`/chat/${conversation.id}`)}
      onCreate={() => void startSession()}
      creating={creatingSession}
      actions={wideChat ? (
        <>
          <Tooltip title={chatPinned ? t("chat.unpin") : t("chat.pin")}>
            <Button
              type={chatPinned ? "primary" : "text"}
              size="small"
              icon={<PushpinOutlined />}
              aria-label={chatPinned ? t("chat.unpin") : t("chat.pin")}
              onClick={() => { setChatPinned(!chatPinned); setChatOpen(true); }}
            />
          </Tooltip>
          <Tooltip title={t("chat.collapsePanel")}>
            <Button
              type="text"
              size="small"
              icon={<CloseOutlined />}
              aria-label={t("chat.collapsePanel")}
              onClick={() => { setChatPinned(false); setChatOpen(false); }}
            />
          </Tooltip>
        </>
      ) : undefined}
    />
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
          <aside className="kb-chat-panel" style={{ width: dragWidth ?? chatWidth }} data-testid="kb-chat-panel" aria-label={t("chat.conversations")}>
            <div
              className="kb-chat-resizer"
              role="separator"
              aria-orientation="vertical"
              aria-label={t("chat.resizePanel")}
              tabIndex={0}
              onPointerDown={() => { dragging.current = true; document.body.classList.add("resizing"); }}
              onKeyDown={(event) => {
                if (event.key === "ArrowLeft") setChatWidth(chatWidth + 16);
                if (event.key === "ArrowRight") setChatWidth(chatWidth - 16);
              }}
            />
            {chatSessions}
          </aside>
        )}
      </div>
      {canChat && !wideChat && (
        <Drawer
          open={chatOpen}
          onClose={() => setChatOpen(false)}
          placement="right"
          size={360}
          title={t("chat.conversations")}
          data-testid="kb-chat-drawer"
        >
          {chatSessions}
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

function DocumentsTab({ knowledgeBaseId, canWrite, canDelete }: { knowledgeBaseId: string; canWrite: boolean; canDelete: boolean }) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [folderKey, setFolderKey] = useState(FOLDER_ALL);
  const [tagFilter, setTagFilter] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [batchBusy, setBatchBusy] = useState("");
  const [folderDialog, setFolderDialog] = useState<{ mode: "create"; parentID: string } | { mode: "rename" | "move"; folder: Folder }>();
  const [folderName, setFolderName] = useState("");
  const [folderParent, setFolderParent] = useState("");
  const [tagManagerOpen, setTagManagerOpen] = useState(false);
  const [tagName, setTagName] = useState("");
  const [editingTag, setEditingTag] = useState<{ id: string; name: string }>();
  const [tagDialog, setTagDialog] = useState<"tag" | "untag">();
  const [tagSelection, setTagSelection] = useState<string[]>([]);
  const [moveOpen, setMoveOpen] = useState(false);
  const [moveTarget, setMoveTarget] = useState(FOLDER_ROOT);
  const [uploading, setUploading] = useState(false);
  const [replacing, setReplacing] = useState("");
  const uploadRef = useRef<HTMLInputElement>(null);
  const replaceRef = useRef<HTMLInputElement>(null);
  const replaceTarget = useRef("");
  const folders = useFolders(knowledgeBaseId).data ?? EMPTY_FOLDERS;
  const tags = useTags(knowledgeBaseId).data ?? EMPTY_TAGS;
  const folderId = folderKey === FOLDER_ALL ? "" : folderKey === FOLDER_ROOT ? "root" : folderKey;
  const documents = useDocuments(knowledgeBaseId, { folderId, tagIds: tagFilter });
  const items = documents.data?.pages.flatMap((page) => page.items) ?? [];
  const processing = items.some((item) => !terminalVersionStatuses.has(item.version.status));
  const filtered = folderId !== "" || tagFilter.length > 0;

  const folderOptions = useMemo(() => {
    const byID = new Map(folders.map((folder) => [folder.id, folder]));
    function label(folder: Folder): string {
      const names = [folder.name];
      let parent = folder.parentId ?? "";
      while (parent) {
        const next = byID.get(parent);
        if (!next) break;
        names.unshift(next.name);
        parent = next.parentId ?? "";
      }
      return names.join(" / ");
    }
    return [
      { value: FOLDER_ROOT, label: t("folder.root") },
      ...folders.map((folder) => ({ value: folder.id, label: label(folder) })),
    ];
  }, [folders, t]);

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

  function openCreateFolder(parentID: string) {
    setFolderName("");
    setFolderParent(parentID);
    setFolderDialog({ mode: "create", parentID });
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
      if (folderDialog.mode === "create") {
        await api(`/knowledge-bases/${knowledgeBaseId}/folders`, jsonInit({ name: folderName, parentId: folderParent }));
      } else if (folderDialog.mode === "rename") {
        await api(`/folders/${folderDialog.folder.id}`, jsonInit({ name: folderName, parentId: folderParent }, "PATCH"));
      } else {
        await api(`/folders/${folderDialog.folder.id}`, jsonInit({ parentId: folderParent }, "PATCH"));
      }
      message.success(folderDialog.mode === "create" ? t("folder.createSuccess") : t("folder.updateSuccess"));
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
    if (!tagName.trim()) return;
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/tags`, jsonInit({ name: tagName }));
      setTagName("");
      message.success(t("tag.createSuccess"));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("tag.saveFailed"));
    }
  }

  async function renameTag() {
    if (!editingTag) return;
    try {
      await api(`/tags/${editingTag.id}`, jsonInit({ name: editingTag.name }, "PATCH"));
      setEditingTag(undefined);
      message.success(t("tag.updateSuccess"));
      await refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("tag.saveFailed"));
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

  async function upload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setUploading(true);
    try {
      await uploadDocument(knowledgeBaseId, file, undefined, folderId && folderId !== "root" ? folderId : undefined);
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
          selectedKey={folderKey}
          canWrite={canWrite}
          canDelete={canDelete}
          onSelect={(key) => { setFolderKey(key); setSelected([]); }}
          onCreate={openCreateFolder}
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
                  <Button size="small" type="primary" icon={<UploadOutlined />} loading={uploading} onClick={() => uploadRef.current?.click()}>
                    {t("knowledgeBase.upload")}
                  </Button>
                  <input ref={uploadRef} data-testid="document-upload-input" type="file" accept={accept} hidden onChange={(event) => void upload(event)} />
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

      <Modal
        open={Boolean(folderDialog)}
        title={folderDialog?.mode === "create" ? t("folder.create") : folderDialog?.mode === "rename" ? t("folder.rename") : t("folder.move")}
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
          <Select
            style={{ width: "100%" }}
            value={folderParent || FOLDER_ROOT}
            onChange={(value) => setFolderParent(value === FOLDER_ROOT ? "" : value)}
            options={folderOptions}
          />
        </div>
      </Modal>

      <Modal open={tagManagerOpen} title={t("tag.manage")} onCancel={() => setTagManagerOpen(false)} footer={null}>
        <Space.Compact style={{ width: "100%" }}>
          <Input value={tagName} onChange={(event) => setTagName(event.target.value)} placeholder={t("tag.name")} onPressEnter={() => void createTag()} />
          <Button type="primary" onClick={() => void createTag()}>{t("tag.create")}</Button>
        </Space.Compact>
        <div className="tag-manager-list">
          {tags.length === 0 && <Typography.Text type="secondary">{t("tag.empty")}</Typography.Text>}
          {tags.map((tag) => (
            <Flex key={tag.id} justify="space-between" align="center" gap={8} className="tag-manager-row">
              {editingTag?.id === tag.id ? (
                <Space.Compact style={{ flex: 1 }}>
                  <Input value={editingTag.name} onChange={(event) => setEditingTag({ id: tag.id, name: event.target.value })} />
                  <Button onClick={() => void renameTag()}>{t("common.save")}</Button>
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
        <Select style={{ width: "100%" }} value={moveTarget} onChange={setMoveTarget} options={folderOptions} />
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
