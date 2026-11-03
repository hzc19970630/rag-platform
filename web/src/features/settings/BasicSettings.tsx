import { Alert, Button, Flex, Form, Input, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { queryKeys } from "../../api/hooks";
import type { KnowledgeBase } from "../../api/types";

type Props = {
  knowledgeBase: KnowledgeBase;
  canWrite: boolean;
  onSaved: () => void;
};

export function BasicSettings({ knowledgeBase, canWrite, onSaved }: Props) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [name, setName] = useState(knowledgeBase.name);
  const [description, setDescription] = useState(knowledgeBase.description);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    setName(knowledgeBase.name);
    setDescription(knowledgeBase.description);
    setError("");
  }, [knowledgeBase]);

  const dirty = name !== knowledgeBase.name || description !== knowledgeBase.description;

  async function save() {
    if (!canWrite || !dirty || saving) return;
    if (!name.trim()) {
      setError(t("settings.nameRequired"));
      return;
    }
    setSaving(true);
    setError("");
    try {
      await api(`/knowledge-bases/${knowledgeBase.id}`, jsonInit({ name: name.trim(), description }, "PATCH"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBase(knowledgeBase.id) }),
        queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases }),
        queryClient.invalidateQueries({ queryKey: queryKeys.configuration(knowledgeBase.id) }),
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
      {!canWrite && <Alert type="info" showIcon message={t("settings.readOnlyHint")} className="settings-alert" />}
      <section className="settings-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("settings.basic")}</Typography.Title>
            <Typography.Text type="secondary">{t("settings.basicHint")}</Typography.Text>
          </div>
        </header>
        <Form layout="vertical" className="settings-form">
          <Form.Item label={t("knowledgeBase.name")} required>
            <Input
              value={name}
              maxLength={255}
              disabled={!canWrite}
              onChange={(event) => setName(event.target.value)}
              status={error ? "error" : undefined}
              aria-label={t("knowledgeBase.name")}
            />
          </Form.Item>
          <Form.Item label={t("knowledgeBase.descriptionField")}>
            <Input.TextArea
              value={description}
              rows={4}
              disabled={!canWrite}
              onChange={(event) => setDescription(event.target.value)}
              aria-label={t("knowledgeBase.descriptionField")}
            />
          </Form.Item>
        </Form>
        <div className="read-only-row">
          <Typography.Text type="secondary">{t("common.id")}: <code>{knowledgeBase.id}</code></Typography.Text>
          <Typography.Text type="secondary">ACL {knowledgeBase.aclVersion}</Typography.Text>
        </div>
        {error && <Alert type="error" showIcon message={error} className="settings-alert" />}
        {canWrite && (
          <Flex justify="flex-end" gap={8} className="settings-actions">
            <Button type="primary" onClick={() => void save()} loading={saving} disabled={!dirty}>
              {t("common.save")}
            </Button>
          </Flex>
        )}
      </section>
    </div>
  );
}
