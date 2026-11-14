import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
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