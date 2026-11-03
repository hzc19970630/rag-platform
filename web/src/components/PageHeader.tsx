import { Breadcrumb, Flex, Typography } from "antd";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

export type Crumb = { title: string; to?: string };

export function PageHeader({ breadcrumbs, title, description, extra, eyebrow }: { breadcrumbs?: Crumb[]; title: ReactNode; description?: ReactNode; extra?: ReactNode; eyebrow?: string }) {
  return (
    <div className="page-header">
      {breadcrumbs && breadcrumbs.length > 0 && (
        <Breadcrumb
          items={breadcrumbs.map((crumb) => ({
            title: crumb.to ? <Link to={crumb.to}>{crumb.title}</Link> : crumb.title,
          }))}
        />
      )}
      <Flex align="flex-start" justify="space-between" gap={16} wrap="wrap">
        <div className="page-heading">
          {eyebrow && <div className="eyebrow">{eyebrow}</div>}
          <Typography.Title level={2} className="page-title">{title}</Typography.Title>
          {description && <Typography.Paragraph className="page-description" type="secondary">{description}</Typography.Paragraph>}
        </div>
        {extra && <div className="page-extra">{extra}</div>}
      </Flex>
    </div>
  );
}
