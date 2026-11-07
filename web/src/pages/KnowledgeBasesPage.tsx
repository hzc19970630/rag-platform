import { DeleteOutlined, MessageOutlined, MoreOutlined, PlusOutlined, SearchOutlined, SettingOutlined } from "@ant-design/icons";
import { App, Button, Card, Dropdown, Flex, Form, Input, Modal, Select, Space, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api, jsonInit } from "../api/client";
import { queryKeys, useKnowledgeBases } from "../api/hooks";
import type { KnowledgeBase } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { LoadingBlock, QueryFallback } from "../components/QueryFallback";
import { usePermissions } from "../session";
import { formatTime, parseIndexingStrategy } from "../utils/format";

type SortKey = "updated" | "name";

export function KnowledgeBasesPage() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const queryClient = useQueryClient();
  const knowledgeBases = useKnowledgeBases();
  const { canWrite, canDelete, canChat } = usePermissions();
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<SortKey>("updated");
  const [createOpen, setCreateOpen] = useState(params.get("new") === "1");
  const [creating, setCreating] = useState(false);
  const [form] = Form.useForm<{ name: string; description?: string }>();

  const rows = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return [...(knowledgeBases.data ?? [])]
      .filter((kb) => !needle || kb.name.toLowerCase().includes(needle) || kb.description.toLowerCase().includes(needle))
      .sort((left, right) => {
        if (sort === "name") return left.name.localeCompare(right.name);
        return new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime();
      });
  }, [knowledgeBases.data, search, sort]);

  async function create(values: { name: string; description?: string }) {
    if (creating) return;
    setCreating(true);
    try {
      const created = await api<KnowledgeBase>("/knowledge-bases", jsonInit({
        name: values.name.trim(),
        description: values.description ?? "",
      }));
      message.success(t("knowledgeBase.created"));
      setCreateOpen(false);
      form.resetFields();
      await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
      if (created?.id) navigate(`/knowledge-bases/${created.id}`);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("knowledgeBase.createFailed"));
    } finally {
      setCreating(false);
    }
  }

  function remove(kb: KnowledgeBase) {
    modal.confirm({
      title: t("settings.deleteTitle", { name: kb.name }),
      content: t("knowledgeBase.deleteConfirm"),
      okText: t("common.delete"),
      okButtonProps: { danger: true },
      cancelText: t("common.cancel"),
      onOk: async () => {
        try {
          await api(`/knowledge-bases/${kb.id}`, { method: "DELETE" });
          message.success(t("settings.deleted"));
          await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("knowledgeBase.deleteFailed"));
        }
      },
    });
  }

  const extra = (
    <Space wrap>
      <Input
        allowClear
        value={search}
        onChange={(event) => setSearch(event.target.value)}
        prefix={<SearchOutlined />}
        placeholder={t("knowledgeBase.searchPlaceholder")}
        aria-label={t("knowledgeBase.searchPlaceholder")}
        style={{ width: 240 }}
      />
      <Select<SortKey>
        value={sort}
        onChange={setSort}
        aria-label={t("knowledgeBase.sort")}
        options={[
          { value: "updated", label: t("knowledgeBase.sortUpdated") },
          { value: "name", label: t("knowledgeBase.sortName") },
        ]}
        style={{ width: 140 }}
      />
      {canWrite && (
        <Button data-testid="kb-create-open" type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
          {t("knowledgeBase.new")}
        </Button>
      )}
    </Space>
  );

  return (
    <div className="page">
      <PageHeader title={t("knowledgeBase.title")} description={t("knowledgeBase.description")} extra={extra} />
      {knowledgeBases.isLoading && <LoadingBlock />}
      {knowledgeBases.error && <QueryFallback error={knowledgeBases.error} retry={() => void knowledgeBases.refetch()} />}
      {knowledgeBases.data && rows.length === 0 && (
        <EmptyState
          description={search ? t("knowledgeBase.noMatch") : t("knowledgeBase.empty")}
          action={canWrite && !search ? () => setCreateOpen(true) : undefined}
          actionLabel={canWrite && !search ? t("knowledgeBase.emptyAction") : undefined}
        />
      )}
      <div className="kb-grid">
        {rows.map((kb) => {
          const strategy = parseIndexingStrategy(kb.indexingStrategy);
          const capabilityTags = strategy ? [
            strategy.vector_enabled && t("knowledgeBase.vector"),
            strategy.keyword_enabled && t("knowledgeBase.keyword"),
            strategy.wiki_enabled && t("knowledgeBase.wiki"),
            strategy.graph_enabled && t("knowledgeBase.graph"),
          ].filter(Boolean) as string[] : [];
          const menu = {
            items: [
              { key: "open", label: t("knowledgeBase.details") },
              { key: "settings", label: t("knowledgeBase.settings"), icon: <SettingOutlined /> },
              ...(canChat ? [{ key: "chat", label: t("knowledgeBase.chat"), icon: <MessageOutlined /> }] : []),
              ...(canDelete ? [{ key: "delete", label: t("common.delete"), icon: <DeleteOutlined />, danger: true }] : []),
            ],
            onClick: ({ key, domEvent }: { key: string; domEvent: { stopPropagation: () => void } }) => {
              domEvent.stopPropagation();
              if (key === "open") navigate(`/knowledge-bases/${kb.id}`);
              if (key === "settings") navigate(`/knowledge-bases/${kb.id}?settings=1`);
              if (key === "chat") navigate(`/chat?kb=${encodeURIComponent(kb.id)}`);
              if (key === "delete") remove(kb);
            },
          };
          return (
            <Card
              key={kb.id}
              className="kb-card"
              hoverable
              onClick={() => navigate(`/knowledge-bases/${kb.id}`)}
              actions={[
                <Button key="open" type="link" onClick={(event) => { event.stopPropagation(); navigate(`/knowledge-bases/${kb.id}`); }}>
                  {t("knowledgeBase.open")}
                </Button>,
                <Dropdown key="more" menu={menu} trigger={["click"]}>
                  <Button type="text" icon={<MoreOutlined />} aria-label={t("common.more")} onClick={(event) => event.stopPropagation()} />
                </Dropdown>,
              ]}
            >
              <div className="kb-card-body">
                <Flex justify="space-between" align="flex-start" gap={8} className="kb-card-title-row">
                  <Typography.Title level={4} ellipsis={{ tooltip: kb.name }}>{kb.name}</Typography.Title>
                  <Tag color="green">{t("knowledgeBase.readable")}</Tag>
                </Flex>
                <Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }} className="kb-description">
                  {kb.description || t("knowledgeBase.noDescription")}
                </Typography.Paragraph>
                <Space size={4} wrap className="kb-capabilities">
                  {capabilityTags.length > 0
                    ? capabilityTags.map((label) => <Tag key={label}>{label}</Tag>)
                    : <Tag>{t("knowledgeBase.noCapability")}</Tag>}
                </Space>
                <Typography.Text type="secondary" className="kb-meta">
                  ACL {kb.aclVersion} · {t("common.updatedAt")} {formatTime(kb.updatedAt)}
                </Typography.Text>
                <Typography.Text type="secondary" className="kb-id" ellipsis={{ tooltip: kb.id }}>{kb.id}</Typography.Text>
              </div>
            </Card>
          );
        })}
      </div>

      <Modal
        open={createOpen}
        title={t("knowledgeBase.new")}
        okText={t("common.create")}
        cancelText={t("common.cancel")}
        confirmLoading={creating}
        onCancel={() => { setCreateOpen(false); form.resetFields(); }}
        onOk={() => form.submit()}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" onFinish={(values) => void create(values)}>
          <Form.Item label={t("knowledgeBase.name")} name="name" rules={[{ required: true, message: t("knowledgeBase.nameRequired") }]}>
            <Input maxLength={255} placeholder={t("knowledgeBase.namePlaceholder")} autoFocus />
          </Form.Item>
          <Form.Item label={t("knowledgeBase.descriptionField")} name="description">
            <Input.TextArea rows={3} placeholder={t("knowledgeBase.descriptionPlaceholder")} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
