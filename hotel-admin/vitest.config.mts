import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";

// Deterministic component-test setup for the Hotel Admin UI. jsdom environment; the "@/..." alias
// mirrors tsconfig; React 18 automatic JSX via esbuild (no vite react plugin needed for RTL). Only the
// "test/" tree is executed — never bundled into the app.
export default defineConfig({
  esbuild: { jsx: "automatic", jsxImportSource: "react" },
  resolve: {
    alias: { "@": fileURLToPath(new URL("./", import.meta.url)) },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./test/setup.ts"],
    include: ["test/**/*.test.{ts,tsx}"],
    css: false,
    restoreMocks: true,
  },
});
