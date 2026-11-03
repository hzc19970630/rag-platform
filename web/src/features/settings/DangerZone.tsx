import { Alert, App, Button, Flex, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { api } from "../../api/client";
import { queryKeys } from "../../api/hooks";
import type { KnowledgeBase } from "../../api/types";

type Props = {
  knowledgeBase: KnowledgeBase;
  canDelete: boolean;
};

export function DangerZone({ knowledgeBase, canDelete }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState(false);

  async function remove() {
    if (!canDelete || deleting) return;
    const confirmed = await new Promise<boolean>((resolve) => {
      modal.confirm({
        title: t("settings.deleteTitle", { name: knowledgeBase.name }),
        content: (
          <div>
            <p>{t("settings.deleteConfirm")}</p>
            <p><Typography.Text code>{knowledgeBase.id}</Typography.Text></p>
          </div>
        ),
        okText: t("common.delete"),
        okButtonProps: { danger: true },
        cancelText: t("common.cancel"),
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
    if (!confirmed) return;
    setDeleting(true);
    try {
      await api(`/knowledge-bases/${knowledgeBase.id}`, { method: "DELETE" });
      await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
      message.success(t("settings.deleted"));
      navigate("/knowledge-bases", { replace: true });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("settings.deleteFailed"));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="settings-section">
      <section className="settings-block danger-block">
        <header className="settings-block-heading">
          <div>
            <Typography.Title level={4}>{t("settings.danger")}</Typography.Title>
            <Typography.Text type="secondary">{t("settings.dangerHint")}</Typography.Text>
          </div>
        </header>
        <Alert
          type="error"
          showIcon
          message={t("settings.deleteTitle", { name: knowledgeBase.name })}
          description={t("knowledgeBase.deleteConfirm")}
        />
        {canDelete
          ? (
            <Flex justify="flex-end" className="settings-actions">
              <Button danger type="primary" loading={deleting} onClick={() => void remove()}>{t("settings.deleteKnowledgeBase")}</Button>
            </Flex>
          )
          : <Alert type="info" showIcon message={t("settings.deleteForbidden")} />}
      </section>
    </div>
  );
}
