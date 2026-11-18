import { PlusOutlined } from "@ant-design/icons";
import { Button, Flex, Typography } from "antd";
import { useMemo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Conversation } from "../api/types";
import { formatTime } from "../utils/format";
import { LoadingBlock, QueryFallback } from "./QueryFallback";
import { VirtualSessionList } from "./VirtualSessionList";

export type SessionRow =
  | { key: string; kind: "group"; name: string }
  | { key: string; kind: "conversation"; conversation: Conversation };

type Props = {
  items: Conversation[];
  activeId?: string;
  onSelect: (conversation: Conversation) => void;
  onCreate: () => void;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  creating?: boolean;
  // Knowledge base panels list one knowledge base, so grouping is optional.
  grouped?: boolean;
  knowledgeBaseNames?: Map<string, string>;
  actions?: ReactNode;
};

export function SessionList(props: Props) {
  const { t } = useTranslation();
  const { items, grouped, knowledgeBaseNames } = props;

  const rows = useMemo<SessionRow[]>(() => {
    const sorted = [...items].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
    if (!grouped) {
      return sorted.map((conversation) => ({ key: `conversation:${conversation.id}`, kind: "conversation", conversation }));
    }
    const groups = new Map<string, Conversation[]>();
    for (const conversation of sorted) {
      const list = groups.get(conversation.knowledgeBaseId) ?? [];
      list.push(conversation);
      groups.set(conversation.knowledgeBaseId, list);
    }
    const next: SessionRow[] = [];
    for (const [knowledgeBaseId, conversations] of groups) {
      next.push({
        key: `group:${knowledgeBaseId}`,
        kind: "group",
        name: knowledgeBaseNames?.get(knowledgeBaseId) ?? knowledgeBaseId,
      });
      for (const conversation of conversations) {
        next.push({ key: `conversation:${conversation.id}`, kind: "conversation", conversation });
      }
    }
    return next;
  }, [items, grouped, knowledgeBaseNames]);

  return (
    <div className="session-list">
      <Flex justify="space-between" align="center" gap={8} className="session-list-heading">
        <Typography.Text strong>{t("chat.conversations")}</Typography.Text>
        <Flex align="center" gap={4}>
          {props.actions}
          <Button
            type="primary"
            size="small"
            icon={<PlusOutlined />}
            loading={props.creating}
            onClick={props.onCreate}
            data-testid="session-new"
          >
            {t("chat.newShort")}
          </Button>
        </Flex>
      </Flex>
      {props.loading && <LoadingBlock />}
      {Boolean(props.error) && <QueryFallback error={props.error} retry={props.onRetry ?? (() => undefined)} />}
      {!props.loading && !props.error && items.length === 0 && (
        <div className="session-list-empty">
          <Typography.Paragraph type="secondary">{t("chat.noConversations")}</Typography.Paragraph>
          <Button size="small" icon={<PlusOutlined />} onClick={props.onCreate} loading={props.creating}>
            {t("chat.newConversation")}
          </Button>
        </div>
      )}
      {!props.loading && !props.error && items.length > 0 && (
        <VirtualSessionList
          rows={rows}
          estimateSize={(index) => (rows[index]?.kind === "group" ? 38 : 56)}
          renderRow={(row) => row.kind === "group" ? (
            <Typography.Text type="secondary" className="chat-session-group">
              {row.name}
            </Typography.Text>
          ) : (
            <button
              type="button"
              className={row.conversation.id === props.activeId ? "chat-session active" : "chat-session"}
              onClick={() => props.onSelect(row.conversation)}
            >
              <Flex vertical gap={2} className="chat-session-body">
                <Typography.Text ellipsis>{row.conversation.title || t("chat.untitled")}</Typography.Text>
                <Typography.Text type="secondary" className="chat-session-time">{formatTime(row.conversation.updatedAt)}</Typography.Text>
              </Flex>
            </button>
          )}
        />
      )}
    </div>
  );
}
