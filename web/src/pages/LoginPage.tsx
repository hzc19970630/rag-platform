import { ArrowRightOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Divider, Flex, Space, Spin, Typography } from "antd";
import { useTranslation } from "react-i18next";
import { Navigate } from "react-router-dom";
import { apiBase, ApiError } from "../api/client";
import { useAuthConfig, useMe } from "../api/hooks";

export function LoginPage() {
  const { t } = useTranslation();
  const authConfig = useAuthConfig();
  const me = useMe();

  if (me.isLoading) {
    return <div className="login-shell"><Spin size="large" /></div>;
  }
  if (me.data) return <Navigate to="/knowledge-bases" replace />;
  const meErrorStatus = me.error instanceof ApiError ? me.error.status : 0;
  const showMeError = Boolean(me.error) && meErrorStatus !== 401;

  return (
    <main className="login-shell">
      <div className="login-brand">
        <div className="brand-mark large" aria-hidden="true">R</div>
        <Typography.Title>{t("brand")}</Typography.Title>
        <Typography.Paragraph className="login-tagline">{t("login.tagline")}</Typography.Paragraph>
        <Space direction="vertical" size={12} className="login-points">
          <span><SafetyCertificateOutlined /> {t("login.pointGoverned")}</span>
          <span><SafetyCertificateOutlined /> {t("login.pointEvidence")}</span>
          <span><SafetyCertificateOutlined /> {t("login.pointAudit")}</span>
        </Space>
      </div>
      <Card className="login-panel" variant="borderless">
        <Typography.Title level={2}>{t("auth.title")}</Typography.Title>
        <Typography.Paragraph type="secondary">{t("auth.description")}</Typography.Paragraph>
        {showMeError && (
          <Alert type="error" showIcon message={me.error instanceof Error ? me.error.message : t("common.networkError")} className="settings-alert" />
        )}
        {authConfig.isLoading && <Flex justify="center" className="login-loading"><Spin /></Flex>}
        {authConfig.data?.oidcEnabled && (
          <Button type="primary" size="large" block href={`${apiBase}/auth/oidc/login`} icon={<ArrowRightOutlined />}>
            {t("auth.oidc")}
          </Button>
        )}
        {authConfig.data && !authConfig.data.oidcEnabled && <Alert type="warning" showIcon message={t("auth.oidcDisabled")} />}
        {authConfig.data?.devAuthEnabled && (
          <>
            <Divider />
            <Typography.Paragraph type="secondary">{t("auth.devHint")}</Typography.Paragraph>
            <Button data-testid="login-dev-continue" block href="/knowledge-bases" onClick={(event) => { event.preventDefault(); window.location.assign("/knowledge-bases"); }}>
              {t("login.continueDev")}
            </Button>
          </>
        )}
        {authConfig.error && <Alert type="error" showIcon message={authConfig.error instanceof Error ? authConfig.error.message : t("common.networkError")} className="settings-alert" />}
      </Card>
    </main>
  );
}
