import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: "vendor-react", test: /node_modules[\\/](react|react-dom|scheduler|react-router|react-router-dom)[\\/]/ },
            // antd and its rc-* primitives dominate the bundle; split them by
            // subsystem so no single chunk trips rolldown's size warning.
            // Groups capture dependencies recursively, so the tree group must
            // come before the table group that also pulls rc-tree in.
            {
              name: "vendor-antd-tree",
              test: /node_modules[\\/](rc-tree|rc-virtual-list)[\\/]|node_modules[\\/]antd[\\/]es[\\/](tree|transfer|upload)[\\/]/,
            },
            {
              name: "vendor-antd-table",
              test: /node_modules[\\/](rc-table|rc-pagination|rc-checkbox|rc-radio|rc-progress|rc-segmented|rc-collapse|rc-tabs)[\\/]|node_modules[\\/]antd[\\/]es[\\/](table|tabs|collapse|pagination|checkbox|radio|progress|segmented)[\\/]/,
            },
            {
              name: "vendor-antd-form",
              test: /node_modules[\\/](rc-field-form|rc-select|rc-cascader|rc-input|rc-input-number|rc-mentions|rc-slider|rc-switch|rc-textarea|rc-picker|rc-tooltip|rc-dropdown|rc-menu|rc-motion|rc-notification|rc-dialog|rc-drawer)[\\/]|node_modules[\\/]antd[\\/]es[\\/](form|select|input|input-number|mentions|slider|switch|date-picker|time-picker|menu|dropdown|modal|drawer|notification)[\\/]/,
            },
            { name: "vendor-antd", test: /node_modules[\\/](antd|@ant-design|rc-[^\\/]+|@rc-component|dayjs)[\\/]/ },
            { name: "vendor-tanstack", test: /node_modules[\\/]@tanstack[\\/]/ },
            { name: "vendor-i18n", test: /node_modules[\\/](i18next|react-i18next)[\\/]/ },
          ],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/api/generated.ts",
        "src/api/types.ts",
        "src/**/*.test.{ts,tsx}",
        "src/test/**",
        "src/main.tsx",
        "src/theme.ts",
        "src/vite-env.d.ts",
      ],
      // Ratcheted after the query/settings/page suites landed. Raise these as
      // new areas are covered instead of letting coverage drift back down.
      thresholds: {
        statements: 42,
        branches: 35,
        functions: 37,
        lines: 43,
      },
    },
  },
});