import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte()],
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
