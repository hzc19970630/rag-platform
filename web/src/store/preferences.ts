import { create } from "zustand";
import { persist } from "zustand/middleware";
import i18n from "../i18n";

export type ThemeMode = "light" | "dark" | "system";
export type Language = "zh-CN" | "en-US";

type PreferencesState = {
  themeMode: ThemeMode;
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  language: Language;
  setThemeMode: (mode: ThemeMode) => void;
  setSidebarCollapsed: (collapsed: boolean) => void;
  setSidebarWidth: (width: number) => void;
  setLanguage: (language: Language) => void;
};

export const clampSidebarWidth = (width: number) => Math.min(320, Math.max(220, Math.round(width)));

export const usePreferences = create<PreferencesState>()(
  persist(
    (set) => ({
      themeMode: "system",
      sidebarCollapsed: false,
      sidebarWidth: 260,
      language: (localStorage.getItem("rag-language") as Language | null) ?? "zh-CN",
      setThemeMode: (themeMode) => set({ themeMode }),
      setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
      setSidebarWidth: (sidebarWidth) => set({ sidebarWidth: clampSidebarWidth(sidebarWidth) }),
      setLanguage: (language) => {
        localStorage.setItem("rag-language", language);
        void i18n.changeLanguage(language);
        set({ language });
      },
    }),
    {
      name: "rag-workbench-preferences",
      partialize: (state) => ({
        themeMode: state.themeMode,
        sidebarCollapsed: state.sidebarCollapsed,
        sidebarWidth: state.sidebarWidth,
        language: state.language,
      }),
    },
  ),
);
