import { Button, Empty } from "antd";
import type { ReactNode } from "react";

export function EmptyState({ action, actionLabel, description }: { action?: () => void; actionLabel?: string; description: ReactNode }) {
  return (
    <Empty
      className="empty-state"
      image={Empty.PRESENTED_IMAGE_SIMPLE}
      description={description}
    >
      {action && actionLabel ? <Button type="primary" onClick={action}>{actionLabel}</Button> : null}
    </Empty>
  );
}
