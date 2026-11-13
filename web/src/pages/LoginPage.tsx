import { ArrowRightOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Divider, Flex, Input, Space, Spin, Typography } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate } from "react-router-dom";
import { api, apiBase, ApiError, jsonInit } from "../api/client";
import { useAuthConfig, useMe } from "../api/hooks";

export function LoginPage() {
  const { t } = useTranslation();
  const authConfig = useAuthConfig();
  const me = useMe();
  const [ldapUsername, setLdapUsername] = useState("");
  const [ldapPassword, setLdapPassword] = useState("");
  const [ldapBusy, setLdapBusy] = useState(false);
  const [ldapError, setLdapError] = useState("");

  async function ldapLogin(event: React.FormEvent) {
    event.preventDefault();
    if (!ldapUsername.trim() || !ldapPassword) return;
    setLdapBusy(true);
    setLdapError("");
    try {
      await api("/auth/ldap/login", jsonInit({ username: ldapUsername.trim(), password: ldapPassword }));
      window.location.assign("/knowledge-bases");
    } catch (cause) {
      setLdapError(cause instanceof Error ? cause.message : t("auth.ldapFailed"));
    } finally {
      setLdapBusy(false);
    }
  }

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
        {authConfig.data && !authConfig.data.oidcEnabled && !authConfig.data.ldapEnabled && (
          <Alert type="warning" showIcon message={t("auth.oidcDisabled")} />
        )}
        {authConfig.data?.ldapEnabled && (
          <>
            <Divider />
            <Typography.Paragraph type="secondary">{t("auth.ldapHint")}</Typography.Paragraph>
            <form onSubmit={(event) => void ldapLogin(event)}>
              <Space direction="vertical" size={12} style={{ width: "100%" }}>
                <Input
                  value={ldapUsername}
                  autoComplete="username"
                  placeholder={t("auth.username")}
                  onChange={(event) => setLdapUsername(event.target.value)}
                />
                <Input.Password
                  value={ldapPassword}
                  autoComplete="current-password"
                  placeholder={t("auth.password")}
                  onChange={(event) => setLdapPassword(event.target.value)}
                />
                {ldapError && <Alert type="error" showIcon message={ldapError} />}
                <Button
                  type="primary"
                  htmlType="submit"
                  block
                  loading={ldapBusy}
                  disabled={!ldapUsername.trim() || !ldapPassword}
                >
                  {t("auth.ldapLogin")}
                </Button>
              </Space>
            </form>
          </>
        )}
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
