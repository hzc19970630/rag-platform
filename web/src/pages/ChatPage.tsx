import { ArrowLeftOutlined, MessageOutlined } from "@ant-design/icons";
import { Alert, App, Button, Modal, Select, Space, Spin, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { createConversation } from "../api/conversations";
import { queryKeys, useConversations, useKnowledgeBases } from "../api/hooks";
import type { Conversation } from "../api/types";
import { ChatConversation } from "../components/ChatConversation";
import { EmptyState } from "../components/EmptyState";
import { SessionList } from "../components/SessionList";
import { usePermissions } from "../session";
import { formatTime } from "../utils/format";

export function ChatPage() {
  const { conversationId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const knowledgeBaseFromQuery = searchParams.get("kb") ?? "";
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { message } = App.useApp();
  const { t } = useTranslation();
  const { canChat } = usePermissions();
  const knowledgeBases = useKnowledgeBases();
  const conversations = useConversations();
  const [session, setSession] = useState<Conversation>();
  const [creating, setCreating] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [newKnowledgeBase, setNewKnowledgeBase] = useState("");

  const knowledgeBaseNames = useMemo(
    () => new Map((knowledgeBases.data ?? []).map((kb) => [kb.id, kb.name])),
    [knowledgeBases.data],
  );

  const listedConversation = (conversations.data ?? []).find((item) => item.id === conversationId);
  const current = session?.id === conversationId ? session : listedConversation;
  const currentKnowledgeBaseId = current?.knowledgeBaseId ?? "";
  const currentKnowledgeBaseName = knowledgeBaseNames.get(currentKnowledgeBaseId) ?? currentKnowledgeBaseId;

  useEffect(() => {
    if (conversationId || !knowledgeBaseFromQuery || !canChat) return;
    let cancelled = false;
    setCreating(true);
    void createConversation(knowledgeBaseFromQuery)
      .then((created) => {
        if (cancelled) return;
        setSession(created);
        void queryClient.invalidateQueries({ queryKey: queryKeys.conversations });
        navigate(`/chat/${created.id}`, { replace: true });
      })
      .catch((cause: unknown) => {
        if (!cancelled) message.error(cause instanceof Error ? cause.message : t("chat.createFailed"));
      })
      .finally(() => {
        if (!cancelled) setCreating(false);
      });
    return () => {
      cancelled = true;
    };
  }, [conversationId, knowledgeBaseFromQuery, canChat, navigate, queryClient, message, t]);

  if (!canChat) {
    return (
      <div className="page">
        <Alert type="error" showIcon message={t("common.noPermission")} description={t("chat.readRequired")} />
      </div>
    );
  }

  async function startConversation() {
    if (!newKnowledgeBase) return;
    setCreating(true);
    try {
      const created = await createConversation(newKnowledgeBase);
      setSession(created);
      setNewOpen(false);
      setNewKnowledgeBase("");
      await queryClient.invalidateQueries({ queryKey: queryKeys.conversations });
      navigate(`/chat/${created.id}`);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("chat.createFailed"));
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="chat-workspace">
      <aside className="chat-sessions" aria-label={t("chat.conversations")}>
        <SessionList
          items={conversations.data ?? []}
          grouped
          knowledgeBaseNames={knowledgeBaseNames}
          activeId={conversationId}
          onSelect={(conversation) => navigate(`/chat/${conversation.id}`)}
          onCreate={() => setNewOpen(true)}
          loading={conversations.isLoading}
          error={conversations.error}
          onRetry={() => void conversations.refetch()}
          creating={creating}
        />
      </aside>

      <section className="chat-main">
        {!current && (
          <div className="chat-empty">
            {creating ? <Spin /> : (
              <EmptyState
                description={t("chat.chooseKnowledgeBase")}
                action={() => setNewOpen(true)}
                actionLabel={t("chat.newConversation")}
              />
            )}
          </div>
        )}
        {current && (
          <>
            <header className="chat-main-header">
              <Space size={8} wrap>
                <Button
                  type="text"
                  icon={<ArrowLeftOutlined />}
                  onClick={() => navigate(currentKnowledgeBaseId ? `/knowledge-bases/${currentKnowledgeBaseId}` : "/knowledge-bases")}
                  aria-label={t("common.back")}
                />
                <MessageOutlined />
                <Typography.Title level={4}>{current.title || t("chat.untitled")}</Typography.Title>
                <Tag>{currentKnowledgeBaseName}</Tag>
              </Space>
              <Typography.Text type="secondary">{formatTime(current.updatedAt)}</Typography.Text>
            </header>
            <ChatConversation conversation={current} />
          </>
        )}
      </section>

      <Modal
        open={newOpen}
        title={t("chat.newConversation")}
        okText={t("common.create")}
        cancelText={t("common.cancel")}
        confirmLoading={creating}
        onCancel={() => { setNewOpen(false); setNewKnowledgeBase(""); }}
        onOk={() => void startConversation()}
        okButtonProps={{ disabled: !newKnowledgeBase }}
      >
        <Typography.Paragraph type="secondary">{t("chat.chooseKnowledgeBase")}</Typography.Paragraph>
        <Select
          value={newKnowledgeBase || undefined}
          onChange={setNewKnowledgeBase}
          placeholder={t("chat.chooseKnowledgeBase")}
          aria-label={t("chat.chooseKnowledgeBase")}
          style={{ width: "100%" }}
          options={(knowledgeBases.data ?? []).map((kb) => ({ value: kb.id, label: kb.name }))}
        />
      </Modal>
    </div>
  );
}