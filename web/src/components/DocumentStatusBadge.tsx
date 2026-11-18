import { Tag, Tooltip } from "antd";
import { useTranslation } from "react-i18next";
import { documentStatusLabel, documentStatusTone, statusLabel, toneColor } from "../utils/enums";

export function DocumentStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  const tone = documentStatusTone(status);
  return (
    <Tooltip title={statusLabel(t, status)}>
      <Tag color={toneColor(tone)} className="status-badge">
        <span className={`status-dot status-${tone}`} aria-hidden="true" />
        {documentStatusLabel(t, status)}
      </Tag>
    </Tooltip>
  );
}