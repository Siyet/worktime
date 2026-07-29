import { expect, pushBarrier, seedServer, test } from "./fixtures";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

test.describe("reports", () => {
  test("per-project totals for this week and this month", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [
        { description: "seeded 2h", startedAt: Date.now() - 2 * HOUR, stoppedAt: Date.now() - 0.5 * HOUR, projectID },
        { description: "seeded 30m", startedAt: Date.now() - 1 * HOUR, stoppedAt: Date.now() - 0.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    const summary = page.locator(".card").first();
    await expect(summary).toContainText("2h 00m");

    const backendRow = page.locator(".item").filter({ hasText: "Backend" });
    await expect(backendRow).toContainText("1h 30m");
    const noProjectRow = page.locator(".item").filter({ hasText: "No project" });
    await expect(noProjectRow).toContainText("30m");

    // All entries started today, so the month view shows the same grand total.
    await page.getByLabel("Period").selectOption("month");
    await expect(summary).toContainText("2h 00m");
  });

  test("period boundaries: old entries leave this week but stay in last 30 days and leave the Timer feed", async ({
    page,
    server,
  }) => {
    await seedServer(server.url, {
      entries: [
        { description: "old work", startedAt: Date.now() - 12 * DAY, stoppedAt: Date.now() - 12 * DAY + 2 * HOUR },
        { description: "recent work", startedAt: Date.now() - 1 * HOUR, stoppedAt: Date.now() - 0.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    const summary = page.locator(".card").first();
    await expect(summary).toContainText("30m");
    await expect(summary).not.toContainText("2h 30m");

    await page.getByLabel("Period").selectOption("30days");
    await expect(summary).toContainText("2h 30m");

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
    await page.getByLabel("Period").selectOption("30days");
    await expect(page.getByText("2d vacation")).toBeVisible();
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
    await expect(page.locator(".card").first()).toContainText("1h 0");
  });
});
