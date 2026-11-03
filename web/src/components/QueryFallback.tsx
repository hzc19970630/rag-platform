import { Button, Result, Spin } from "antd";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";

export function LoadingBlock() {
  const { t } = useTranslation();
  return (
    <div className="loading-block" role="status">
      <Spin />
      <span>{t("common.loading")}</span>
    </div>
  );
}

export function QueryFallback({ error, retry }: { error: unknown; retry?: () => void }) {
  const { t } = useTranslation();
  if (error instanceof ApiError && error.status === 403) {
    return <Result status="403" title="403" subTitle={t("common.noPermission")} />;
  }
  if (error instanceof ApiError && error.status === 404) {
    return <Result status="404" title="404" subTitle={error.message} />;
  }
  return (
    <Result
      status="error"
      title={t("common.networkError")}
      subTitle={error instanceof Error ? error.message : undefined}
      extra={retry ? <Button onClick={retry}>{t("common.retry")}</Button> : undefined}
    />
  );
}
