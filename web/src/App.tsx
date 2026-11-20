import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp, Button, ConfigProvider, Result, Spin } from "antd";
import enUS from "antd/locale/en_US";
import zhCN from "antd/locale/zh_CN";
import { lazy, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { ApiError } from "./api/client";
import { useMe } from "./api/hooks";
import { AppShell } from "./components/AppShell";
import i18n from "./i18n";
import { LoginPage } from "./pages/LoginPage";
import { SessionProvider } from "./session";
import { usePreferences } from "./store/preferences";
import { useResolvedTheme } from "./theme";

const AdminPage = lazy(() => import("./pages/AdminPage").then((module) => ({ default: module.AdminPage })));
const ChatPage = lazy(() => import("./pages/ChatPage").then((module) => ({ default: module.ChatPage })));
const DocumentPage = lazy(() => import("./pages/DocumentPage").then((module) => ({ default: module.DocumentPage })));
const KnowledgeBasePage = lazy(() => import("./pages/KnowledgeBasePage").then((module) => ({ default: module.KnowledgeBasePage })));
const KnowledgeBasesPage = lazy(() => import("./pages/KnowledgeBasesPage").then((module) => ({ default: module.KnowledgeBasesPage })));
const McpPage = lazy(() => import("./pages/McpPage").then((module) => ({ default: module.McpPage })));
const OpsPage = lazy(() => import("./pages/OpsPage").then((module) => ({ default: module.OpsPage })));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => !(error instanceof ApiError) || (error.status >= 500 && failureCount < 2),
    },
  },
});

function ProtectedShell() {
  const me = useMe();
  const location = useLocation();
  const { t } = useTranslation();

  if (me.isLoading) {
    return <div className="route-loading"><Spin size="large" /></div>;
  }
  if (me.error) {
    if (me.error instanceof ApiError && (me.error.status === 401 || me.error.status === 403)) {
      return <Navigate to="/login" replace state={{ from: `${location.pathname}${location.search}` }} />;
    }
    return (
      <div className="route-loading">
        <Result
          status="error"
          title={t("common.networkError")}
          subTitle={me.error instanceof Error ? me.error.message : undefined}
          extra={<Button onClick={() => void me.refetch()}>{t("common.retry")}</Button>}
        />
      </div>
    );
  }
  if (!me.data) return <Navigate to="/login" replace />;
  return (
    <SessionProvider value={me.data}>
      <AppShell />
    </SessionProvider>
  );
}

function NotFound() {
  const { t } = useTranslation();
  return (
    <Result
      status="404"
      title="404"
      subTitle={t("common.notFound")}
      extra={<Button type="primary" onClick={() => window.location.assign("/knowledge-bases")}>{t("common.back")}</Button>}
    />
  );
}

export function App() {
  const { config } = useResolvedTheme();
  const language = usePreferences((state) => state.language);

  useEffect(() => {
    void i18n.changeLanguage(language);
    document.documentElement.lang = language;
  }, [language]);

  return (
    <QueryClientProvider client={queryClient}>
      <ConfigProvider theme={config} locale={language === "zh-CN" ? zhCN : enUS}>
        <AntdApp>
          <BrowserRouter>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route element={<ProtectedShell />}>
                <Route path="/knowledge-bases" element={<KnowledgeBasesPage />} />
                <Route path="/knowledge-bases/:knowledgeBaseId" element={<KnowledgeBasePage />} />
                <Route path="/documents/:documentId" element={<DocumentPage />} />
                <Route path="/chat" element={<ChatPage />} />
                <Route path="/chat/:conversationId" element={<ChatPage />} />
                <Route path="/admin" element={<AdminPage />} />
                <Route path="/ops" element={<OpsPage />} />
                <Route path="/mcp" element={<McpPage />} />
              </Route>
              <Route path="/" element={<Navigate to="/knowledge-bases" replace />} />
              <Route path="*" element={<NotFound />} />
            </Routes>
          </BrowserRouter>
        </AntdApp>
      </ConfigProvider>
    </QueryClientProvider>
  );
}
