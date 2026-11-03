import {
  AppstoreOutlined,
  ControlOutlined,
  DesktopOutlined,
  GlobalOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MenuOutlined,
  MessageOutlined,
  MoonOutlined,
  SearchOutlined,
  SunOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useQueryClient } from "@tanstack/react-query";
import { Avatar, Button, Drawer, Dropdown, Flex, Input, Layout, List, Menu, Modal, Space, Tooltip, Typography, Grid, type MenuProps } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { api } from "../api/client";
import { useKnowledgeBases } from "../api/hooks";
import { usePermissions } from "../session";
import { clampSidebarWidth, usePreferences, type Language, type ThemeMode } from "../store/preferences";

const { Sider, Content } = Layout;

export function useNavigation() {
  const { canChat, canOperate } = usePermissions();
  const { t } = useTranslation();
  return useMemo<MenuProps["items"]>(() => {
    const items: MenuProps["items"] = [{ key: "knowledge-bases", icon: <AppstoreOutlined />, label: t("nav.knowledgeBases") }];
    if (canChat) items.push({ key: "chat", icon: <MessageOutlined />, label: t("nav.chat") });
    if (canOperate) items.push({ key: "ops", icon: <ControlOutlined />, label: t("nav.ops") });
    return items;
  }, [canChat, canOperate, t]);
}

function ThemeMenu({ onSelect }: { onSelect: (mode: ThemeMode) => void }) {
  return {
    items: [
      { key: "light", icon: <SunOutlined />, label: "Light" },
      { key: "dark", icon: <MoonOutlined />, label: "Dark" },
      { key: "system", icon: <DesktopOutlined />, label: "System" },
    ],
    onClick: ({ key }: { key: string }) => onSelect(key as ThemeMode),
  };
}

function SidebarContent({ collapsed, onNavigate }: { collapsed: boolean; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const { me } = usePermissions();
  const items = useNavigation();
  const themeMode = usePreferences((state) => state.themeMode);
  const setThemeMode = usePreferences((state) => state.setThemeMode);
  const language = usePreferences((state) => state.language);
  const setLanguage = usePreferences((state) => state.setLanguage);

  const selected = location.pathname.startsWith("/chat")
    ? "chat"
    : location.pathname.startsWith("/ops")
      ? "ops"
      : "knowledge-bases";

  async function logout() {
    try {
      await api("/auth/logout", { method: "POST" });
    } catch {
      // A dead session is indistinguishable from a successful logout here.
    }
    queryClient.clear();
    navigate("/login", { replace: true });
  }

  const themeIcon = themeMode === "dark" ? <MoonOutlined /> : themeMode === "light" ? <SunOutlined /> : <DesktopOutlined />;

  return (
    <Flex vertical className="sidebar-inner" justify="space-between">
      <div>
        <div className={collapsed ? "brand brand-collapsed" : "brand"}>
          <span className="brand-mark" aria-hidden="true">R</span>
          {!collapsed && <span className="brand-name">{t("brand")}</span>}
        </div>
        <Menu
          mode="inline"
          selectedKeys={[selected]}
          items={items}
          onClick={({ key }) => {
            navigate(`/${key}`);
            onNavigate?.();
          }}
          className="sidebar-menu"
          inlineCollapsed={collapsed}
        />
      </div>
      <div className="sidebar-footer">
        {!collapsed && (
          <div className="sidebar-identity">
            <Avatar size={28} icon={<UserOutlined />} />
            <div className="sidebar-identity-text">
              <Typography.Text ellipsis>{me.principal.subjectId}</Typography.Text>
              <Typography.Text type="secondary" ellipsis>{me.principal.tenantId}</Typography.Text>
            </div>
          </div>
        )}
        <Space size={4} className={collapsed ? "sidebar-actions sidebar-actions-collapsed" : "sidebar-actions"}>
          <Dropdown menu={ThemeMenu({ onSelect: setThemeMode })} trigger={["click"]} placement="topRight">
            <Tooltip title={`Theme: ${themeMode}`}>
              <Button type="text" size="small" aria-label="theme" icon={themeIcon} />
            </Tooltip>
          </Dropdown>
          <Dropdown
            trigger={["click"]}
            placement="topRight"
            menu={{
              items: [
                { key: "zh-CN", label: "简体中文" },
                { key: "en-US", label: "English" },
              ],
              onClick: ({ key }) => setLanguage(key as Language),
            }}
          >
            <Tooltip title="Language">
              <Button type="text" size="small" aria-label="language" icon={<GlobalOutlined />} />
            </Tooltip>
          </Dropdown>
          {!me.developmentIdentity && (
            <Tooltip title={t("auth.logout")}>
              <Button type="text" size="small" aria-label={t("auth.logout")} icon={<LogoutOutlined />} onClick={() => void logout()} />
            </Tooltip>
          )}
        </Space>
        {!collapsed && <span className="sidebar-language">{language === "zh-CN" ? "简体中文" : "English"}</span>}
      </div>
    </Flex>
  );
}

export function AppShell() {
  const { t } = useTranslation();
  const screens = Grid.useBreakpoint();
  const navigate = useNavigate();
  const collapsed = usePreferences((state) => state.sidebarCollapsed);
  const setCollapsed = usePreferences((state) => state.setSidebarCollapsed);
  const width = usePreferences((state) => state.sidebarWidth);
  const setWidth = usePreferences((state) => state.setSidebarWidth);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [search, setSearch] = useState("");
  const dragging = useRef(false);
  const compact = screens.md === false;
  const knowledgeBases = useKnowledgeBases();

  useEffect(() => {
    if (screens.lg === false && screens.md !== false && !collapsed) setCollapsed(true);
  }, [screens.lg, screens.md, collapsed, setCollapsed]);

  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);

  useEffect(() => {
    const move = (event: PointerEvent) => {
      if (!dragging.current) return;
      setWidth(clampSidebarWidth(event.clientX));
    };
    const stop = () => { dragging.current = false; document.body.classList.remove("resizing"); };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
  }, [setWidth]);

  const filtered = (knowledgeBases.data ?? []).filter((kb) => {
    const needle = search.trim().toLowerCase();
    return !needle || kb.name.toLowerCase().includes(needle) || kb.description.toLowerCase().includes(needle);
  });

  return (
    <Layout className="app-shell">
      {compact ? (
        <Drawer
          open={drawerOpen}
          placement="left"
          size={260}
          onClose={() => setDrawerOpen(false)}
          styles={{ body: { padding: 0 } }}
          title={null}
        >
          <SidebarContent collapsed={false} onNavigate={() => setDrawerOpen(false)} />
        </Drawer>
      ) : (
        <Sider
          className="app-sider"
          theme="light"
          width={width}
          collapsedWidth={48}
          collapsed={collapsed}
          trigger={null}
        >
          <SidebarContent collapsed={collapsed} />
          {!collapsed && (
            <div
              className="sidebar-resizer"
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize sidebar"
              tabIndex={0}
              onPointerDown={() => {
                dragging.current = true;
                document.body.classList.add("resizing");
              }}
              onKeyDown={(event) => {
                if (event.key === "ArrowLeft") setWidth(width - 8);
                if (event.key === "ArrowRight") setWidth(width + 8);
              }}
            />
          )}
        </Sider>
      )}
      <Layout className="app-main">
        <header className="app-topbar">
          <Flex align="center" gap={8}>
            <Button
              type="text"
              aria-label={compact ? "open navigation" : collapsed ? "expand navigation" : "collapse navigation"}
              icon={compact ? <MenuOutlined /> : collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              onClick={() => (compact ? setDrawerOpen(true) : setCollapsed(!collapsed))}
            />
            <Button type="text" className="global-search-trigger" icon={<SearchOutlined />} onClick={() => setSearchOpen(true)}>
              {t("nav.search")}
              <kbd>⌘K</kbd>
            </Button>
          </Flex>
          <Flex align="center" gap={8}>
            <Typography.Text type="secondary" className="topbar-tenant">{t("auth.verified")}</Typography.Text>
          </Flex>
        </header>
        <Content className="app-content">
          <Outlet />
        </Content>
      </Layout>

      <Modal
        open={searchOpen}
        footer={null}
        onCancel={() => setSearchOpen(false)}
        title={t("nav.search")}
        width={560}
      >
        <Input
          autoFocus
          prefix={<SearchOutlined />}
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t("knowledgeBase.searchPlaceholder")}
          aria-label={t("nav.search")}
        />
        <List
          className="search-results"
          dataSource={filtered}
          locale={{ emptyText: t("knowledgeBase.empty") }}
          renderItem={(kb) => (
            <List.Item
              className="search-result"
              onClick={() => {
                setSearchOpen(false);
                setSearch("");
                navigate(`/knowledge-bases/${kb.id}`);
              }}
            >
              <List.Item.Meta title={kb.name} description={kb.description || kb.id} />
            </List.Item>
          )}
        />
      </Modal>
    </Layout>
  );
}
