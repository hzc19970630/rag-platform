import { Alert, Button, Flex, Select, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useModelCatalog } from "../../api/hooks";
import type { KnowledgeBaseConfigurationV1, ModelCatalogEntry } from "../../api/types";

type Props = {
  knowledgeBaseId: string;
  configuration: KnowledgeBaseConfigurationV1;
  canWrite: boolean;
  onSaved: () => void;
};

const capabilities = ["chat", "rerank"] as const;

export function ModelSettings({ knowledgeBaseId, configuration, canWrite, onSaved }: Props) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const catalog = useModelCatalog();
  const binding = configuration.modelBinding;
  const [chatModel, setChatModel] = useState(() => (binding.chat.source === "knowledge_base" ? binding.chat.model : ""));
  const [rerankModel, setRerankModel] = useState(() => (binding.rerank.source === "knowledge_base" ? binding.rerank.model : ""));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    setChatModel(binding.chat.source === "knowledge_base" ? binding.chat.model : "");
    setRerankModel(binding.rerank.source === "knowledge_base" ? binding.rerank.model : "");
    setError("");
  }, [binding.chat.source, binding.chat.model, binding.rerank.source, binding.rerank.model]);

  const options = useMemo(() => {
    const build = (capability: (typeof capabilities)[number]) => {
      const seen = new Map<string, ModelCatalogEntry>();
      for (const entry of catalog.data ?? []) {
        if (entry.capability === capability && entry.enabled && !seen.has(entry.model)) {
          seen.set(entry.model, entry);
        }
      }
      return [
        { value: "", label: t("models.inherit") },
        ...[...seen.values()].map((entry) => ({
          value: entry.model,
          label: entry.source === "deployment" ? `${entry.model} · ${t("models.sourceDeployment")}` : `${entry.model} · ${entry.provider}`,
        })),
      ];
    };
    return { chat: build("chat"), rerank: build("rerank") };
  }, [catalog.data, t]);

  const dirty = chatModel !== (binding.chat.source === "knowledge_base" ? binding.chat.model : "")
    || rerankModel !== (binding.rerank.source === "knowledge_base" ? binding.rerank.model : "");

  async function save() {
    setSaving(true);
    setError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}`, jsonInit({ chat_model: chatModel, rerank_model: rerankModel }, "PATCH"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) }),
      ]);
      onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="settings-section">
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("models.title")}</Typography.Title>
            <Typography.Text type="secondary">{t("models.hint")}</Typography.Text>
          </div>
          <Tag color="blue">{t("models.routing")}</Tag>
        </header>
        <Flex vertical gap={16}>
          <div>
            <Typography.Text strong>{t("models.chat")}</Typography.Text>
            <Select
              className="settings-select"
              value={chatModel}
              options={options.chat}
              onChange={setChatModel}
              disabled={!canWrite || saving}
              loading={catalog.isLoading}
              showSearch
              optionFilterProp="label"
            />
            <Typography.Text type="secondary">
              {binding.chat.source === "knowledge_base" ? t("models.sourceKnowledgeBase") : t("models.sourceDeployment")}
              {binding.chat.version ? ` · ${binding.chat.version}` : ""}
            </Typography.Text>
          </div>
          <div>
            <Typography.Text strong>{t("models.rerank")}</Typography.Text>
            <Select
              className="settings-select"
              value={rerankModel}
              options={options.rerank}
              onChange={setRerankModel}
              disabled={!canWrite || saving}
              loading={catalog.isLoading}
              showSearch
              optionFilterProp="label"
            />
            <Typography.Text type="secondary">
              {binding.rerank.source === "knowledge_base" ? t("models.sourceKnowledgeBase") : t("models.sourceDeployment")}
              {binding.rerank.version ? ` · ${binding.rerank.version}` : ""}
            </Typography.Text>
          </div>
          <div>
            <Typography.Text strong>{t("models.embedding")}</Typography.Text>
            <div>
              <Tag>{`${binding.embedding.model || configuration.readOnly.embedding.model} · ${binding.embeddingDimension || configuration.readOnly.embedding.dimension}`}</Tag>
              {binding.embeddingLocked ? <Tag color="orange">{t("models.embeddingLocked")}</Tag> : null}
            </div>
            <Typography.Text type="secondary">{binding.embeddingLockReason || t("models.embeddingHint")}</Typography.Text>
          </div>
          {error ? <Alert type="error" showIcon message={error} /> : null}
          {canWrite ? (
            <Button type="primary" loading={saving} disabled={!dirty} onClick={() => void save()}>
              {t("settings.saveSection", { section: t("models.title") })}
            </Button>
          ) : (
            <Alert type="info" showIcon message={t("settings.readOnlyHint")} />
          )}
        </Flex>
      </section>
    </div>
  );
}