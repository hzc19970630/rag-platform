import { FolderOpenOutlined, FolderOutlined, PlusOutlined } from "@ant-design/icons";
import { Button, Flex, Tooltip, Typography } from "antd";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Conversation } from "../api/types";
import { LoadingBlock, QueryFallback } from "./QueryFallback";
import { VirtualSessionList } from "./VirtualSessionList";

export type SessionRow =
  | { key: string; kind: "group"; name: string; groupId: string }
  | { key: string; kind: "conversation"; conversation: Conversation };

type Props = {
  items: Conversation[];
  activeId?: string;
  onSelect: (conversation: Conversation) => void;
  onCreate: () => void;
  onCreateInGroup?: (knowledgeBaseId: string) => void;
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
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set());

  function toggleGroup(groupId: string) {
    setCollapsedGroups((current) => {
      const next = new Set(current);
      if (next.has(groupId)) next.delete(groupId);
      else next.add(groupId);
      return next;
    });
  }

  const rows = useMemo<SessionRow[]>(() => {
    const sorted = [...items].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
    if (!grouped) {
      return sorted.map((conversation) => ({ key: `conversation:${conversation.id}`, kind: "conversation", conversation }));
    }
    const groups = new Map<string, Conversation[]>();
    for (const conversation of sorted) {
      const group = groups.get(conversation.knowledgeBaseId) ?? [];
      group.push(conversation);
      groups.set(conversation.knowledgeBaseId, group);
    }
    const next: SessionRow[] = [];
    for (const [knowledgeBaseId, conversations] of groups) {
      next.push({
        key: `group:${knowledgeBaseId}`,
        kind: "group",
        groupId: knowledgeBaseId,
        name: knowledgeBaseNames?.get(knowledgeBaseId) ?? knowledgeBaseId,
      });
      if (collapsedGroups.has(knowledgeBaseId)) continue;
      for (const conversation of conversations) {
        next.push({ key: `conversation:${conversation.id}`, kind: "conversation", conversation });
      }
    }
    return next;
  }, [items, grouped, knowledgeBaseNames, collapsedGroups]);

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
        </div>
      )}
      {!props.loading && !props.error && items.length > 0 && (
        <VirtualSessionList
          rows={rows}
          estimateSize={(index) => (rows[index]?.kind === "group" ? 40 : 44)}
          renderRow={(row) => {
            if (row.kind === "group") {
              const groupCollapsed = collapsedGroups.has(row.groupId);
              return (
                <div className="chat-session-group">
                  <button
                    type="button"
                    className="chat-session-group-main"
                    aria-expanded={!groupCollapsed}
                    onClick={() => toggleGroup(row.groupId)}
                  >
                    {groupCollapsed ? <FolderOutlined /> : <FolderOpenOutlined />}
                    <span className="chat-session-group-name">{row.name}</span>
                  </button>
                  {props.onCreateInGroup && (
                    <span className="chat-session-group-actions">
                      <Tooltip title={t("chat.newConversation")}>
                        <Button
                          type="text"
                          size="small"
                          icon={<PlusOutlined />}
                          aria-label={t("chat.newConversation")}
                          loading={props.creating}
                          onClick={() => props.onCreateInGroup?.(row.groupId)}
                        />
                      </Tooltip>
                    </span>
                  )}
                </div>
              );
            }
            return (
              <button
                type="button"
                className={row.conversation.id === props.activeId ? "chat-session active" : "chat-session"}
                onClick={() => props.onSelect(row.conversation)}
              >
                <Typography.Text ellipsis>{row.conversation.title || t("chat.untitled")}</Typography.Text>
              </button>
            );
          }}
        />
      )}
    </div>
  );
}