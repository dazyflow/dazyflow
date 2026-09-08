// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

// The dev server proxies /api/v1 to the daemon so the browser sees a
// same-origin call (no CORS friction). Override DAZYFLOW_API in your
// shell or .env to point at a different host. Production builds hit
// the daemon directly via VITE_API_BASE.
const target = process.env.DAZYFLOW_API ?? "http://localhost:8080";

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api/v1": { target, changeOrigin: true },
      "/trigger": { target, changeOrigin: true },
      "/form": { target, changeOrigin: true },
    },
    allowedHosts: process.env.VITE_ALLOWED_HOSTS
      ? process.env.VITE_ALLOWED_HOSTS.split(",").map((h) => h.trim())
      : ["localhost", "127.0.0.1"],
  },
  build:
    mode === "docs"
      ? {
          outDir: "dist-docs",
          sourcemap: true,
          rollupOptions: { input: resolve(__dirname, "docs.html") },
        }
      : {
          outDir: "dist",
          sourcemap: true,
        },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
}));
