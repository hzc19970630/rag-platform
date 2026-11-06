import {
  ArrowLeftOutlined,
  FileTextOutlined,
  LoadingOutlined,
  MessageOutlined,
  PlusOutlined,
  SendOutlined,
} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Drawer,
  Empty,
  Flex,
  Input,
  List,
  Modal,
  Select,
  Space,
  Spin,
  Tag,
  Typography,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { streamAnswer, type AnswerStreamPhase } from "../api/answerStream";
import { api, jsonInit } from "../api/client";
import { queryKeys, useConversations, useKnowledgeBases, useMessages } from "../api/hooks";
import type { Citation, Conversation, MessageView, StreamEvent } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { VirtualMessageList } from "../components/VirtualMessageList";
import { VirtualSessionList } from "../components/VirtualSessionList";
import { usePermissions } from "../session";
import { formatTime } from "../utils/format";

// StrictMode mounts effects twice in development; sharing the in-flight
// creation keeps a knowledge-base entry point from opening two sessions.
const sessionCreations = new Map<string, Promise<Conversation>>();

function createConversation(knowledgeBaseId: string): Promise<Conversation> {
  const pending = sessionCreations.get(knowledgeBaseId);
  if (pending) return pending;
  const request = api<Conversation>("/sessions", jsonInit({ knowledge_base_id: knowledgeBaseId }))
    .finally(() => sessionCreations.delete(knowledgeBaseId));
  sessionCreations.set(knowledgeBaseId, request);
  return request;
}

type StreamedAnswer = {
  content: string;
  citations: Citation[];
  status: string;
};

type ChatRow =
  | { key: string; kind: "loading" }
  | { key: string; kind: "error"; error: unknown }
  | { key: string; kind: "empty" }
  | { key: string; kind: "message"; item: MessageView }
  | { key: string; kind: "pending"; content: string }
  | { key: string; kind: "asking" }
  | { key: string; kind: "stream-error"; message: string };

type SessionRow =
  | { key: string; kind: "group"; name: string }
  | { key: string; kind: "conversation"; conversation: Conversation };

const noMessages: MessageView[] = [];

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
  const messages = useMessages(conversationId || undefined);
  const [session, setSession] = useState<Conversation>();
  const [creating, setCreating] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [newKnowledgeBase, setNewKnowledgeBase] = useState("");
  const [question, setQuestion] = useState("");
  const [asking, setAsking] = useState(false);
  const [states, setStates] = useState<string[]>([]);
  const [pendingUser, setPendingUser] = useState("");
  const [streamed, setStreamed] = useState<StreamedAnswer>();
  const [streamError, setStreamError] = useState("");
  const [streamPhase, setStreamPhase] = useState<AnswerStreamPhase>();
  const [citationDrawer, setCitationDrawer] = useState<{ title: string; citations: Citation[] }>();
  const mounted = useRef(true);
  const activeStream = useRef<AbortController | null>(null);

  const knowledgeBaseNames = useMemo(
    () => new Map((knowledgeBases.data ?? []).map((kb) => [kb.id, kb.name])),
    [knowledgeBases.data],
  );

  const sessionRows = useMemo<SessionRow[]>(() => {
    const grouped = new Map<string, Conversation[]>();
    for (const conversation of conversations.data ?? []) {
      const rows = grouped.get(conversation.knowledgeBaseId) ?? [];
      rows.push(conversation);
      grouped.set(conversation.knowledgeBaseId, rows);
    }
    const rows: SessionRow[] = [];
    for (const [knowledgeBaseId, groupedConversations] of grouped) {
      rows.push({
        key: `group:${knowledgeBaseId}`,
        kind: "group",
        name: knowledgeBaseNames.get(knowledgeBaseId) ?? knowledgeBaseId,
      });
      for (const conversation of groupedConversations) {
        rows.push({ key: `conversation:${conversation.id}`, kind: "conversation", conversation });
      }
    }
    return rows;
  }, [conversations.data, knowledgeBaseNames]);

  const listedConversation = (conversations.data ?? []).find((item) => item.id === conversationId);
  const current = session?.id === conversationId ? session : listedConversation;
  const currentKnowledgeBaseId = current?.knowledgeBaseId ?? "";
  const currentKnowledgeBaseName = knowledgeBaseNames.get(currentKnowledgeBaseId) ?? currentKnowledgeBaseId;

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      activeStream.current?.abort();
    };
  }, []);

  useEffect(() => {
    activeStream.current?.abort();
    setAsking(false);
    setPendingUser("");
    setStates([]);
    setStreamed(undefined);
    setStreamError("");
    setStreamPhase(undefined);
  }, [conversationId]);

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

  async function send() {
    const query = question.trim();
    if (!query || asking || !current) return;
    const controller = new AbortController();
    activeStream.current?.abort();
    activeStream.current = controller;
    setQuestion("");
    setAsking(true);
    setStates([]);
    setStreamed(undefined);
    setStreamError("");
    setPendingUser(query);
    try {
      await streamAnswer({
        conversationId: current.id,
        query,
        requestId: crypto.randomUUID(),
        signal: controller.signal,
        onStatus: setStreamPhase,
        onEvent: (event: StreamEvent) => {
          if (event.type === "status") {
            const state = String(event.data?.state ?? "");
            if (state) setStates((existing) => (existing.includes(state) ? existing : [...existing, state]));
          } else if (event.type === "answer") {
            setStreamed({
              content: String(event.data?.content ?? ""),
              citations: (event.data?.citations as Citation[] | undefined) ?? [],
              status: String(event.data?.status ?? "COMPLETED"),
            });
          } else if (event.type === "complete") {
            setStreamed((existing) => existing
              ? { ...existing, status: String(event.data?.status ?? existing.status) }
              : existing);
          } else if (event.type === "error") {
            setStreamError(String(event.data?.message ?? event.data?.code ?? t("chat.failed")));
          }
        },
      });
      if (!mounted.current) return;
      setPendingUser("");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.messages(current.id) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.conversations }),
      ]);
      setStreamed(undefined);
      setStates([]);
    } catch (cause) {
      if (controller.signal.aborted || !mounted.current) return;
      setStreamError(cause instanceof Error ? cause.message : t("chat.failed"));
    } finally {
      if (activeStream.current === controller) activeStream.current = null;
      if (mounted.current) {
        setAsking(false);
        setStreamPhase(undefined);
      }
    }
  }

  function openCitation(citation: Citation) {
    const from = currentKnowledgeBaseId ? `&from=${encodeURIComponent(currentKnowledgeBaseId)}` : "";
    navigate(`/documents/${citation.documentId}?chunk=${encodeURIComponent(citation.chunkId)}${from}`);
  }

  const history = messages.data ?? noMessages;
  const messageRows = useMemo<ChatRow[]>(() => {
    const rows: ChatRow[] = [];
    if (messages.isLoading) rows.push({ key: "loading", kind: "loading" });
    if (messages.error) rows.push({ key: "error", kind: "error", error: messages.error });
    if (!messages.isLoading && history.length === 0 && !pendingUser) rows.push({ key: "empty", kind: "empty" });
    for (const item of history) rows.push({ key: `message:${item.message.id}`, kind: "message", item });
    if (pendingUser) rows.push({ key: "pending", kind: "pending", content: pendingUser });
    if (asking) rows.push({ key: "asking", kind: "asking" });
    if (streamError) rows.push({ key: "stream-error", kind: "stream-error", message: streamError });
    return rows;
  }, [messages.isLoading, messages.error, history, pendingUser, asking, streamError]);

  return (
    <div className="chat-workspace">
      <aside className="chat-sessions" aria-label={t("chat.conversations")}>
        <Flex justify="space-between" align="center" className="chat-sessions-heading">
          <Typography.Text strong>{t("chat.conversations")}</Typography.Text>
          <Button type="primary" size="small" icon={<PlusOutlined />} onClick={() => setNewOpen(true)}>
            {t("chat.newShort")}
          </Button>
        </Flex>
        {conversations.isLoading && <LoadingBlock />}
        {conversations.error && <QueryFallback error={conversations.error} retry={() => void conversations.refetch()} />}
        {!conversations.isLoading && sessionRows.length === 0 && (
          <Typography.Paragraph type="secondary" className="chat-sessions-empty">
            {t("chat.noConversations")}
          </Typography.Paragraph>
        )}
        <VirtualSessionList
          rows={sessionRows}
          estimateSize={(index) => (sessionRows[index]?.kind === "group" ? 38 : 56)}
          renderRow={(row) => row.kind === "group" ? (
            <Typography.Text type="secondary" className="chat-session-group">
              {row.name}
            </Typography.Text>
          ) : (
            <button
              type="button"
              className={row.conversation.id === conversationId ? "chat-session active" : "chat-session"}
              onClick={() => navigate(`/chat/${row.conversation.id}`)}
            >
              <Flex vertical gap={2} className="chat-session-body">
                <Typography.Text ellipsis>{row.conversation.title || t("chat.untitled")}</Typography.Text>
                <Typography.Text type="secondary" className="chat-session-time">{formatTime(row.conversation.updatedAt)}</Typography.Text>
              </Flex>
            </button>
          )}
        />
      </aside>

      <section className="chat-main">
        {!current && (
          <div className="chat-empty">
            {creating ? <Spin indicator={<LoadingOutlined spin />} /> : (
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
                <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate("/knowledge-bases")} aria-label={t("common.back")} />
                <MessageOutlined />
                <Typography.Title level={4}>{current.title || t("chat.untitled")}</Typography.Title>
                <Tag>{currentKnowledgeBaseName}</Tag>
              </Space>
              <Typography.Text type="secondary">{formatTime(current.updatedAt)}</Typography.Text>
            </header>

            <VirtualMessageList
              rows={messageRows}
              autoScrollKey={`${history.length}:${pendingUser}:${asking}:${states.length}:${streamed?.content ? 1 : 0}`}
              renderRow={(row) => {
                switch (row.kind) {
                  case "loading":
                    return <LoadingBlock />;
                  case "error":
                    return <QueryFallback error={row.error} retry={() => void messages.refetch()} />;
                  case "empty":
                    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noMessages")} />;
                  case "message":
                    return (
                      <MessageBubble
                        item={row.item}
                        onOpenCitation={openCitation}
                        onShowCitations={(citations) => setCitationDrawer({ title: current.title || t("chat.untitled"), citations })}
                      />
                    );
                  case "pending":
                    return (
                      <div className="message-row user">
                        <div className="chat-bubble user">
                          <Typography.Paragraph>{row.content}</Typography.Paragraph>
                        </div>
                      </div>
                    );
                  case "asking":
                    return (
                      <div className="message-row assistant">
                        <div className="chat-bubble assistant">
                          <Flex align="center" gap={8} className="chat-progress">
                            <Spin size="small" />
                            <Typography.Text type="secondary">
                              {streamPhase === "reconnecting"
                                ? t("chat.reconnecting")
                                : states.length > 0 ? states.join(" → ") : t("chat.processing")}
                            </Typography.Text>
                          </Flex>
                          {streamed && (
                            <AnswerText content={streamed.content} citations={streamed.citations} onOpen={openCitation} />
                          )}
                          {streamed && streamed.citations.length > 0 && (
                            <CitationList citations={streamed.citations} onOpen={openCitation} />
                          )}
                        </div>
                      </div>
                    );
                  case "stream-error":
                    return <Alert type="error" showIcon message={row.message} className="chat-error" />;
                }
              }}
            />

            <div className="chat-composer">
              <Input.TextArea
                value={question}
                onChange={(event) => setQuestion(event.target.value)}
                onPressEnter={(event) => {
                  if (!event.shiftKey) {
                    event.preventDefault();
                    void send();
                  }
                }}
                autoSize={{ minRows: 2, maxRows: 6 }}
                placeholder={t("chat.placeholder")}
                aria-label={t("chat.question")}
                disabled={asking}
              />
              <Button
                type="primary"
                icon={<SendOutlined />}
                loading={asking}
                disabled={!question.trim() || asking}
                onClick={() => void send()}
              >
                {asking ? t("chat.asking") : t("chat.ask")}
              </Button>
            </div>
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

      <Drawer
        open={Boolean(citationDrawer)}
        onClose={() => setCitationDrawer(undefined)}
        title={t("chat.citations")}
        size={480}
      >
        {citationDrawer && citationDrawer.citations.length === 0 && <Empty description={t("chat.noCitations")} />}
        {citationDrawer && (
          <List
            dataSource={citationDrawer.citations}
            renderItem={(citation) => (
              <List.Item>
                <Flex vertical gap={6} className="citation-drawer-item">
                  <Space size={8} wrap>
                    <Tag>[{citation.index}]</Tag>
                    <Button type="link" icon={<FileTextOutlined />} onClick={() => openCitation(citation)}>
                      {citation.headingPath || t("document.citation")}
                    </Button>
                  </Space>
                  <Typography.Text type="secondary">
                    {t("document.position", { page: citation.pageNumber })} · {t("chat.fusionScore")} {citation.score.toFixed(3)}
                  </Typography.Text>
                  <Typography.Paragraph className="quote">{citation.quote}</Typography.Paragraph>
                </Flex>
              </List.Item>
            )}
          />
        )}
      </Drawer>
    </div>
  );
}

function MessageBubble(props: { item: MessageView; onOpenCitation: (citation: Citation) => void; onShowCitations: (citations: Citation[]) => void }) {
  const { item, onOpenCitation, onShowCitations } = props;
  const { t } = useTranslation();
  const isUser = item.message.role === "user";
  return (
    <div className={`message-row ${isUser ? "user" : "assistant"}`}>
      <div className={`chat-bubble ${isUser ? "user" : "assistant"}`}>
        <Flex justify="space-between" align="center" gap={8} className="chat-bubble-meta">
          <Tag color={isUser ? "default" : "green"}>{isUser ? t("chat.me") : t("chat.answer")}</Tag>
          <Space size={8}>
            <Typography.Text type="secondary" className="chat-message-status">
              {item.message.status}
              {item.message.errorCode ? ` · ${item.message.errorCode}` : ""}
            </Typography.Text>
            <Typography.Text type="secondary" className="chat-message-status">{formatTime(item.message.createdAt)}</Typography.Text>
          </Space>
        </Flex>
        {isUser
          ? <Typography.Paragraph>{item.message.content}</Typography.Paragraph>
          : <AnswerText content={item.message.content} citations={item.citations} onOpen={onOpenCitation} />}
        {item.message.errorCode && (
          <Alert type="error" showIcon message={item.message.errorCode} description={item.message.errorMessage} />
        )}
        {item.citations.length > 0 && (
          <>
            <CitationList citations={item.citations} onOpen={onOpenCitation} />
            <Button type="link" size="small" onClick={() => onShowCitations(item.citations)}>
              {t("chat.viewCitations", { count: item.citations.length })}
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

export function CitationList(props: { citations: Citation[]; onOpen: (citation: Citation) => void }) {
  const { t } = useTranslation();
  return (
    <ul className="citation-list">
      {props.citations.map((citation) => (
        <li key={citation.chunkId}>
          <Button type="link" className="citation-link" onClick={() => props.onOpen(citation)}>
            [{citation.index}] {citation.headingPath || t("chat.citationFallback")}
          </Button>
          <Typography.Text type="secondary" className="citation-meta">
            {t("document.position", { page: citation.pageNumber })} · {t("chat.fusionScore")} {citation.score.toFixed(3)}
          </Typography.Text>
          <Typography.Paragraph className="citation-quote">{citation.quote}</Typography.Paragraph>
        </li>
      ))}
    </ul>
  );
}

// Answer markers such as [1] become the same navigation target as the
// citation list, so a citation always opens its chunk in document detail.
export function AnswerText(props: { content: string; citations: Citation[]; onOpen: (citation: Citation) => void }) {
  const byIndex = useMemo(() => {
    const map = new Map<number, Citation>();
    for (const citation of props.citations) map.set(citation.index, citation);
    return map;
  }, [props.citations]);
  const parts = props.content.split(/(\[\d{1,3}\])/g);
  return (
    <Typography.Paragraph className="answer">
      {parts.map((part, index) => {
        const match = /^\[(\d{1,3})\]$/.exec(part);
        const citation = match ? byIndex.get(Number(match[1])) : undefined;
        if (!citation) return <span key={index}>{part}</span>;
        return (
          <Button key={index} size="small" className="citation-chip" onClick={() => props.onOpen(citation)}>
            {part}
          </Button>
        );
      })}
    </Typography.Paragraph>
  );
}
