import { Alert, App, Button, Flex, Space, Switch, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys } from "../../api/hooks";
import type { IndexingStrategy, KnowledgeBaseConfigurationV1 } from "../../api/types";
import { normalizedStrategy } from "./retrievalFields";
import { SchemaConfigForm } from "./SchemaConfigForm";

type Props = {
  knowledgeBaseId: string;
  configuration: KnowledgeBaseConfigurationV1;
  canWrite: boolean;
  onSaved: () => void;
};

function strategyOrder(strategy: IndexingStrategy) {
  return JSON.stringify({
    vector_enabled: strategy.vector_enabled,
    keyword_enabled: strategy.keyword_enabled,
    wiki_enabled: strategy.wiki_enabled,
    graph_enabled: strategy.graph_enabled,
    required_capabilities: [...strategy.required_capabilities].sort(),
  });
}

export function RetrievalSettings({ knowledgeBaseId, configuration, canWrite, onSaved }: Props) {
  const { t } = useTranslation();
  const { modal } = App.useApp();
  const queryClient = useQueryClient();
  const [switches, setSwitches] = useState<IndexingStrategy>(() => normalizedStrategy(configuration.indexingStrategy));
  const [savingStrategy, setSavingStrategy] = useState(false);
  const [savingRetrieval, setSavingRetrieval] = useState(false);
  const [formError, setFormError] = useState("");

  useEffect(() => {
    setSwitches(normalizedStrategy(configuration.indexingStrategy));
    setFormError("");
  }, [configuration]);

  const initialStrategy = useMemo(() => normalizedStrategy(configuration.indexingStrategy), [configuration]);
  const strategyDirty = strategyOrder(switches) !== strategyOrder(initialStrategy);

  function updateStrategy(patch: Partial<IndexingStrategy>) {
    setSwitches((current) => normalizedStrategy({ ...current, ...patch }));
  }

  function confirmStrategy(): Promise<boolean> {
    const projectionChanged = switches.wiki_enabled !== initialStrategy.wiki_enabled || switches.graph_enabled !== initialStrategy.graph_enabled;
    const noRecallChannel = !switches.vector_enabled && !switches.keyword_enabled;
    if (!projectionChanged && !noRecallChannel) return Promise.resolve(true);
    return new Promise((resolve) => {
      modal.confirm({
        title: t("settings.diffTitle"),
        content: (
          <Space direction="vertical">
            {noRecallChannel && <Typography.Text>{t("settings.bothChannelsOff")}</Typography.Text>}
            {projectionChanged && <Typography.Text>{t("settings.projectionChange")}</Typography.Text>}
          </Space>
        ),
        okText: t("common.confirm"),
        cancelText: t("common.cancel"),
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
  }

  async function saveStrategy() {
    if (!canWrite || !strategyDirty || savingStrategy) return;
    if (switches.required_capabilities.length === 0) switches.required_capabilities = ["evidence"];
    if (!(await confirmStrategy())) return;
    setSavingStrategy(true);
    setFormError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}`, jsonInit({ indexing_strategy: switches }, "PATCH"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.projection(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBaseId) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases }),
      ]);
      onSaved();
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    } finally {
      setSavingStrategy(false);
    }
  }

  async function putOverride(override: Record<string, unknown>) {
    if (savingRetrieval) return;
    setSavingRetrieval(true);
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/retrieval-config`, jsonInit(override, "PUT"));
      await queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) });
      onSaved();
    } finally {
      setSavingRetrieval(false);
    }
  }

  return (
    <div className="settings-section">
      {!canWrite && <Alert type="info" showIcon message={t("settings.readOnlyHint")} className="settings-alert" />}
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("settings.retrieval")}</Typography.Title>
            <Typography.Text type="secondary">{t("settings.overviewNote")}</Typography.Text>
          </div>
        </header>
        <div className="switch-grid">
          {([
            ["vector_enabled", "retrieval.switchVector", "retrieval.switchVectorHint"],
            ["keyword_enabled", "retrieval.switchKeyword", "retrieval.switchKeywordHint"],
            ["wiki_enabled", "retrieval.switchWiki", "retrieval.switchWikiHint"],
            ["graph_enabled", "retrieval.switchGraph", "retrieval.switchGraphHint"],
          ] as const).map(([key, labelKey, hintKey]) => (
            <div className="switch-row" key={key}>
              <div>
                <Typography.Text strong>{t(labelKey)}</Typography.Text>
                <div className="setting-help">{t(hintKey)}</div>
              </div>
              <Switch
                checked={switches[key]}
                onChange={(checked) => updateStrategy({ [key]: checked })}
                disabled={!canWrite || savingStrategy}
                aria-label={t(labelKey)}
              />
            </div>
          ))}
        </div>
        <div className="capability-row">
          <Typography.Text type="secondary">{t("retrieval.requiredCapabilities")}</Typography.Text>
          <Space wrap>
            {switches.required_capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}
          </Space>
        </div>
        {formError && <Alert type="error" showIcon message={formError} className="settings-alert" />}
        <Flex justify="flex-end" gap={8} className="settings-actions">
          <Button type="primary" onClick={() => void saveStrategy()} loading={savingStrategy} disabled={!canWrite || !strategyDirty}>
            {t("retrieval.strategySave")}
          </Button>
        </Flex>
      </section>

      <SchemaConfigForm
        domain={configuration.retrieval}
        override={configuration.retrieval.knowledgeBaseOverride as unknown as Record<string, unknown>}
        titleKey="settings.retrievalEffective"
        hintKey="retrieval.overrideHint"
        canWrite={canWrite}
        saving={savingRetrieval}
        onSave={putOverride}
        onClear={() => putOverride({})}
      />
    </div>
  );
}
