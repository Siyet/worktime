import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

test.describe("timers", () => {
  test("lifecycle: empty state, start, tick, stop lands in today's list", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    await expect(page.getByText("No entries yet. Start your first timer above.")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);

    await page.getByPlaceholder("What are you working on?").fill("Write e2e tests");
    await page.getByRole("button", { name: "Start" }).click();

    const card = runningCard(page);
    await expect(card).toBeVisible();
    const row = card.locator(".item").filter({ hasText: "Write e2e tests" });
    await expect(row.locator(".elapsed")).toHaveText(/^\d+:\d{2}:\d{2}$/);
    await expect(page.getByPlaceholder("What are you working on?")).toHaveValue("");
    await expect(page.getByText("No entries yet")).toHaveCount(0);

    await expect(row.locator(".elapsed")).not.toHaveText("0:00:00", { timeout: 5000 });
    await row.getByRole("button", { name: "Stop" }).click();

    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    // Compute today's heading inside the page so Node/browser locale differences don't matter.
    const expectedDay = await page.evaluate(() =>
      new Date().toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }),
    );
    const dayCard = page.locator(".card").filter({ hasText: expectedDay });
    await expect(dayCard).toBeVisible();
    const finished = dayCard.locator(".item").filter({ hasText: "Write e2e tests" });
    await expect(finished.locator("span.muted.mono")).toContainText("-");
    await expect(finished.getByTitle("Delete entry")).toBeVisible();
  });

  test("two concurrent timers tick independently, stopping one leaves the other", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("alpha");
    await page.getByRole("button", { name: "Start" }).click();
    await input.fill("beta");
    await page.getByRole("button", { name: "Start" }).click();

    const card = runningCard(page);
    await expect(card.locator(".item")).toHaveCount(2);
    const rowAlpha = card.locator(".item").filter({ hasText: "alpha" });
    const rowBeta = card.locator(".item").filter({ hasText: "beta" });

    await expect(rowAlpha.locator(".elapsed")).toBeVisible();
    await expect(rowBeta.locator(".elapsed")).toBeVisible();
    const alphaBefore = await rowAlpha.locator(".elapsed").textContent();
    await expect(rowAlpha.locator(".elapsed")).not.toHaveText(alphaBefore!, { timeout: 10_000 });
    const betaBefore = await rowBeta.locator(".elapsed").textContent();
    await expect(rowBeta.locator(".elapsed")).not.toHaveText(betaBefore!, { timeout: 10_000 });

    await rowBeta.getByRole("button", { name: "Stop" }).click();
    await expect(card.locator(".item")).toHaveCount(1);
    await expect(card).toContainText("alpha");
    await expect(card).not.toContainText("beta");

    const finishedBeta = page
      .locator(".card")
      .filter({ hasNot: page.getByRole("heading", { name: "Running" }) })
      .filter({ hasText: "beta" });
    await expect(finishedBeta).toHaveCount(1);

    const alphaAfter = await rowAlpha.locator(".elapsed").textContent();
    await expect(rowAlpha.locator(".elapsed")).not.toHaveText(alphaAfter!, { timeout: 5000 });
  });

  test("elapsed is exact under a mocked clock", async ({ page, server }) => {
    await page.clock.install();
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("clocked");
    // Freeze time so started_at is deterministic, then advance tick by tick.
    await page.clock.pauseAt(new Date(Date.now() + 60_000));
    await page.getByRole("button", { name: "Start" }).click();
    const row = runningCard(page).locator(".item").filter({ hasText: "clocked" });
    await expect(row).toBeVisible();

    await page.clock.runFor("01:01");
    await expect(row.locator(".elapsed")).toHaveText("0:01:01");
    await page.clock.runFor("01:00:00");
    await expect(row.locator(".elapsed")).toHaveText("1:01:01");
  });

  test("description and project are attached to running and finished entries", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await page.getByPlaceholder("New project name").fill("Backend");
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.locator("input[aria-label='Name']")).toHaveValue("Backend");

    await page.goto(server.url + "/#/");
    const projectSelect = page.getByLabel("Project", { exact: true });
    await projectSelect.click();
    await page.getByRole("option", { name: "Backend" }).click();
    await expect(projectSelect).toContainText("Backend");
    await page.getByPlaceholder("What are you working on?").fill("API work");
    await page.getByRole("button", { name: "Start" }).click();

    const row = runningCard(page).locator(".item").filter({ hasText: "API work" });
    await expect(row).toContainText("Backend");
    await row.getByRole("button", { name: "Stop" }).click();

    const finished = page.locator(".item").filter({ hasText: "API work" });
    await expect(finished).toContainText("Backend");
  });

  test("deleting the only finished entry restores the empty state", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("to be deleted");
    await page.getByRole("button", { name: "Start" }).click();
    const row = runningCard(page).locator(".item").filter({ hasText: "to be deleted" });
    await row.getByRole("button", { name: "Stop" }).click();

    const finished = page.locator(".item").filter({ hasText: "to be deleted" });
    await expect(finished).toBeVisible();
    await finished.getByTitle("Delete entry").click();

    await expect(page.getByText("to be deleted")).toHaveCount(0);
    await expect(page.getByText("No entries yet. Start your first timer above.")).toBeVisible();
  });

  test("running timer survives a page reload and keeps ticking", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("survivor");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();

    await page.reload();
    const row = runningCard(page).locator(".item").filter({ hasText: "survivor" });
    await expect(row).toBeVisible();
    const before = await row.locator(".elapsed").textContent();
    await expect(row.locator(".elapsed")).not.toHaveText(before!, { timeout: 5000 });
  });
});
