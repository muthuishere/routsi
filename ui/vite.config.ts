import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The built app is embedded into the routsi binary (internal/server/public,
// //go:embed all:public) and served at "/". Relative base so it works no
// matter what host/port/prefix routsi is reached on.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "../internal/server/public",
    emptyOutDir: true,
    // One JS + one CSS file keeps the embedded payload easy to reason about.
    rollupOptions: { output: { manualChunks: undefined } },
  },
  server: {
    // `npm run dev` proxies the API to a locally running routsi. Point it
    // elsewhere with ROUTSI_URL=http://localhost:8080 npm run dev.
    proxy: Object.fromEntries(
      ["/stats", "/config", "/audit", "/v1", "/metrics", "/health"].map((p) => [
        p,
        {
          target: process.env.ROUTSI_URL ?? "http://localhost:11080",
          changeOrigin: true,
        },
      ]),
    ),
  },
});
