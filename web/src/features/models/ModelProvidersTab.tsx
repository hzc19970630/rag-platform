import { DeleteOutlined, EditOutlined, ExperimentOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, App, Button, Card, Drawer, Flex, Input, InputNumber, Select, Space, Switch, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useModelCatalog, useModelProviders } from "../../api/hooks";
import type { ModelCatalogEntry, ModelProvider, ModelProviderTestResult, ModelProviderUpsertRequest } from "../../api/types";
import { capabilityLabel, providerKindLabel } from "../../utils/enums";

export type DraftModel = {
  model: string;
  displayName: string;
  capability: "chat" | "embedding" | "rerank";
  version: string;
  dimension: number;
  enabled: boolean;
};

export type Draft = {
  name: string;
  kind: "openai_compatible" | "ollama";
  baseUrl: string;
  apiKey: string;
  apiKeySet: boolean;
  healthUrl: string;
  enabled: boolean;
  models: DraftModel[];
};

function emptyDraft(): Draft {
  return {
    name: "", kind: "openai_compatible", baseUrl: "", apiKey: "", apiKeySet: false,
    healthUrl: "", enabled: true, models: [{ model: "", displayName: "", capability: "chat", version: "", dimension: 0, enabled: true }],
  };
}

function draftFrom(provider: ModelProvider): Draft {
  return {
    name: provider.name, kind: provider.kind === "ollama" ? "ollama" : "openai_compatible",
    baseUrl: provider.baseUrl, apiKey: "", apiKeySet: provider.apiKeySet, healthUrl: provider.healthUrl,
    enabled: provider.enabled,
    models: provider.models.map((entry) => ({
      model: entry.model, displayName: entry.displayName,
      capability: (entry.capability as DraftModel["capability"]) ?? "chat",
      version: entry.version, dimension: entry.dimension, enabled: entry.enabled,
    })),
  };
}

// buildProviderRequest keeps the wire shape in one testable place: a blank key
// means "keep the stored one", and only embedding entries carry a dimension.
export function buildProviderRequest(draft: Draft): ModelProviderUpsertRequest {
  return {
    name: draft.name.trim(),
    kind: draft.kind,
    base_url: draft.baseUrl.trim(),
    api_key: draft.apiKey === "" ? undefined : draft.apiKey,
    health_url: draft.healthUrl.trim(),
    enabled: draft.enabled,
    models: draft.models
      .filter((entry) => entry.model.trim() !== "")
      .map((entry) => ({
        model: entry.model.trim(), display_name: entry.displayName.trim(),
        capability: entry.capability, version: entry.version.trim(),
        dimension: entry.capability === "embedding" ? entry.dimension : 0, enabled: entry.enabled,
      })),
  };
}

// deploymentCatalog exposes only the models registered from env/YAML. They are
// merged into the catalog so knowledge bases can inherit them, but they are not
// tenant provider rows and must not be editable from the provider page.
export function deploymentCatalog(items: ModelCatalogEntry[]): ModelCatalogEntry[] {
  return items.filter((entry) => entry.source === "deployment");
}

export function ModelProvidersTab() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const providers = useModelProviders();
  const catalog = useModelCatalog();
  const [open, setOpen] = useState(false);
  const [editingId, setEditingId] = useState("");
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [testingId, setTestingId] = useState("");

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: queryKeys.modelProviders });
    void queryClient.invalidateQueries({ queryKey: queryKeys.modelCatalog });
  }

  function startCreate() {
    setEditingId("");
    setDraft(emptyDraft());
    setError("");
    setOpen(true);
  }

  function startEdit(provider: ModelProvider) {
    setEditingId(provider.id);
    setDraft(draftFrom(provider));
    setError("");
    setOpen(true);
  }

  function patchDraft(patch: Partial<Draft>) {
    setDraft((current) => ({ ...current, ...patch }));
  }

  function patchModel(index: number, patch: Partial<DraftModel>) {
    setDraft((current) => ({
      ...current,
      models: current.models.map((entry, position) => (position === index ? { ...entry, ...patch } : entry)),
    }));
  }

  async function save() {
    setSaving(true);
    setError("");
    const payload = buildProviderRequest(draft);
    try {
      if (editingId) {
        await api(`/models/providers/${encodeURIComponent(editingId)}`, jsonInit(payload, "PATCH"));
      } else {
        await api("/models/providers", jsonInit(payload));
      }
      message.success(t("admin.modelProviders.saved"));
      setOpen(false);
      refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function test(provider: ModelProvider) {
    setTestingId(provider.id);
    try {
      const result = await api<ModelProviderTestResult>(`/models/providers/${encodeURIComponent(provider.id)}/test`, jsonInit({}));
      if (result.healthy) message.success(t("admin.modelProviders.testOk"));
      else message.error(`${t("admin.modelProviders.testFailed")}: ${result.error ?? ""}`);
      refresh();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("admin.modelProviders.testFailed"));
    } finally {
      setTestingId("");
    }
  }

  function remove(provider: ModelProvider) {
    modal.confirm({
      title: t("admin.modelProviders.deleteTitle", { name: provider.name }),
      content: t("admin.modelProviders.deleteConfirm"),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await api(`/models/providers/${encodeURIComponent(provider.id)}`, { method: "DELETE" });
          message.success(t("admin.modelProviders.deleted"));
          refresh();
        } catch (cause) {
          message.error(cause instanceof Error ? cause.message : t("admin.modelProviders.deleteFailed"));
        }
      },
    });
  }

  const deploymentModels = deploymentCatalog(catalog.data ?? []);
  const deploymentColumns: TableColumnsType<ModelCatalogEntry> = [
    {
      title: t("admin.modelProviders.capability"), dataIndex: "capability", key: "capability", width: 130,
      render: (value: string) => <Tag>{capabilityLabel(t, value)}</Tag>,
    },
    {
      title: t("admin.modelProviders.model"), key: "model",
      render: (_, entry) => (
        <Flex vertical gap={0}>
          <Typography.Text strong>{entry.model}</Typography.Text>
          <Typography.Text type="secondary">{entry.provider}</Typography.Text>
        </Flex>
      ),
    },
    { title: t("admin.modelProviders.version"), dataIndex: "version", key: "version", width: 130, render: (value: string) => value || "deployment" },
    {
      title: t("admin.modelProviders.dimension"), dataIndex: "dimension", key: "dimension", width: 110,
      render: (value: number, entry) => (entry.capability === "embedding" && value > 0 ? value : "—"),
    },
    {
      title: t("admin.modelProviders.status"), key: "status", width: 120,
      render: () => <Tag color="blue">{t("admin.modelProviders.deploymentSource")}</Tag>,
    },
  ];

  const columns: TableColumnsType<ModelProvider> = [
    { title: t("admin.modelProviders.name"), dataIndex: "name", key: "name" },    {
      title: t("admin.modelProviders.kind"), dataIndex: "kind", key: "kind", width: 150,
      render: (value: string) => <Tag>{providerKindLabel(t, value)}</Tag>,
    },
    { title: t("admin.modelProviders.baseUrl"), dataIndex: "baseUrl", key: "baseUrl", render: (value: string) => <span className="mono-small">{value}</span> },
    {
      title: t("admin.modelProviders.models"), key: "models", width: 120,
      render: (_, provider) => provider.models.map((entry) => <Tag key={entry.id || entry.model}>{entry.model}</Tag>),
    },
    {
      title: t("admin.modelProviders.status"), dataIndex: "enabled", key: "enabled", width: 100,
      render: (value: boolean) => <Tag color={value ? "green" : "default"}>{value ? t("admin.modelProviders.enabled") : t("admin.modelProviders.disabled")}</Tag>,
    },
    {
      title: t("admin.modelProviders.actions"), key: "actions", width: 260,
      render: (_, provider) => (
        <Flex gap={8}>
          <Button size="small" icon={<EditOutlined />} onClick={() => startEdit(provider)}>{t("admin.modelProviders.edit")}</Button>
          <Button size="small" icon={<ExperimentOutlined />} loading={testingId === provider.id} onClick={() => void test(provider)}>{t("admin.modelProviders.test")}</Button>
          <Button size="small" danger icon={<DeleteOutlined />} onClick={() => remove(provider)}>{t("admin.modelProviders.delete")}</Button>
        </Flex>
      ),
    },
  ];

  return (
    <div className="admin-section">
      <Flex justify="space-between" align="center" className="admin-section-heading">
        <div>
          <Typography.Title level={4}>{t("admin.modelProviders.title")}</Typography.Title>
          <Typography.Text type="secondary">{t("admin.modelProviders.hint")}</Typography.Text>
        </div>
        <Flex gap={8}>
          <Button icon={<ReloadOutlined />} onClick={refresh}>{t("admin.modelProviders.refresh")}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={startCreate}>{t("admin.modelProviders.add")}</Button>
        </Flex>
      </Flex>
      <Card
        size="small"
        className="deployment-models-card"
        title={<Space size={8}><Typography.Text strong>{t("admin.modelProviders.deploymentTitle")}</Typography.Text><Tag color="blue">{t("admin.modelProviders.deploymentReadOnly")}</Tag></Space>}
        extra={<Typography.Text type="secondary">{t("admin.modelProviders.deploymentHint")}</Typography.Text>}
        style={{ marginBottom: 16 }}
      >
        {catalog.error && <Alert type="error" showIcon message={t("admin.modelProviders.deploymentLoadFailed")} description={catalog.error.message} style={{ marginBottom: 12 }} />}
        <Table
          rowKey={(entry) => `${entry.model}:${entry.capability}`}
          size="small"
          columns={deploymentColumns}
          dataSource={deploymentModels}
          loading={catalog.isLoading}
          pagination={false}
          locale={{ emptyText: t("admin.modelProviders.deploymentEmpty") }}
        />
      </Card>
      <Table rowKey="id" size="small" columns={columns} dataSource={providers.data ?? []} loading={providers.isLoading} pagination={false} />

      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        size={720}
        title={editingId ? t("admin.modelProviders.editTitle") : t("admin.modelProviders.addTitle")}
        extra={<Button type="primary" loading={saving} onClick={() => void save()}>{t("admin.modelProviders.save")}</Button>}
      >
        <Flex vertical gap={12}>
          <label>
            <Typography.Text strong>{t("admin.modelProviders.name")}</Typography.Text>
            <Input value={draft.name} onChange={(event) => patchDraft({ name: event.target.value })} />
          </label>
          <label>
            <Typography.Text strong>{t("admin.modelProviders.kind")}</Typography.Text>
            <Select
              value={draft.kind}
              onChange={(value) => patchDraft({ kind: value })}
              options={[
                { value: "openai_compatible", label: providerKindLabel(t, "openai_compatible") },
                { value: "ollama", label: providerKindLabel(t, "ollama") },
              ]}
            />
          </label>
          <label>
            <Typography.Text strong>{t("admin.modelProviders.baseUrl")}</Typography.Text>
            <Input value={draft.baseUrl} placeholder="https://api.example.com/v1" onChange={(event) => patchDraft({ baseUrl: event.target.value })} />
          </label>
          <label>
            <Typography.Text strong>{t("admin.modelProviders.apiKey")}</Typography.Text>
            <Input.Password
              value={draft.apiKey}
              placeholder={draft.apiKeySet ? t("admin.modelProviders.apiKeySet") : ""}
              onChange={(event) => patchDraft({ apiKey: event.target.value })}
            />
            <Typography.Text type="secondary">{t("admin.modelProviders.apiKeyHint")}</Typography.Text>
          </label>
          <label>
            <Typography.Text strong>{t("admin.modelProviders.healthUrl")}</Typography.Text>
            <Input value={draft.healthUrl} onChange={(event) => patchDraft({ healthUrl: event.target.value })} />
          </label>
          <Flex align="center" gap={8}>
            <Switch checked={draft.enabled} onChange={(checked) => patchDraft({ enabled: checked })} />
            <Typography.Text>{t("admin.modelProviders.enabled")}</Typography.Text>
          </Flex>

          <Typography.Title level={5}>{t("admin.modelProviders.models")}</Typography.Title>
          {draft.models.map((entry: DraftModel, index: number) => (
            <Flex key={index} gap={8} align="end" wrap>
              <label>
                <Typography.Text type="secondary">{t("admin.modelProviders.model")}</Typography.Text>
                <Input value={entry.model} onChange={(event) => patchModel(index, { model: event.target.value })} />
              </label>
              <label>
                <Typography.Text type="secondary">{t("admin.modelProviders.capability")}</Typography.Text>
                <Select
                  value={entry.capability}
                  onChange={(value) => patchModel(index, { capability: value })}
                  options={[
                    { value: "chat", label: capabilityLabel(t, "chat") },
                    { value: "embedding", label: capabilityLabel(t, "embedding") },
                    { value: "rerank", label: capabilityLabel(t, "rerank") },
                  ]}
                />
              </label>
              <label>
                <Typography.Text type="secondary">{t("admin.modelProviders.version")}</Typography.Text>
                <Input value={entry.version} onChange={(event) => patchModel(index, { version: event.target.value })} />
              </label>
              {entry.capability === "embedding" ? (
                <label>
                  <Typography.Text type="secondary">{t("admin.modelProviders.dimension")}</Typography.Text>
                  <InputNumber min={1} value={entry.dimension || null} onChange={(value) => patchModel(index, { dimension: value ?? 0 })} />
                </label>
              ) : null}
              <Button
                danger
                disabled={draft.models.length <= 1}
                onClick={() => patchDraft({ models: draft.models.filter((_, position) => position !== index) })}
              >
                {t("admin.modelProviders.removeModel")}
              </Button>
            </Flex>
          ))}
          <Button onClick={() => patchDraft({ models: [...draft.models, { model: "", displayName: "", capability: "chat", version: "", dimension: 0, enabled: true }] })}>
            {t("admin.modelProviders.addModel")}
          </Button>
          <Typography.Text type="secondary">{t("admin.modelProviders.embeddingHint")}</Typography.Text>
          {error ? <Alert type="error" showIcon message={error} /> : null}
        </Flex>
      </Drawer>
    </div>
  );
}