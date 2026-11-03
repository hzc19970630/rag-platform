import {
  ApartmentOutlined,
  ApiOutlined,
  DatabaseOutlined,
  InfoCircleOutlined,
  SettingOutlined,
  TeamOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { Drawer, Grid, Menu, Typography, type MenuProps } from "antd";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { KnowledgeBase, KnowledgeBaseConfigurationV1 } from "../../api/types";
import { BasicSettings } from "./BasicSettings";
import { DangerZone } from "./DangerZone";
import { DataSourceSettings } from "./DataSourceSettings";
import { MembersSettings } from "./MembersSettings";
import { ProjectionPanel } from "./ProjectionPanel";
import { RetrievalSettings } from "./RetrievalSettings";
import { SystemInfoSection } from "./SystemInfoSection";

type SectionKey = "basic" | "retrieval" | "members" | "projection" | "dataSource" | "system" | "danger";

type Props = {
  open: boolean;
  onClose: () => void;
  knowledgeBase: KnowledgeBase;
  configuration: KnowledgeBaseConfigurationV1;
  canWrite: boolean;
  canManage: boolean;
  canDelete: boolean;
  canOperate: boolean;
  onChanged: () => void;
};

export function SettingsDrawer(props: Props) {
  const { open, onClose, knowledgeBase, configuration, canWrite, canManage, canDelete, canOperate, onChanged } = props;
  const { t } = useTranslation();
  const screens = Grid.useBreakpoint();
  const [section, setSection] = useState<SectionKey>("basic");

  const projectionEnabled = configuration.indexingStrategy.wiki_enabled || configuration.indexingStrategy.graph_enabled;

  const items = useMemo<MenuProps["items"]>(() => {
    const entries: Array<{ key: SectionKey; icon: ReactNode; label: string }> = [
      { key: "basic", icon: <SettingOutlined />, label: t("settings.basic") },
      { key: "retrieval", icon: <DatabaseOutlined />, label: t("settings.retrieval") },
    ];
    if (canManage) entries.push({ key: "members", icon: <TeamOutlined />, label: t("settings.members") });
    if (projectionEnabled) entries.push({ key: "projection", icon: <ApartmentOutlined />, label: t("settings.projection") });
    entries.push({ key: "dataSource", icon: <ApiOutlined />, label: t("settings.dataSource") });
    entries.push({ key: "system", icon: <InfoCircleOutlined />, label: t("settings.system") });
    if (canDelete) entries.push({ key: "danger", icon: <WarningOutlined />, label: t("settings.danger") });
    return entries;
  }, [canManage, canDelete, projectionEnabled, t]);

  useEffect(() => {
    if (!open) return;
    const available = new Set((items ?? []).map((item) => String(item?.key)));
    if (!available.has(section)) setSection("basic");
  }, [open, items, section]);

  const content = (() => {
    switch (section) {
      case "retrieval":
        return <RetrievalSettings knowledgeBaseId={knowledgeBase.id} configuration={configuration} canWrite={canWrite} onSaved={onChanged} />;
      case "members":
        return canManage
          ? <MembersSettings knowledgeBaseId={knowledgeBase.id} ownerId={knowledgeBase.ownerId} canManage onChanged={onChanged} />
          : null;
      case "projection":
        return projectionEnabled
          ? <ProjectionPanel knowledgeBaseId={knowledgeBase.id} canWrite={canWrite} embedded />
          : null;
      case "dataSource":
        return <DataSourceSettings knowledgeBaseId={knowledgeBase.id} canOperate={canOperate} />;
      case "system":
        return <SystemInfoSection configuration={configuration} />;
      case "danger":
        return canDelete ? <DangerZone knowledgeBase={knowledgeBase} canDelete /> : null;
      default:
        return <BasicSettings knowledgeBase={knowledgeBase} canWrite={canWrite} onSaved={onChanged} />;
    }
  })();

  return (
    <Drawer
      open={open}
      onClose={onClose}
      size={screens.lg === false ? "100%" : 1080}
      title={(
        <div className="settings-title">
          <Typography.Title level={4}>{t("settings.title")}</Typography.Title>
          <Typography.Text type="secondary">{knowledgeBase.name}</Typography.Text>
        </div>
      )}
      className="settings-drawer"
      styles={{ body: { padding: 0 } }}
      destroyOnHidden
    >
      <div className="settings-layout">
        <nav className="settings-nav" aria-label={t("settings.title")}>
          <Menu
            mode="inline"
            selectedKeys={[section]}
            items={items}
            onClick={({ key }) => setSection(key as SectionKey)}
          />
        </nav>
        <div className="settings-content">{content}</div>
      </div>
    </Drawer>
  );
}
