import { Tag } from "antd";
import { useTranslation } from "react-i18next";
import { statusLabel, statusTone, toneColor } from "../utils/enums";

export function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  const tone = statusTone(status);
  return (
    <Tag color={toneColor(tone)} className="status-badge">
      <span className={`status-dot status-${tone}`} aria-hidden="true" />
      {statusLabel(t, status)}
    </Tag>
  );
}
