import {
  DislikeOutlined,
  FileTextOutlined,
  LikeOutlined,
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
  Space,
  Spin,
  Tag,
  Typography,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { streamAnswer, type AnswerStreamPhase } from "../api/answerStream";
import { api, jsonInit } from "../api/client";
import { queryKeys, useMessages } from "../api/hooks";
import type { Citation, Conversation, MessageView, StreamEvent } from "../api/types";
import { answerFailureText, statusLabel } from "../utils/enums";
import { formatTime } from "../utils/format";
import { LoadingBlock, QueryFallback } from "./QueryFallback";
import { VirtualMessageList } from "./VirtualMessageList";

type StreamedAnswer = {
  content: string;
  citations: Citation[];
  status: string;
};

type ToolCallView = {
  callId: string;
  name: string;
  state: "running" | "done" | "error";
  evidence?: number;
  truncated?: boolean;
};

type ChatRow =
  | { key: string; kind: "loading" }
  | { key: string; kind: "error"; error: unknown }
  | { key: string; kind: "empty" }
  | { key: string; kind: "message"; item: MessageView }
  | { key: string; kind: "pending"; content: string }
  | { key: string; kind: "asking" }
  | { key: string; kind: "stream-error"; message: string };

const noMessages: MessageView[] = [];

export function ChatConversation(props: { conversation: Conversation; compact?: boolean }) {
  const { conversation, compact } = props;
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const messages = useMessages(conversation.id);
  const [question, setQuestion] = useState("");
  const [asking, setAsking] = useState(false);
  const [states, setStates] = useState<string[]>([]);
  const [pendingUser, setPendingUser] = useState("");
  const [streamed, setStreamed] = useState<StreamedAnswer>();
  const [toolCalls, setToolCalls] = useState<ToolCallView[]>([]);
  const [streamError, setStreamError] = useState("");
  const [streamPhase, setStreamPhase] = useState<AnswerStreamPhase>();
  const [citationDrawer, setCitationDrawer] = useState<{ title: string; citations: Citation[] }>();
  const mounted = useRef(true);
  const activeStream = useRef<AbortController | null>(null);

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
    setToolCalls([]);
    setStreamError("");
    setStreamPhase(undefined);
  }, [conversation.id]);

  async function send() {
    const query = question.trim();
    if (!query || asking) return;
    const controller = new AbortController();
    activeStream.current?.abort();
    activeStream.current = controller;
    setQuestion("");
    setAsking(true);
    setStates([]);
    setStreamed(undefined);
    setToolCalls([]);
    setStreamError("");
    setPendingUser(query);
    try {
      await streamAnswer({
        conversationId: conversation.id,
        query,
        requestId: crypto.randomUUID(),
        signal: controller.signal,
        onStatus: setStreamPhase,
        onEvent: (event: StreamEvent) => {
          if (event.type === "status") {
            const state = String(event.data?.state ?? "");
            if (state) setStates((existing) => (existing.includes(state) ? existing : [...existing, state]));
          } else if (event.type === "tool_call") {
            const callId = String(event.data?.callId ?? "");
            const name = String(event.data?.tool ?? "");
            if (callId && name) {
              setToolCalls((existing) => [...existing, { callId, name, state: "running" }]);
            }
          } else if (event.type === "tool_result") {
            const callId = String(event.data?.callId ?? "");
            setToolCalls((existing) => existing.map((call) => call.callId === callId
              ? {
                ...call,
                state: event.data?.isError ? "error" : "done",
                evidence: Number(event.data?.evidence ?? 0),
                truncated: Boolean(event.data?.truncated),
              }
              : call));
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
        queryClient.invalidateQueries({ queryKey: queryKeys.messages(conversation.id) }),
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
    const from = conversation.knowledgeBaseId ? `&from=${encodeURIComponent(conversation.knowledgeBaseId)}` : "";
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
    <div className={compact ? "chat-conversation compact" : "chat-conversation"}>
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
                  conversationId={conversation.id}
                  onOpenCitation={openCitation}
                  onShowCitations={(citations) => setCitationDrawer({ title: conversation.title || t("chat.untitled"), citations })}
                />
              );
            case "pending":
              return (
                <div className="message-row user" data-testid="message-user">
                  <div className="chat-bubble user">
                    <Typography.Paragraph>{row.content}</Typography.Paragraph>
                  </div>
                </div>
              );
            case "asking":
              return (
                <div className="message-row assistant" data-testid="message-streaming">
                  <div className="chat-bubble assistant">
                    <Flex align="center" gap={8} className="chat-progress">
                      <Spin size="small" />
                      <Typography.Text type="secondary">
                        {streamPhase === "reconnecting"
                          ? t("chat.reconnecting")
                          : states.length > 0 ? states.join(" → ") : t("chat.processing")}
                      </Typography.Text>
                    </Flex>
                    {toolCalls.length > 0 && (
                      <Space orientation="vertical" size={2} className="chat-tool-calls">
                        {toolCalls.map((call) => (
                          <Tag key={call.callId} color={call.state === "error" ? "red" : call.state === "done" ? "green" : "blue"}>
                            {call.name} · {call.state === "running"
                              ? t("chat.toolRunning")
                              : call.state === "error"
                                ? t("chat.toolFailed")
                                : t("chat.toolDone", { count: call.evidence ?? 0 })}
                            {call.truncated ? ` · ${t("chat.toolTruncated")}` : ""}
                          </Tag>
                        ))}
                      </Space>
                    )}
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

      <div className="chat-composer" data-testid="chat-composer">
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
                    {t("document.position", { page: citation.pageNumber })}
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

function MessageBubble(props: { item: MessageView; conversationId: string; onOpenCitation: (citation: Citation) => void; onShowCitations: (citations: Citation[]) => void }) {
  const { item, conversationId, onOpenCitation, onShowCitations } = props;
  const { t } = useTranslation();
  const { message: toast } = App.useApp();
  const [rating, setRating] = useState("");
  const [ratingBusy, setRatingBusy] = useState(false);
  const isUser = item.message.role === "user";

  async function submitRating(kind: "helpful" | "unhelpful") {
    setRatingBusy(true);
    try {
      await api(`/sessions/${conversationId}/messages/${item.message.id}/feedback`, jsonInit({ kind }));
      setRating(kind);
      toast.success(t("chat.feedbackThanks"));
    } catch {
      toast.error(t("chat.feedbackFailed"));
    } finally {
      setRatingBusy(false);
    }
  }

  function openCitation(citation: Citation) {
    void api(`/sessions/${conversationId}/messages/${item.message.id}/feedback`, jsonInit({
      kind: "citation_click",
      chunkId: citation.chunkId,
    })).catch(() => undefined);
    onOpenCitation(citation);
  }
  return (
    <div className={`message-row ${isUser ? "user" : "assistant"}`} data-testid={isUser ? "message-user" : "message-assistant"}>
      <div className={`chat-bubble ${isUser ? "user" : "assistant"}`}>
        <Flex justify="space-between" align="center" gap={8} className="chat-bubble-meta">
          <Tag color={isUser ? "default" : "green"}>{isUser ? t("chat.me") : t("chat.answer")}</Tag>
          <Space size={8}>
            <Typography.Text type="secondary" className="chat-message-status">
              {statusLabel(t, item.message.status)}
            </Typography.Text>
            <Typography.Text type="secondary" className="chat-message-status">{formatTime(item.message.createdAt)}</Typography.Text>
          </Space>
        </Flex>
        {isUser
          ? <Typography.Paragraph>{item.message.content}</Typography.Paragraph>
          : <AnswerText content={item.message.content} citations={item.citations} onOpen={openCitation} />}
        {item.message.errorCode && (
          <Alert type="error" showIcon message={answerFailureText(t, item.message.errorCode)} />
        )}
        {item.citations.length > 0 && (
          <>
            <CitationList citations={item.citations} onOpen={openCitation} />
            <Button type="link" size="small" onClick={() => onShowCitations(item.citations)}>
              {t("chat.viewCitations", { count: item.citations.length })}
            </Button>
          </>
        )}
        {!isUser && item.message.status !== "FAILED" && (
          <Space size={4} className="chat-feedback">
            <Typography.Text type="secondary">{t("chat.feedbackPrompt")}</Typography.Text>
            <Button
              data-testid="message-feedback-helpful"
              size="small"
              type={rating === "helpful" ? "primary" : "text"}
              icon={<LikeOutlined />}
              loading={ratingBusy}
              onClick={() => void submitRating("helpful")}
            >
              {t("chat.feedbackHelpful")}
            </Button>
            <Button
              data-testid="message-feedback-unhelpful"
              size="small"
              type={rating === "unhelpful" ? "primary" : "text"}
              icon={<DislikeOutlined />}
              loading={ratingBusy}
              onClick={() => void submitRating("unhelpful")}
            >
              {t("chat.feedbackUnhelpful")}
            </Button>
          </Space>
        )}
      </div>
    </div>
  );
}

export function CitationList(props: { citations: Citation[]; onOpen: (citation: Citation) => void }) {
  const { t } = useTranslation();
  return (
    <ul className="citation-list" data-testid="citation-list">
      {props.citations.map((citation) => (
        <li key={citation.chunkId}>
          <Button type="link" className="citation-link" onClick={() => props.onOpen(citation)}>
            [{citation.index}] {citation.headingPath || t("chat.citationFallback")}
          </Button>
          <Typography.Text type="secondary" className="citation-meta">
            {t("document.position", { page: citation.pageNumber })}
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