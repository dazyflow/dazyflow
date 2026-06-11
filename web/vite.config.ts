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
      // The webhook trigger + hosted form live on the daemon too. Proxy
      // them so the URLs the editor displays (built from the dev
      // public-base-url, i.e. this origin) work when copy-pasted.
      "/trigger": { target, changeOrigin: true },
      "/form": { target, changeOrigin: true },
    },
    // Vite 5.4+ blocks unknown Host headers as a DNS-rebind defense.
    // Allow the reverse-proxy hostnames we expect — comma-separated
    // VITE_ALLOWED_HOSTS overrides the default localhost set.
    allowedHosts: process.env.VITE_ALLOWED_HOSTS
      ? process.env.VITE_ALLOWED_HOSTS.split(",").map((h) => h.trim())
      : ["localhost", "127.0.0.1"],
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
