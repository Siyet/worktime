import { expect, pushBarrier, test, triggerSync } from "./fixtures";
import type { BrowserContext, Page } from "@playwright/test";

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

async function startTimer(page: Page, description: string) {
  await page.getByPlaceholder("What are you working on?").fill(description);
  await page.getByRole("button", { name: "Start" }).click();
}

async function startAndStop(page: Page, description: string) {
  await startTimer(page, description);
  await runningCard(page)
    .locator(".item")
    .filter({ hasText: description })
    .getByRole("button", { name: "Stop" })
    .click();
}

test.describe("multi-device conflicts (LWW)", () => {
  let contextB: BrowserContext;
  let pageB: Page;

  test.beforeEach(async ({ browser }) => {
    contextB = await browser.newContext();
    pageB = await contextB.newPage();
  });

  test.afterEach(async () => {
    await contextB.close();
  });

  test("rename conflict: the later offline edit wins on both devices", async ({ page, server, context }) => {
    // Device A creates the project; device B bootstraps it.
    await page.goto(server.url + "/#/projects");
    const created = pushBarrier(page, "Original");
    await page.getByPlaceholder("New project name").fill("Original");
    await page.getByRole("button", { name: "Add" }).click();
    await created;

    await pageB.goto(server.url + "/#/projects");
    const nameB = pageB.locator("input[aria-label='Name']");
    await expect(nameB).toHaveValue("Original");

    // Both go offline and rename; B's edit is strictly later.
    await context.setOffline(true);
    await contextB.setOffline(true);
    const nameA = page.locator("input[aria-label='Name']");
    await nameA.fill("From A");
    await nameA.blur();
    await pageB.waitForTimeout(50);
    await nameB.fill("From B");
    await nameB.blur();

    // A reconnects first, then B; B's newer updated_at wins server-side.
    const pushedA = pushBarrier(page, "From A");
    await context.setOffline(false);
    await pushedA;
    const pushedB = pushBarrier(pageB, "From B");
    await contextB.setOffline(false);
    await pushedB;

    await triggerSync(page);
    await expect(nameA).toHaveValue("From B");
    await expect(nameB).toHaveValue("From B");
  });

  test("entry deleted on device A disappears on device B", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    const created = pushBarrier(page, "doomed");
    await startAndStop(page, "doomed");
    await created;

    await pageB.goto(server.url + "/#/");
    const rowB = pageB.locator(".item").filter({ hasText: "doomed" });
    await expect(rowB).toBeVisible();

    const deleted = pushBarrier(page, "doomed");
    await page.locator(".item").filter({ hasText: "doomed" }).getByTitle("Delete entry").click();
    await deleted;

    await triggerSync(pageB);
    await expect(rowB).toHaveCount(0);
  });

  test("timer started on device A can be stopped from device B", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    const started = pushBarrier(page, "cross-device");
    await startTimer(page, "cross-device");
    await started;

    await pageB.goto(server.url + "/#/");
    const runningB = runningCard(pageB).locator(".item").filter({ hasText: "cross-device" });
    await expect(runningB).toBeVisible();

    const stopped = pushBarrier(pageB, "cross-device");
    await runningB.getByRole("button", { name: "Stop" }).click();
    await stopped;

    await triggerSync(page);
    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    await expect(page.locator(".item").filter({ hasText: "cross-device" })).toBeVisible();
  });

  test("echo of own pushes never duplicates rows", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    const pushed = pushBarrier(page, "echoed");
    await startAndStop(page, "echoed");
    await pushed;

    // Extra sync cycles re-deliver the row through the pull path.
    await triggerSync(page);
    await triggerSync(page);
    await page.waitForTimeout(300);
    await expect(page.locator(".item").filter({ hasText: "echoed" })).toHaveCount(1);
  });

  test("concurrent offline creates on both devices merge without loss", async ({ page, server, context }) => {
    await page.goto(server.url + "/#/");
    await pageB.goto(server.url + "/#/");

    await context.setOffline(true);
    await contextB.setOffline(true);
    await startAndStop(page, "made on A");
    await startAndStop(pageB, "made on B");

    const pushedA = pushBarrier(page, "made on A");
    await context.setOffline(false);
    await pushedA;
    const pushedB = pushBarrier(pageB, "made on B");
    await contextB.setOffline(false);
    await pushedB;

    await triggerSync(page);
    await triggerSync(pageB);
    for (const target of [page, pageB]) {
      await expect(target.locator(".item").filter({ hasText: "made on A" })).toBeVisible();
      await expect(target.locator(".item").filter({ hasText: "made on B" })).toBeVisible();
    }
  });
});
