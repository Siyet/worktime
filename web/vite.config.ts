import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { VitePWA } from "vite-plugin-pwa";

export default defineConfig({
  plugins: [
    svelte(),
    VitePWA({
      registerType: "autoUpdate",
      manifest: {
        name: "WorkTime",
        short_name: "WorkTime",
        description: "Free, open-source, offline-first time tracker",
        theme_color: "#2563eb",
        background_color: "#0f172a",
        display: "standalone",
        start_url: "/",
        icons: [
          { src: "/pwa-192.png", sizes: "192x192", type: "image/png" },
          { src: "/pwa-512.png", sizes: "512x512", type: "image/png" },
          { src: "/pwa-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
        ],
      },
      workbox: {
        // The app shell is precached; API calls must never be served from cache -
        // offline data lives in IndexedDB, not in HTTP cache.
        navigateFallbackDenylist: [/^\/api/, /^\/auth/, /^\/mcp/, /^\/healthz/],
      },
    }),
  ],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/auth": "http://localhost:8080",
      "/mcp": "http://localhost:8080",
    },
  },
  build: {
    target: "es2022",
    reportCompressedSize: true,
  },
});
