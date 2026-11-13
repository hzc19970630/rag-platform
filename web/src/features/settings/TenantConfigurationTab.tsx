import { Alert, App, Button, Card, Flex, Space, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { previewConfigChange } from "../../api/configPreview";
import { queryKeys, useConfigHealth, useTenantConfiguration } from "../../api/hooks";
import type { ConfigConnectionTestResult, ConfigHealthCheck, TenantConfiguration } from "../../api/types";
import { capabilityLabel, statusLabel, statusTone, toneColor } from "../../utils/enums";
import { confirmConfigPreview } from "./ConfigPreviewContent";
import { SchemaConfigForm } from "./SchemaConfigForm";

function checkLabelKey(id: string): string {
  switch (id) {
    case "model.chat":
      return "configuration.checkModelChat";
    case "model.embedding":
      return "configuration.checkModelEmbedding";
    case "model.rerank":
      return "configuration.checkModelRerank";
    case "model.tenant_providers":
      return "configuration.checkModelProviders";
    case "index.valid":
      return "configuration.checkIndex";
    case "embedding.dimension":
      return "configuration.checkDimension";
    case "rls.mode":
      return "configuration.checkRLS";
    case "backup.age":
      return "configuration.checkBackup";
    case "license.expiry":
      return "configuration.checkLicense";
    case "drift.retrieval_file":
      return "configuration.checkFileDrift";
    case "drift.stored_config":
      return "configuration.checkStoredDrift";
    default:
      return id;
  }
}

export function TenantConfigurationTab() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const configuration = useTenantConfiguration();
  const [saving, setSaving] = useState<"" | "retrieval" | "chunking">("");

  async function saveDomain(domain: "retrieval" | "chunking", override: Record<string, unknown>) {
    if (!configuration.data || saving) return;
    setSaving(domain);
    try {
      const body: { retrieval: Record<string, unknown>; chunking: Record<string, unknown> } = {
        retrieval: domain === "retrieval" ? override : configuration.data.retrieval.tenantOverride,
        chunking: domain === "chunking" ? override : configuration.data.chunking.tenantOverride,
      };
      const preview = await previewConfigChange({ scope: "tenant", retrieval: body.retrieval, chunking: body.chunking });
      if (!(await confirmConfigPreview(preview, modal, t))) return;
      await api<TenantConfiguration>("/tenant/config", jsonInit(body, "PUT"));
      message.success(t("configuration.saved"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.tenantConfig }),
        queryClient.invalidateQueries({ queryKey: queryKeys.configHealth }),
      ]);
    } finally {
      setSaving("");
    }
  }

  if (configuration.error) {
    return <Alert type="error" showIcon message={t("configuration.loadFailed")} description={configuration.error.message} />;
  }
  if (!configuration.data) {
    return <Typography.Text type="secondary">{t("common.loading")}</Typography.Text>;
  }

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message={t("configuration.title")}
        description={t("configuration.description")}
      />
      <ConfigHealthPanel />
      <SchemaConfigForm
        domain={configuration.data.retrieval}
        override={configuration.data.retrieval.tenantOverride}
        titleKey="configuration.retrievalTitle"
        hintKey="configuration.retrievalHint"
        canWrite
        saving={saving === "retrieval"}
        onSave={(override) => saveDomain("retrieval", override)}
        onClear={() => saveDomain("retrieval", {})}
      />
      <SchemaConfigForm
        domain={configuration.data.chunking}
        override={configuration.data.chunking.tenantOverride}
        titleKey="configuration.chunkingTitle"
        hintKey="configuration.chunkingHint"
        canWrite
        saving={saving === "chunking"}
        onSave={(override) => saveDomain("chunking", override)}
        onClear={() => saveDomain("chunking", {})}
      />
    </Space>
  );
}

function ConfigHealthPanel() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const health = useConfigHealth();
  const [testing, setTesting] = useState("");
  const [result, setResult] = useState<ConfigConnectionTestResult | null>(null);

  async function test(capability: "chat" | "embedding" | "rerank") {
    setTesting(capability);
    setResult(null);
    try {
      const value = await api<ConfigConnectionTestResult>("/tenant/config/test-connection", jsonInit({ capability }, "POST"));
      setResult(value);
      if (value.healthy) message.success(t("configuration.testHealthy"));
      else message.warning(value.error || t("configuration.testFailed"));
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("configuration.testFailed"));
    } finally {
      setTesting("");
    }
  }

  const status = health.data?.status ?? "unknown";
  const alertType = status === "error" ? "error" : status === "warning" ? "warning" : "success";

  return (
    <Card
      title={t("configuration.healthTitle")}
      extra={<Button onClick={() => void queryClient.invalidateQueries({ queryKey: queryKeys.configHealth })} loading={health.isFetching}>{t("configuration.refresh")}</Button>}
    >
      {health.error && <Alert type="error" showIcon message={t("configuration.healthFailed")} description={health.error.message} />}
      {health.data && (
        <Space direction="vertical" size={12} style={{ width: "100%" }}>
          <Alert
            type={alertType}
            showIcon
            message={status === "ok" ? t("configuration.healthOK") : t("configuration.healthAttention", { count: health.data.highRisk })}
          />
          {health.data.checks.map((check) => <HealthRow key={check.id} check={check} />)}
          <Flex gap={8} wrap="wrap">
            {(["chat", "embedding", "rerank"] as const).map((capability) => (
              <Button key={capability} onClick={() => void test(capability)} loading={testing === capability}>
                {t("configuration.testCapability", { capability: capabilityLabel(t, capability) })}
              </Button>
            ))}
          </Flex>
          {result && (
            <Alert
              type={result.healthy ? "success" : "error"}
              showIcon
              message={result.healthy ? t("configuration.testHealthy") : t("configuration.testUnhealthy")}
              description={[result.error, result.dimension ? `dimension=${result.dimension}` : ""].filter(Boolean).join(" · ")}
            />
          )}
        </Space>
      )}
    </Card>
  );
}

function HealthRow({ check }: { check: ConfigHealthCheck }) {
  const { t } = useTranslation();
  return (
    <div className="setting-row">
      <div className="setting-label">
        <Space size={8} wrap>
          <Tag color={toneColor(statusTone(check.status))}>{statusLabel(t, check.status)}</Tag>
          <Typography.Text strong>{t(checkLabelKey(check.id))}</Typography.Text>
          {check.severity === "high" && check.status !== "ok" && check.status !== "skipped" && <Tag color="red">{t("configuration.highRisk")}</Tag>}
        </Space>
        <div className="setting-help">{check.summary}{check.detail ? ` · ${check.detail}` : ""}</div>
        {check.remediation && <div className="setting-help">{t("configuration.remediation")}: {check.remediation}</div>}
      </div>
    </div>
  );
}
