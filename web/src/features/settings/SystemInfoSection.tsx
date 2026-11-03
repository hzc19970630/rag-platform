import { Descriptions, Tag, Typography } from "antd";
import { useTranslation } from "react-i18next";
import type { KnowledgeBaseConfigurationV1 } from "../../api/types";

export function SystemInfoSection({ configuration }: { configuration: KnowledgeBaseConfigurationV1 }) {
  const { t } = useTranslation();
  const readOnly = configuration.readOnly;
  return (
    <div className="settings-section">
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("system.title")}</Typography.Title>
            <Typography.Text type="secondary">{t("system.hint")}</Typography.Text>
          </div>
          <Tag color="blue">{t("system.managed")}</Tag>
        </header>
        <Descriptions bordered column={1} size="small" className="system-descriptions">
          <Descriptions.Item label={t("system.embedding")}>
            {readOnly.embedding.source} · {readOnly.embedding.model || "-"} · {readOnly.embedding.dimension}
          </Descriptions.Item>
          <Descriptions.Item label={t("system.chunking")}>
            {readOnly.chunking.size} / {readOnly.chunking.overlap} / {readOnly.chunking.splitMarkers || "-"}
            <Typography.Text type="secondary"> · min {readOnly.chunking.minSize}</Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label={t("system.chat")}>
            {readOnly.chat.source} · {readOnly.chat.model || "-"}
          </Descriptions.Item>
          <Descriptions.Item label={t("system.rerank")}>
            {readOnly.rerank.source} · {readOnly.rerank.model || "-"}
          </Descriptions.Item>
          <Descriptions.Item label={t("system.parser")}>
            {readOnly.parser.version} · {readOnly.parser.supportedFormats.join(", ")}
          </Descriptions.Item>
        </Descriptions>
      </section>
    </div>
  );
}
