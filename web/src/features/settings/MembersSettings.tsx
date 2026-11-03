import { App, Alert, Button, Empty, Input, List, Popconfirm, Select, Space, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useMembers } from "../../api/hooks";
import type { Member } from "../../api/types";
import { LoadingBlock, QueryFallback } from "../../components/QueryFallback";

type Props = {
  knowledgeBaseId: string;
  ownerId: string;
  canManage: boolean;
  onChanged?: () => void;
};

const roles = ["viewer", "editor", "admin"];

export function MembersSettings({ knowledgeBaseId, ownerId, canManage, onChanged }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const members = useMembers(knowledgeBaseId, canManage);
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState("viewer");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  if (!canManage) {
    return <Alert type="info" showIcon message={t("settings.membersHidden")} />;
  }
  if (members.isLoading) return <LoadingBlock />;
  if (members.error) return <QueryFallback error={members.error} retry={() => void members.refetch()} />;

  async function grant() {
    if (!userId.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/members`, jsonInit({ user_id: userId.trim(), role }));
      setUserId("");
      message.success(t("members.granted"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.members(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases }),
        queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) }),
      ]);
      onChanged?.();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("members.grantFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function remove(member: Member) {
    setBusy(true);
    setError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/members/${encodeURIComponent(member.userId)}`, { method: "DELETE" });
      message.success(t("members.removed", { user: member.userId }));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.members(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases }),
        queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) }),
      ]);
      onChanged?.();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("members.removeFailed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="settings-section">
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("members.title")}</Typography.Title>
            <Typography.Text type="secondary">{t("settings.membersHint")}</Typography.Text>
          </div>
        </header>
        {error && <Alert type="error" showIcon message={error} className="settings-alert" />}
        {members.data && members.data.length === 0 && <Empty description={t("members.empty")} />}
        <List
          className="member-list"
          dataSource={members.data ?? []}
          renderItem={(member) => (
            <List.Item
              actions={[
                member.userId === ownerId ? (
                  <Typography.Text type="secondary" key="owner">{t("members.ownerHint")}</Typography.Text>
                ) : (
                  <Popconfirm
                    key="remove"
                    title={t("members.removeConfirm", { user: member.userId })}
                    okText={t("common.confirm")}
                    cancelText={t("common.cancel")}
                    onConfirm={() => void remove(member)}
                  >
                    <Button danger size="small" disabled={busy}>{t("members.remove")}</Button>
                  </Popconfirm>
                ),
              ]}
            >
              <List.Item.Meta
                title={member.userId}
                description={member.userId === ownerId ? "owner" : undefined}
              />
              <Tag color={member.role === "admin" ? "green" : member.role === "editor" ? "blue" : "default"}>{member.role}</Tag>
            </List.Item>
          )}
        />
      </section>

      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("members.add")}</Typography.Title>
            <Typography.Text type="secondary">{t("settings.membersAddHint")}</Typography.Text>
          </div>
        </header>
        <Space wrap align="end" className="inline-form">
          <label className="field-label">
            <span>{t("members.userId")}</span>
            <Input value={userId} onChange={(event) => setUserId(event.target.value)} placeholder="viewer-a" />
          </label>
          <label className="field-label">
            <span>{t("members.role")}</span>
            <Select
              value={role}
              onChange={setRole}
              style={{ width: 140 }}
              options={roles.map((value) => ({ value, label: value }))}
            />
          </label>
          <Button type="primary" onClick={() => void grant()} loading={busy} disabled={!userId.trim()}>{t("members.add")}</Button>
        </Space>
      </section>
    </div>
  );
}
