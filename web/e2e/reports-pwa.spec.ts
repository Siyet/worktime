import { expect, pushBarrier, seedServer, test } from "./fixtures";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

// 9:00 local anchors every seed to the intended day. "An hour ago" lands in
// yesterday when the suite runs after midnight, and on a Monday that is also the
// previous calendar week, which empties the Week view.
function todayNine(): number {
  return new Date().setHours(9, 0, 0, 0);
}

test.describe("reports", () => {
  test("per-project totals for this week and this month", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    const base = todayNine();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [
        { description: "seeded 2h", startedAt: base, stoppedAt: base + 1.5 * HOUR, projectID },
        { description: "seeded 30m", startedAt: base + 2 * HOUR, stoppedAt: base + 2.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "Week", exact: true }).click();
    const stats = page.locator(".stats");
    await expect(stats).toContainText("2.0h");

    const backendRow = page.locator(".proj-item").filter({ hasText: "Backend" });
    await expect(backendRow).toContainText("1h 30m");
    const noProjectRow = page.locator(".proj-item").filter({ hasText: "No project" });
    await expect(noProjectRow).toContainText("30m");

    // All entries started today, so the month view shows the same grand total.
    await page.getByRole("button", { name: "Month" }).click();
    await expect(stats).toContainText("2.0h");
  });

  test("period boundaries: old entries leave this week but stay in last 30 days and leave the Timer feed", async ({
    page,
    server,
  }) => {
    const base = todayNine();
    await seedServer(server.url, {
      entries: [
        { description: "old work", startedAt: base - 12 * DAY, stoppedAt: base - 12 * DAY + 2 * HOUR },
        { description: "recent work", startedAt: base, stoppedAt: base + 0.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "Week", exact: true }).click();
    const stats = page.locator(".stats");
    await expect(stats).toContainText("0.5h");
    await expect(stats).not.toContainText("2.5h");

    await page.getByRole("button", { name: "30 days" }).click();
    await expect(stats).toContainText("2.5h");

    // The Timer feed shows only the last 7 days.
    await page.goto(server.url + "/#/");
    await expect(page.locator(".item").filter({ hasText: "recent work" })).toBeVisible();
    await expect(page.getByText("old work")).toHaveCount(0);
  });

  test("time off day counts appear in the report", async ({ page, server }) => {
    const isoDate = (offsetDays: number) => {
      const date = new Date(Date.now() + offsetDays * DAY);
      return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    };
    await seedServer(server.url, {
      timeOff: [{ kind: "vacation", dateFrom: isoDate(-1), dateTo: isoDate(0) }],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "30 days" }).click();
    await expect(page.locator(".stats")).toContainText("2 vac");
  });
});

test.describe("pwa", () => {
  test("manifest is served and the service worker registers", async ({ page, server }) => {
    await page.goto(server.url + "/");
    await page.evaluate(() => navigator.serviceWorker.ready);

    const manifest = await page.request.get(server.url + "/manifest.webmanifest");
    expect(manifest.ok()).toBe(true);
    const parsed = await manifest.json();
    expect(parsed.name).toBe("WorkTime");
    expect(parsed.icons.length).toBeGreaterThanOrEqual(2);
  });

  test("offline reload: shell from cache, data from IndexedDB, reports usable", async ({ page, server, context }) => {
    const projectID = crypto.randomUUID();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Cached" }],
      entries: [
        { description: "cached work", startedAt: Date.now() - 2 * HOUR, stoppedAt: Date.now() - 1 * HOUR, projectID },
      ],
    });

    // Warm-up: first load installs the SW; reload guarantees it controls the page.
    await page.goto(server.url + "/#/");
    await page.evaluate(() => navigator.serviceWorker.ready);
    await page.reload();
    await expect(page.locator(".item").filter({ hasText: "cached work" })).toBeVisible();

    // While offline, finish one more entry so the reload also proves offline-created data survives.
    await context.setOffline(true);
    await page.getByPlaceholder("What are you working on?").fill("finished offline");
    await page.getByRole("button", { name: "Start" }).click();
    await page
      .locator(".item")
      .filter({ hasText: "finished offline" })
      .getByRole("button", { name: "Stop" })
      .click();

    await page.reload();
    await expect(page.getByPlaceholder("What are you working on?")).toBeVisible();
    // Network emulation does not always propagate navigator.onLine to a page
    // served by the service worker, so the header may read "sync error" instead
    // of "offline" - both prove the app is up without a reachable server.
    await expect(page.locator(".status")).toContainText(/offline|sync error/);
    await expect(page.locator(".item").filter({ hasText: "cached work" })).toBeVisible();
    await expect(page.locator(".item").filter({ hasText: "finished offline" })).toBeVisible();

    await page.goto(server.url + "/#/reports");
    await expect(page.locator(".stats")).toContainText("1.0h");
  });
});
