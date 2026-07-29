import { expect, pushBarrier, seedServer, test, triggerSync } from "./fixtures";
import type { Page } from "@playwright/test";

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

function status(page: Page) {
  return page.locator(".status");
}

async function startTimer(page: Page, description: string) {
  await page.getByPlaceholder("What are you working on?").fill(description);
  await page.getByRole("button", { name: "Start" }).click();
}

test.describe("offline and sync", () => {
  test("offline: status flips, pending counts dirty rows, timers keep ticking", async ({ page, server, context }) => {
    await page.goto(server.url + "/#/");
    await expect(status(page)).toHaveText("synced");

    await context.setOffline(true);
    await expect(status(page)).toContainText("offline");

    await startTimer(page, "offline work A");
    await startTimer(page, "offline work B");
    await expect(status(page)).toHaveText("offline (2)");

    // Stopping an already-dirty row must not grow the pending count.
    const rowA = runningCard(page).locator(".item").filter({ hasText: "offline work A" });
    await rowA.getByRole("button", { name: "Stop" }).click();
    await expect(status(page)).toHaveText("offline (2)");

    const rowB = runningCard(page).locator(".item").filter({ hasText: "offline work B" });
    await expect(rowB.locator(".elapsed")).toBeVisible();
    const before = await rowB.locator(".elapsed").textContent();
    await expect(rowB.locator(".elapsed")).not.toHaveText(before!, { timeout: 10_000 });
  });

  test("reconnect: offline changes reach the server and status returns to synced", async ({ page, server, context }) => {
    await page.goto(server.url + "/#/");
    await expect(status(page)).toHaveText("synced");

    await context.setOffline(true);
    await startTimer(page, "born offline");
    const rowStop = runningCard(page).locator(".item").filter({ hasText: "born offline" });
    await rowStop.getByRole("button", { name: "Stop" }).click();
    await expect(status(page)).toHaveText("offline (1)");

    const pushed = pushBarrier(page, "born offline");
    await context.setOffline(false);
    await pushed;
    await expect(status(page)).toHaveText("synced");

    const response = await fetch(server.url + "/api/entries");
    const entries = await response.json();
    const match = entries.filter((entry: { description: string }) => entry.description === "born offline");
    expect(match).toHaveLength(1);
    expect(match[0].stopped_at).not.toBeNull();
  });

  test("sync HTTP error: status shows sync error, data stays local, recovery pushes after restart", async ({
    page,
    server,
  }) => {
    await page.goto(server.url + "/#/");
    await expect(status(page)).toHaveText("synced");

    // The server starts rejecting pushes.
    await page.route("**/api/sync", (route) => route.fulfill({ status: 500, body: "boom" }));
    await startTimer(page, "queued change");
    const row = runningCard(page).locator(".item").filter({ hasText: "queued change" });
    await row.getByRole("button", { name: "Stop" }).click();
    await expect(status(page)).toHaveText("sync error (1)");

    // Reload while the endpoint is still broken: the dirty queue must survive restart.
    await page.route("**/api/sync", (route) => route.fulfill({ status: 500, body: "boom" }));
    await page.reload();
    await expect(page.locator(".item").filter({ hasText: "queued change" })).toBeVisible();

    // Server recovers: the queued change is pushed and the status heals.
    await page.unroute("**/api/sync");
    const pushed = pushBarrier(page, "queued change");
    await triggerSync(page);
    await pushed;
    await expect(status(page)).toHaveText("synced");

    const entries = await (await fetch(server.url + "/api/entries")).json();
    expect(entries.some((entry: { description: string }) => entry.description === "queued change")).toBe(true);
  });

  test("two devices: fresh context bootstraps full state, open context pulls increments", async ({
    page,
    server,
    browser,
  }) => {
    // Device A creates a project, a finished entry and a time off range.
    await page.goto(server.url + "/#/projects");
    const projectPushed = pushBarrier(page, "Shared");
    await page.getByPlaceholder("New project name").fill("Shared");
    await page.getByRole("button", { name: "Add" }).click();
    await projectPushed;

    await page.goto(server.url + "/#/");
    const entryPushed = pushBarrier(page, "from A");
    await startTimer(page, "from A");
    await runningCard(page).locator(".item").getByRole("button", { name: "Stop" }).click();
    await entryPushed;

    await page.goto(server.url + "/#/timeoff");
    const timeOffPushed = pushBarrier(page, "vacation");
    await page.getByRole("button", { name: "Add" }).click();
    await timeOffPushed;

    // Device B: a fresh browser context (separate IndexedDB) bootstraps everything.
    const contextB = await browser.newContext();
    const pageB = await contextB.newPage();
    await pageB.goto(server.url + "/#/");
    await expect(pageB.locator(".item").filter({ hasText: "from A" })).toBeVisible();
    await pageB.goto(server.url + "/#/projects");
    await expect(pageB.locator("input[aria-label='Name']")).toHaveValue("Shared");
    await pageB.goto(server.url + "/#/timeoff");
    await expect(pageB.locator(".item").filter({ hasText: "Vacation" })).toBeVisible();

    // Device A adds one more entry; B pulls it on its next sync trigger.
    await page.goto(server.url + "/#/");
    const incrementPushed = pushBarrier(page, "increment from A");
    await startTimer(page, "increment from A");
    await runningCard(page).locator(".item").getByRole("button", { name: "Stop" }).click();
    await incrementPushed;

    await pageB.goto(server.url + "/#/");
    await triggerSync(pageB);
    await expect(pageB.locator(".item").filter({ hasText: "increment from A" })).toBeVisible();

    await contextB.close();
  });

  test("reports and local data work fully offline after a prior sync", async ({ page, server, context }) => {
    const hour = 3_600_000;
    await seedServer(server.url, {
      entries: [{ description: "seeded work", startedAt: Date.now() - 2 * hour, stoppedAt: Date.now() - hour }],
    });

    await page.goto(server.url + "/#/");
    await expect(page.locator(".item").filter({ hasText: "seeded work" })).toBeVisible();

    await context.setOffline(true);
    await page.goto(server.url + "/#/reports");
    await expect(page.locator(".card").first()).toContainText("1h 00m");
    await expect(status(page)).toContainText("offline");
  });
});
