import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  // Each test runs its own server + browser page; cap workers so ticking-clock
  // assertions are not starved by CPU contention.
  workers: 4,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"]],
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    // The feed keeps the reader's place by hand because Safari has no scroll
    // anchoring, so its specs run in WebKit as well.
    { name: "webkit", testMatch: /feed\.spec\.ts/, use: { ...devices["Desktop Safari"], locale: "en-US" } },
  ],
});
