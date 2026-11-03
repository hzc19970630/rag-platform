import { App, Alert, Button, Flex, InputNumber, Select, Space, Switch, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys } from "../../api/hooks";
import type { IndexingStrategy, KnowledgeBaseConfigurationV1, RetrievalOverride } from "../../api/types";
import { booleanFields, buildOverride, fieldError, normalizedStrategy, numericFields, type BoolValue, type NumericField } from "./retrievalFields";

type Props = {
  knowledgeBaseId: string;
  configuration: KnowledgeBaseConfigurationV1;
  canWrite: boolean;
  onSaved: () => void;
};

const groups: Array<{ key: string; labelKey: string }> = [
  { key: "recall", labelKey: "retrieval.groupRecall" },
  { key: "fusion", labelKey: "retrieval.groupFusion" },
  { key: "expansion", labelKey: "retrieval.groupExpansion" },
  { key: "rerank", labelKey: "retrieval.groupRerank" },
  { key: "context", labelKey: "retrieval.groupContext" },
];

function initialNumbers(override: RetrievalOverride): Record<string, number | null> {
  const values: Record<string, number | null> = {};
  for (const field of [...numericFields, ...booleanFields]) {
    const raw = override[field.key as keyof RetrievalOverride];
    values[field.key] = typeof raw === "number" ? raw : null;
  }
  return values;
}

function initialBooleans(override: RetrievalOverride): Record<string, BoolValue> {
  const values: Record<string, BoolValue> = {};
  for (const field of booleanFields) {
    const raw = override[field.key as keyof RetrievalOverride];
    values[field.key] = typeof raw === "boolean" ? (raw ? "true" : "false") : "";
  }
  return values;
}

function strategyOrder(strategy: IndexingStrategy) {
  return JSON.stringify({
    vector_enabled: strategy.vector_enabled,
    keyword_enabled: strategy.keyword_enabled,
    wiki_enabled: strategy.wiki_enabled,
    graph_enabled: strategy.graph_enabled,
    required_capabilities: [...strategy.required_capabilities].sort(),
  });
}

function retrievalOrder(override: RetrievalOverride) {
  return JSON.stringify(Object.fromEntries(Object.entries(override).sort(([a], [b]) => a.localeCompare(b))));
}

export function RetrievalSettings({ knowledgeBaseId, configuration, canWrite, onSaved }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const [switches, setSwitches] = useState<IndexingStrategy>(() => normalizedStrategy(configuration.indexingStrategy));
  const [numbers, setNumbers] = useState<Record<string, number | null>>(() => initialNumbers(configuration.retrieval.knowledgeBaseOverride));
  const [booleans, setBooleans] = useState<Record<string, BoolValue>>(() => initialBooleans(configuration.retrieval.knowledgeBaseOverride));
  const [savingStrategy, setSavingStrategy] = useState(false);
  const [savingRetrieval, setSavingRetrieval] = useState(false);
  const [formError, setFormError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    setSwitches(normalizedStrategy(configuration.indexingStrategy));
    setNumbers(initialNumbers(configuration.retrieval.knowledgeBaseOverride));
    setBooleans(initialBooleans(configuration.retrieval.knowledgeBaseOverride));
    setFieldErrors({});
    setFormError("");
  }, [configuration]);

  const initialStrategy = useMemo(() => normalizedStrategy(configuration.indexingStrategy), [configuration]);
  const initialOverride = useMemo(() => configuration.retrieval.knowledgeBaseOverride, [configuration]);
  const strategyDirty = strategyOrder(switches) !== strategyOrder(initialStrategy);
  const retrievalDirty = retrievalOrder(buildOverride(numbers, booleans)) !== retrievalOrder(initialOverride);
  const format = (value: number) => (Number.isInteger(value) ? String(value) : String(Number(value.toFixed(4))));

  const validateNumbers = (): Record<string, string> => {
    const errors: Record<string, string> = {};
    for (const field of numericFields) {
      const value = numbers[field.key];
      const error = fieldError(field, value);
      if (!error) continue;
      errors[field.key] = error === "range"
        ? t("retrieval.validationRange", { min: field.min ?? 0, max: field.max ?? "∞" })
        : error === "positive"
          ? t("retrieval.validationPositive")
          : t("retrieval.validationNonNegative");
    }
    return errors;
  };

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
      message.success(t("retrieval.strategySaved"));
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

  async function putOverride(override: RetrievalOverride, successText: string) {
    setSavingRetrieval(true);
    setFormError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}/retrieval-config`, jsonInit(override, "PUT"));
      message.success(successText);
      await queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBaseId) });
      onSaved();
    } catch (cause) {
      const text = cause instanceof Error ? cause.message : t("settings.saveFailed");
      const mapped: Record<string, string> = {};
      for (const field of numericFields) {
        if (text.includes(field.key)) mapped[field.key] = text;
      }
      setFieldErrors(mapped);
      setFormError(Object.keys(mapped).length > 0 ? "" : text);
    } finally {
      setSavingRetrieval(false);
    }
  }

  async function saveRetrieval() {
    if (!canWrite || !retrievalDirty || savingRetrieval) return;
    const errors = validateNumbers();
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;
    await putOverride(buildOverride(numbers, booleans), t("retrieval.retrievalSaved"));
  }

  function clearAllOverrides() {
    modal.confirm({
      title: t("settings.resetRetrieval"),
      content: t("settings.resetConfirm"),
      okText: t("common.confirm"),
      cancelText: t("common.cancel"),
      onOk: () => putOverride({}, t("retrieval.retrievalSaved")),
    });
  }

  const hasOverride = Object.keys(initialOverride).length > 0;

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

      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("settings.retrieval")} · {t("common.effective")}</Typography.Title>
            <Typography.Text type="secondary">{t("retrieval.overrideHint")}</Typography.Text>
          </div>
        </header>
        {groups.map((group) => {
          const fields = numericFields.filter((field) => field.group === group.key);
          const bools = booleanFields.filter((field) => field.group === group.key);
          if (fields.length === 0 && bools.length === 0) return null;
          return (
            <div className="setting-group" key={group.key}>
              <Typography.Title level={5}>{t(group.labelKey)}</Typography.Title>
              {fields.map((field) => (
                <NumericRow
                  key={field.key}
                  field={field}
                  value={numbers[field.key] ?? null}
                  effective={configuration.retrieval.effective[field.key] as number}
                  globalDefault={configuration.retrieval.globalDefaults[field.key] as number}
                  source={configuration.retrieval.sources[field.key]}
                  canWrite={canWrite}
                  error={fieldErrors[field.key]}
                  format={format}
                  onChange={(value) => setNumbers((current) => ({ ...current, [field.key]: value }))}
                  onClear={() => setNumbers((current) => ({ ...current, [field.key]: null }))}
                />
              ))}
              {bools.map((field) => {
                const effective = configuration.retrieval.effective[field.key] as boolean;
                const globalDefault = configuration.retrieval.globalDefaults[field.key] as boolean;
                return (
                  <div className="setting-row" key={field.key}>
                    <div className="setting-label">
                      <Space size={8} wrap>
                        <Typography.Text strong>{t(field.labelKey)}</Typography.Text>
                        <Tag color={booleans[field.key] === "" ? "default" : "green"}>{booleans[field.key] === "" ? t("common.inherit") : t("common.override")}</Tag>
                      </Space>
                      <div className="setting-help">{t("common.effective")}: {String(effective)} · {t("common.inherit")}: {String(globalDefault)}</div>
                    </div>
                    <div className="setting-control">
                      <Select<BoolValue>
                        value={booleans[field.key]}
                        onChange={(value) => setBooleans((current) => ({ ...current, [field.key]: value }))}
                        disabled={!canWrite}
                        aria-label={t(field.labelKey)}
                        options={[
                          { value: "", label: `${t("common.inherit")} (${String(globalDefault)})` },
                          { value: "true", label: "true" },
                          { value: "false", label: "false" },
                        ]}
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          );
        })}
        {formError && <Alert type="error" showIcon message={formError} className="settings-alert" />}
        <Flex justify="space-between" gap={8} className="settings-actions" wrap="wrap">
          <div>
            {canWrite && hasOverride && (
              <Button onClick={clearAllOverrides} loading={savingRetrieval}>{t("settings.resetRetrieval")}</Button>
            )}
          </div>
          <Button type="primary" onClick={() => void saveRetrieval()} loading={savingRetrieval} disabled={!canWrite || !retrievalDirty}>
            {t("retrieval.retrievalSave")}
          </Button>
        </Flex>
      </section>
    </div>
  );
}

function NumericRow(props: {
  field: NumericField;
  value: number | null;
  effective: number;
  globalDefault: number;
  source: "global" | "knowledge_base";
  canWrite: boolean;
  error?: string;
  format: (value: number) => string;
  onChange: (value: number | null) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  const { field, value, effective, globalDefault, source, canWrite, error } = props;
  return (
    <div className={`setting-row${error ? " setting-row-error" : ""}`}>
      <div className="setting-label">
        <Space size={8} wrap>
          <Typography.Text strong>{t(field.labelKey)}</Typography.Text>
          <Tag color={source === "knowledge_base" ? "green" : "default"}>{source === "knowledge_base" ? t("common.override") : t("common.inherit")}</Tag>
        </Space>
        <div className="setting-help">
          {t("common.effective")}: {props.format(effective)}{field.unitKey ? ` ${t(field.unitKey)}` : ""} · {t("common.inherit")}: {props.format(globalDefault)}
          {field.min !== undefined ? ` · ${field.min}${field.max !== undefined ? `–${field.max}` : "+"}` : ""}
        </div>
        <div className="setting-help">{source === "knowledge_base" ? t("retrieval.overrideHint") : t("retrieval.inheritHint")}</div>
      </div>
      <div className="setting-control">
        <Space size={4} wrap>
          <InputNumber
            value={value}
            min={field.min}
            max={field.max}
            step={field.step ?? 1}
            precision={field.integer ? 0 : undefined}
            placeholder={props.format(globalDefault)}
            disabled={!canWrite}
            status={error ? "error" : undefined}
            aria-label={t(field.labelKey)}
            onChange={(next) => props.onChange(typeof next === "number" ? next : null)}
            onBlur={() => {
              if (value !== null && !field.integer) props.onChange(Number(value));
            }}
          />
          {field.unitKey && <Typography.Text type="secondary">{t(field.unitKey)}</Typography.Text>}
          {canWrite && source === "knowledge_base" && (
            <Button type="link" size="small" onClick={props.onClear}>{t("retrieval.clearOverride")}</Button>
          )}
        </Space>
        {error && <div className="field-error" role="alert">{error}</div>}
      </div>
    </div>
  );
}
