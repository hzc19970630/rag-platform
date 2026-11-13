import { Alert, Descriptions, List, Space, Tag, Typography, App } from "antd";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { ConfigChangePreview } from "../../api/types";
import { impactLabelKey, previewDecision, previewValue } from "./configPreview";

type ModalApi = ReturnType<typeof App.useApp>["modal"];

export function ConfigPreviewContent({ preview }: { preview: ConfigChangePreview }) {
  const { t } = useTranslation();
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <Descriptions size="small" column={3}>
        <Descriptions.Item label={t("configuration.previewKnowledgeBases")}>{preview.affected.knowledgeBases}</Descriptions.Item>
        <Descriptions.Item label={t("configuration.previewDocuments")}>{preview.affected.documents}</Descriptions.Item>
        <Descriptions.Item label={t("configuration.previewChunks")}>{preview.affected.chunks}</Descriptions.Item>
      </Descriptions>
      <div>
        <Typography.Text strong>{t("configuration.previewImpact")}: </Typography.Text>
        <Tag color={preview.impact === "rebuild" || preview.impact === "restart" ? "red" : preview.impact === "next_ingest" ? "orange" : "green"}>
          {t(impactLabelKey(preview.impact))}
        </Tag>
        <Typography.Text type="secondary">
          {t("configuration.previewEstimate", { duration: preview.estimatedDurationTier, cost: preview.estimatedCostTier })}
        </Typography.Text>
      </div>
      {preview.blockers.map((blocker) => (
        <Alert key={blocker.code} type="error" showIcon message={blocker.message} description={blocker.remediation} />
      ))}
      {preview.rebuildOptional && (
        <Alert type="warning" showIcon message={t("configuration.previewOnlyNew")} description={t("configuration.previewOptionalRebuild")} />
      )}
      <List
        size="small"
        header={<Typography.Text strong>{t("configuration.previewChanges")}</Typography.Text>}
        dataSource={preview.changes.slice(0, 12)}
        locale={{ emptyText: t("configuration.previewNoChanges") }}
        renderItem={(change) => (
          <List.Item>
            <Space size={8} wrap>
              <Tag>{change.domain}</Tag>
              <Typography.Text code>{change.key}</Typography.Text>
              <Typography.Text delete type="secondary">{previewValue(change.oldValue)}</Typography.Text>
              <Typography.Text>→</Typography.Text>
              <Typography.Text strong>{previewValue(change.newValue)}</Typography.Text>
              <Tag color="blue">{t(impactLabelKey(change.impact))}</Tag>
            </Space>
          </List.Item>
        )}
      />
    </Space>
  );
}

// confirmConfigPreview applies the product rule: hot changes save directly;
// next-ingest changes need explicit confirmation; data-contract changes are
// blocked with the remediation from the server.
export function confirmConfigPreview(preview: ConfigChangePreview, modal: ModalApi, t: TFunction): Promise<boolean> {
  const decision = previewDecision(preview);
  if (decision === "apply") return Promise.resolve(true);
  if (decision === "blocked") {
    modal.error({
      title: t("configuration.previewBlockedTitle"),
      width: 760,
      content: <ConfigPreviewContent preview={preview} />,
      okText: t("common.close"),
    });
    return Promise.resolve(false);
  }
  return new Promise((resolve) => {
    modal.confirm({
      title: t("configuration.previewConfirmTitle"),
      width: 760,
      content: <ConfigPreviewContent preview={preview} />,
      okText: t("configuration.previewOnlyNewConfirm"),
      cancelText: t("common.cancel"),
      onOk: () => resolve(true),
      onCancel: () => resolve(false),
    });
  });
}
