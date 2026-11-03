import { theme, type ThemeConfig } from "antd";
import { useEffect, useMemo, useState } from "react";
import { usePreferences } from "./store/preferences";

export const brand = {
  primary: "#067A3E",
  hover: "#056B36",
  active: "#045C2E",
  bright: "#07C05F",
  light: "#E9F8EC",
  focus: "rgba(6, 122, 62, 0.25)",
  disabled: "#8CE0AF",
};

const sharedToken = {
  colorPrimary: brand.primary,
  colorLink: brand.primary,
  colorSuccess: "#047857",
  colorWarning: "#B45309",
  colorError: "#B42318",
  colorInfo: brand.primary,
  borderRadius: 6,
  borderRadiusSM: 3,
  borderRadiusLG: 9,
  borderRadiusXS: 2,
  controlHeight: 34,
  fontSize: 14,
  fontFamily:
    '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
  wireframe: false,
};

export function buildAntdTheme(dark: boolean): ThemeConfig {
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      ...sharedToken,
      colorPrimary: dark ? "#4ADE80" : brand.primary,
      colorPrimaryHover: dark ? "#86EFAC" : brand.hover,
      colorPrimaryActive: dark ? "#22C55E" : brand.active,
      colorBgLayout: dark ? "#141414" : "#F3F3F3",
      colorBgContainer: dark ? "#1F1F1F" : "#FFFFFF",
      colorBgElevated: dark ? "#262626" : "#FFFFFF",
      colorBorder: dark ? "#7A7A7A" : "#848E99",
      colorBorderSecondary: dark ? "#3A3A3A" : "#E7E7E7",
      colorText: dark ? "rgba(255,255,255,0.88)" : "rgba(0,0,0,0.88)",
      colorTextSecondary: dark ? "rgba(255,255,255,0.65)" : "rgba(0,0,0,0.60)",
      colorTextTertiary: dark ? "rgba(255,255,255,0.53)" : "#64717D",
      colorTextQuaternary: dark ? "rgba(255,255,255,0.32)" : "rgba(0,0,0,0.26)",
      colorFillAlter: dark ? "#262626" : "#F3F3F3",
      colorLink: dark ? "#4ADE80" : brand.primary,
      colorLinkHover: dark ? "#86EFAC" : brand.hover,
      colorInfo: dark ? "#4ADE80" : brand.primary,
      colorSuccess: dark ? "#4ADE80" : "#047857",
      colorWarning: dark ? "#FBBF24" : "#B45309",
      colorError: dark ? "#F87171" : "#B42318",
      boxShadow: dark ? "0 2px 10px rgba(0,0,0,0.55)" : "0 1px 3px rgba(0,0,0,0.06)",
      boxShadowSecondary: dark ? "0 8px 28px rgba(0,0,0,0.65)" : "0 6px 24px rgba(0,0,0,0.10)",
    },
    components: {
      Layout: {
        siderBg: dark ? "#171717" : "#F9F9F9",
        headerBg: dark ? "#1F1F1F" : "#FFFFFF",
        bodyBg: dark ? "#141414" : "#F3F3F3",
      },
      Menu: {
        itemBg: "transparent",
        itemSelectedBg: dark ? "rgba(74, 222, 128, 0.16)" : brand.light,
        itemSelectedColor: dark ? "#86EFAC" : brand.active,
        itemHoverBg: dark ? "#262626" : "#F3F3F3",
        itemBorderRadius: 6,
      },
      Card: { borderRadiusLG: 9 },
      Button: {
        borderRadius: 6,
        controlHeight: 34,
        primaryColor: dark ? "#052E1A" : "#FFFFFF",
        dangerColor: dark ? "#450A0A" : "#FFFFFF",
      },
      Input: { borderRadius: 6 },
      Tabs: {
        itemSelectedColor: dark ? "#86EFAC" : brand.primary,
        inkBarColor: dark ? "#4ADE80" : brand.primary,
      },
    },
  };
}

export function useResolvedTheme() {
  const mode = usePreferences((state) => state.themeMode);
  const [systemDark, setSystemDark] = useState(() => window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false);

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const listener = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    media.addEventListener("change", listener);
    return () => media.removeEventListener("change", listener);
  }, []);

  const dark = mode === "dark" || (mode === "system" && systemDark);
  const config = useMemo(() => buildAntdTheme(dark), [dark]);

  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    document.documentElement.style.colorScheme = dark ? "dark" : "light";
  }, [dark]);

  return { dark, config, mode };
}
