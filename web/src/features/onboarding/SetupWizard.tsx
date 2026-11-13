import { Alert, Button, Form, Input, List, Modal, Space, Steps, Tag, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { api, jsonInit } from "../../api/client";
import { queryKeys, useModelCatalog } from "../../api/hooks";
import type { KnowledgeBase } from "../../api/types";
import { usePermissions } from "../../session";
import { capabilityLabel } from "../../utils/enums";

type Props = {
  open: boolean;
  onClose: () => void;
};

// SetupWizard is the first-run path: confirm a model endpoint, create the first
// knowledge base, then hand the user a sample question. It uses the existing
// model catalog and knowledge-base-create endpoints, so no deployment edit is
// required.
export function SetupWizard({ open, onClose }: Props) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const catalog = useModelCatalog(open);
  const { canAdmin } = usePermissions();
  const [step, setStep] = useState(0);
  const [form] = Form.useForm<{ name: string; description?: string }>();
  const [created, setCreated] = useState<KnowledgeBase | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");

  const items = catalog.data ?? [];
  const chatModels = items.filter((item) => item.capability === "chat");
  const embeddingModels = items.filter((item) => item.capability === "embedding");
  const modelReady = chatModels.length > 0 && embeddingModels.length > 0;

  async function createKnowledgeBase() {
    try {
      const values = await form.validateFields();
      setCreating(true);
      setError("");
      const knowledgeBase = await api<KnowledgeBase>("/knowledge-bases", jsonInit({
        name: values.name.trim(),
        description: values.description ?? "",
      }));
      setCreated(knowledgeBase);
      await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
      setStep(2);
    } catch (cause) {
      if (cause instanceof Error) setError(cause.message);
    } finally {
      setCreating(false);
    }
  }

  function finishChat() {
    if (created) navigate(`/chat?kb=${encodeURIComponent(created.id)}`);
    close();
  }

  function finishDocuments() {
    if (created) navigate(`/knowledge-bases/${created.id}`);
    close();
  }

  function close() {
    setStep(0);
    setCreated(null);
    setError("");
    form.resetFields();
    onClose();
  }

  return (
    <Modal open={open} onCancel={close} footer={null} width={760} title={t("setup.title")} destroyOnHidden>
      <Space direction="vertical" size={20} style={{ width: "100%" }}>
        <Steps
          current={step}
          items={[
            { title: t("setup.stepModel") },
            { title: t("setup.stepKnowledgeBase") },
            { title: t("setup.stepQuestion") },
          ]}
        />
        {step === 0 && (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Typography.Text type="secondary">{t("setup.modelHint")}</Typography.Text>
            {catalog.error && <Alert type="error" showIcon message={t("setup.modelLoadFailed")} description={catalog.error.message} />}
            {!catalog.error && (
              <Alert
                type={modelReady ? "success" : "warning"}
                showIcon
                message={modelReady ? t("setup.modelReady") : t("setup.modelMissing")}
                description={
                  <Space direction="vertical" size={4}>
                    <span>{capabilityLabel(t, "chat")}: {chatModels.map((item) => item.model).join(", ") || "—"}</span>
                    <span>{capabilityLabel(t, "embedding")}: {embeddingModels.map((item) => item.model).join(", ") || "—"}</span>
                  </Space>
                }
              />
            )}
            <Space>
              <Button type="primary" onClick={() => setStep(1)} disabled={!modelReady}>{t("common.next")}</Button>
              {canAdmin && <Button onClick={() => navigate("/admin")}>{t("setup.configureModels")}</Button>}
            </Space>
          </Space>
        )}
        {step === 1 && (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Typography.Text type="secondary">{t("setup.knowledgeBaseHint")}</Typography.Text>
            <Form form={form} layout="vertical">
              <Form.Item name="name" label={t("knowledgeBase.name")} rules={[{ required: true, max: 255 }]}>
                <Input maxLength={255} placeholder={t("setup.namePlaceholder")} />
              </Form.Item>
              <Form.Item name="description" label={t("knowledgeBase.descriptionField")}>
                <Input.TextArea rows={3} />
              </Form.Item>
            </Form>
            {error && <Alert type="error" showIcon message={error} />}
            <Space>
              <Button onClick={() => setStep(0)}>{t("common.back")}</Button>
              <Button type="primary" loading={creating} onClick={() => void createKnowledgeBase()}>{t("setup.createAndContinue")}</Button>
            </Space>
          </Space>
        )}
        {step === 2 && (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Alert type="success" showIcon message={t("setup.created", { name: created?.name ?? "" })} description={t("setup.questionHint")} />
            <List
              size="small"
              header={<Typography.Text strong>{t("setup.sampleQuestions")}</Typography.Text>}
              dataSource={[t("setup.sample1"), t("setup.sample2"), t("setup.sample3")]}
              renderItem={(item) => (
                <List.Item>
                  <Space>
                    <Tag>{t("setup.sampleTag")}</Tag>
                    <Typography.Text>{item}</Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
            <Space>
              <Button onClick={finishDocuments}>{t("setup.uploadDocuments")}</Button>
              <Button type="primary" onClick={finishChat}>{t("setup.startChat")}</Button>
            </Space>
          </Space>
        )}
      </Space>
    </Modal>
  );
}
