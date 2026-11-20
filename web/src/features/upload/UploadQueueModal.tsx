import { Button, Modal, Progress, Space, Tag, Typography } from "antd";
import { useTranslation } from "react-i18next";
import type { UploadJob, UploadQueue } from "./useUploadQueue";

const statusColor: Record<UploadJob["status"], string> = {
  queued: "default",
  running: "processing",
  done: "success",
  failed: "error",
  canceled: "warning",
};

export function UploadQueueModal({ queue }: { queue: UploadQueue }) {
  const { t } = useTranslation();
  const { jobs, open, setOpen, summary } = queue;
  return (
    <Modal
      open={open}
      title={t("upload.title")}
      onCancel={() => setOpen(false)}
      footer={(
        <Space>
          <Button onClick={queue.clearFinished} disabled={summary.done + summary.failed === 0}>{t("upload.clearFinished")}</Button>
          <Button type="primary" onClick={() => setOpen(false)}>{t("upload.continueInBackground")}</Button>
        </Space>
      )}
    >
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <Typography.Text type="secondary">
          {t("upload.summary", { done: summary.done, total: summary.total, failed: summary.failed })}
        </Typography.Text>
        {jobs.map((job) => (
          <div key={job.id} data-testid="upload-job">
            <Space style={{ width: "100%", justifyContent: "space-between" }} align="center">
              <Typography.Text ellipsis style={{ maxWidth: 320 }}>{job.name}</Typography.Text>
              <Tag color={statusColor[job.status]}>{t(`upload.status.${job.status}`)}</Tag>
            </Space>
            <Progress
              percent={Math.round(job.progress * 100)}
              status={job.status === "failed" ? "exception" : job.status === "done" ? "success" : "active"}
              size="small"
            />
            {job.error && <Typography.Text type="danger">{job.error}</Typography.Text>}
            <Space size={4}>
              {job.status === "running" && <Button size="small" onClick={() => queue.cancel(job.id)}>{t("upload.cancel")}</Button>}
              {(job.status === "failed" || job.status === "canceled") && (
                <>
                  <Button size="small" onClick={() => queue.retry(job.id)}>{t("common.retry")}</Button>
                  <Button size="small" onClick={() => queue.cancel(job.id)}>{t("upload.discard")}</Button>
                </>
              )}
            </Space>
          </div>
        ))}
        <Typography.Text type="secondary">{t("upload.resumeHint")}</Typography.Text>
      </Space>
    </Modal>
  );
}
