import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies /api/v1 to the daemon so the browser sees a
// same-origin call (no CORS friction). Override HAZYFLOW_API in your
// shell or .env to point at a different host. Production builds hit
// the daemon directly via VITE_API_BASE.
const target = process.env.HAZYFLOW_API ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api/v1": { target, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
