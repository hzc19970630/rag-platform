import { Alert, App, Button, Flex, Progress, Select, Space, Tag, Typography } from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, api, jsonInit } from "../../api/client";
import type { KnowledgeBaseConfigurationV1, ModelCatalogEntry } from "../../api/types";
import {
  embeddingMigrationStatusLabel,
  embeddingMigrationStatusTone,
  toneColor,
} from "../../utils/enums";

type Migration = {
  id: string;
  status: string;
  sourceModel: string;
  sourceDimension: number;
  targetModel: string;
  targetDimension: number;
  totalChunks: number;
  doneChunks: number;
  errorMessage?: string;
};

type Props = {
  knowledgeBaseId: string;
  configuration: KnowledgeBaseConfigurationV1;
  catalog: ModelCatalogEntry[];
  canWrite: boolean;
  onSaved: () => void;
};

const activeStatuses = ["pending", "building_index", "reembedding", "verifying", "ready"];

export function EmbeddingMigration({ knowledgeBaseId, configuration, catalog, canWrite, onSaved }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const binding = configuration.modelBinding;
  const currentModel = binding.embedding.model || configuration.readOnly.embedding.model;
  const currentDimension = binding.embeddingDimension || configuration.readOnly.embedding.dimension;
  const [migration, setMigration] = useState<Migration | null>(null);
  const [target, setTarget] = useState("");
  const [directTarget, setDirectTarget] = useState(currentModel);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const embeddingModels = useMemo(() => {
    const seen = new Map<string, ModelCatalogEntry>();
    for (const entry of catalog) {
      if (entry.capability === "embedding" && entry.enabled && !seen.has(entry.model)) {
        seen.set(entry.model, entry);
      }
    }
    return [...seen.values()];
  }, [catalog]);

  const targetOptions = useMemo(
    () => embeddingModels
      .filter((entry) => entry.model !== currentModel)
      .map((entry) => ({
        value: entry.model,
        label: `${entry.model} · ${entry.dimension} · ${entry.source === "deployment" ? t("models.sourceDeployment") : entry.provider}`,
      })),
    [embeddingModels, currentModel, t],
  );

  const load = useCallback(async () => {
    try {
      const current = await api<Migration | null>(`/knowledge-bases/${knowledgeBaseId}/embedding-migration`);
      setMigration(current);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        setMigration(null);
        return;
      }
      setError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    }
  }, [knowledgeBaseId, t]);

  useEffect(() => {
    setError("");
    void load();
  }, [load]);

  useEffect(() => {
    if (!migration || !activeStatuses.includes(migration.status)) return;
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [migration, load]);

  async function run(action: () => Promise<Migration>) {
    setBusy(true);
    setError("");
    try {
      const next = await action();
      setMigration(next);
      if (!activeStatuses.includes(next.status)) onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function saveDirectBinding() {
    setBusy(true);
    setError("");
    try {
      await api(`/knowledge-bases/${knowledgeBaseId}`, jsonInit({ embedding_model: directTarget }, "PATCH"));
      message.success(t("settings.saveSuccess"));
      onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    } finally {
      setBusy(false);
    }
  }

  const percent = migration && migration.totalChunks > 0
    ? Math.round((migration.doneChunks / migration.totalChunks) * 100)
    : 0;

  return (
    <div data-testid="embedding-migration">
      <Typography.Text strong>{t("models.embedding")}</Typography.Text>
      <div>
        <Typography.Text type="secondary">{t("models.migrationCurrent")}</Typography.Text>{" "}
        <Tag>{`${currentModel} · ${currentDimension}`}</Tag>
        {binding.embeddingLocked ? <Tag color="orange">{t("models.embeddingLocked")}</Tag> : null}
      </div>
      <Typography.Text type="secondary">{t("models.embeddingHint")}</Typography.Text>

      {!binding.embeddingLocked ? (
        <Flex vertical gap={8} style={{ marginTop: 12 }}>
          <Select
            data-testid="embedding-direct-target"
            className="settings-select"
            value={directTarget}
            options={embeddingModels.map((entry) => ({ value: entry.model, label: `${entry.model} · ${entry.dimension}` }))}
            onChange={setDirectTarget}
            disabled={!canWrite || busy}
          />
          <Button
            data-testid="embedding-direct-save"
            loading={busy}
            disabled={!canWrite || busy || directTarget === currentModel}
            onClick={() => void saveDirectBinding()}
          >
            {t("models.embeddingSave")}
          </Button>
        </Flex>
      ) : null}

      {binding.embeddingLocked ? (
        <div style={{ marginTop: 16 }}>
          <Flex vertical gap={10}>
            <Typography.Text strong>{t("models.migrationTitle")}</Typography.Text>
            <Typography.Text type="secondary">{t("models.migrationHint")}</Typography.Text>
            {migration ? (
              <>
                <Space wrap>
                  <Tag color={toneColor(embeddingMigrationStatusTone(migration.status))}>
                    {embeddingMigrationStatusLabel(t, migration.status)}
                  </Tag>
                  <Typography.Text type="secondary">
                    {`${migration.sourceModel}/${migration.sourceDimension} → ${migration.targetModel}/${migration.targetDimension}`}
                  </Typography.Text>
                </Space>
                {migration.totalChunks > 0 ? (
                  <Progress
                    percent={percent}
                    size="small"
                    status={migration.status === "failed" ? "exception" : undefined}
                  />
                ) : null}
                <Typography.Text type="secondary">
                  {t("models.migrationProgress", { done: migration.doneChunks, total: migration.totalChunks })}
                </Typography.Text>
                {migration.errorMessage ? (
                  <Alert type="error" showIcon message={t("models.migrationFailed", { message: migration.errorMessage })} />
                ) : null}
                <Space wrap>
                  {activeStatuses.includes(migration.status) ? (
                    <Button danger disabled={!canWrite || busy} onClick={() => modal.confirm({
                      title: t("models.migrationCancel"),
                      content: t("models.migrationConfirmCancel"),
                      onOk: () => run(() => api(`/knowledge-bases/${knowledgeBaseId}/embedding-migration/${migration.id}/cancel`, jsonInit({}))),
                    })}>
                      {t("models.migrationCancel")}
                    </Button>
                  ) : null}
                  {migration.status === "ready" ? (
                    <Button type="primary" disabled={!canWrite || busy} onClick={() => modal.confirm({
                      title: t("models.migrationCutover"),
                      content: t("models.migrationConfirmCutover"),
                      onOk: () => run(() => api(`/knowledge-bases/${knowledgeBaseId}/embedding-migration/${migration.id}/cutover`, jsonInit({}))),
                    })}>
                      {t("models.migrationCutover")}
                    </Button>
                  ) : null}
                  {migration.status === "cutover" ? (
                    <>
                      <Button disabled={!canWrite || busy} onClick={() => modal.confirm({
                        title: t("models.migrationRollback"),
                        content: t("models.migrationConfirmRollback"),
                        onOk: () => run(() => api(`/knowledge-bases/${knowledgeBaseId}/embedding-migration/${migration.id}/rollback`, jsonInit({}))),
                      })}>
                        {t("models.migrationRollback")}
                      </Button>
                      <Button danger disabled={!canWrite || busy} onClick={() => modal.confirm({
                        title: t("models.migrationFinalize"),
                        content: t("models.migrationConfirmFinalize"),
                        onOk: () => run(() => api(`/knowledge-bases/${knowledgeBaseId}/embedding-migration/${migration.id}/finalize`, jsonInit({}))),
                      })}>
                        {t("models.migrationFinalize")}
                      </Button>
                    </>
                  ) : null}
                </Space>
              </>
            ) : (
              <>
                <Select
                  data-testid="embedding-migration-target"
                  className="settings-select"
                  placeholder={t("models.migrationTarget")}
                  value={target || undefined}
                  options={targetOptions}
                  onChange={setTarget}
                  disabled={!canWrite || busy}
                  notFoundContent={t("models.migrationNoTargets")}
                />
                {!target ? <Typography.Text type="secondary">{t("models.migrationNone")}</Typography.Text> : null}
                <Button
                  data-testid="embedding-migration-start"
                  type="primary"
                  loading={busy}
                  disabled={!canWrite || busy || !target}
                  onClick={() => void run(() => api(
                    `/knowledge-bases/${knowledgeBaseId}/embedding-migration`,
                    jsonInit({ targetModel: target }),
                  ))}
                >
                  {t("models.migrationStart")}
                </Button>
              </>
            )}
          </Flex>
        </div>
      ) : null}

      {error ? <Alert type="error" showIcon message={error} style={{ marginTop: 12 }} /> : null}
    </div>
  );
}
