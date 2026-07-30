// Unit tests for the pure report/time modules. A dedicated config keeps the
// PWA plugin and the dev proxy out of the test pipeline; the svelte plugin is
// still needed because the import chain reaches .svelte.ts rune modules.
import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte()],
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
