import { expect, pushBarrier, seedServer, test } from "./fixtures";
import type { Page } from "@playwright/test";

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

function entryRow(page: Page, description: string) {
  return page.locator(".item").filter({ hasText: description });
}

function editorDialog(page: Page) {
  return page.locator("dialog.sheet");
}

/** Today at HH:MM local time, as unix ms. */
function todayAt(hour: number, minute: number): number {
  const date = new Date();
  date.setHours(hour, minute, 0, 0);
  return date.getTime();
}

test.describe("entry editing", () => {
  test("kebab opens the menu; Escape and outside click close it", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "menu target", startedAt: todayAt(9, 0), stoppedAt: todayAt(10, 0) }] });
    await page.goto(server.url + "/#/");

    const row = entryRow(page, "menu target");
    const kebab = row.getByRole("button", { name: "Entry actions" });
    await kebab.click();
    await expect(page.getByRole("menu")).toBeVisible();
    await expect(kebab).toHaveAttribute("aria-expanded", "true");

    await page.keyboard.press("Escape");
    await expect(page.getByRole("menu")).toHaveCount(0);

    await kebab.click();
    await expect(page.getByRole("menu")).toBeVisible();
    await page.getByPlaceholder("What are you working on?").click();
    await expect(page.getByRole("menu")).toHaveCount(0);
  });

  test("Edit opens the dialog and a description change reaches the server", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "old words", startedAt: todayAt(9, 0), stoppedAt: todayAt(10, 0) }] });
    await page.goto(server.url + "/#/");

    const row = entryRow(page, "old words");
    await row.getByRole("button", { name: "Entry actions" }).click();
    await page.getByRole("menuitem", { name: "Edit" }).click();

    const dialog = editorDialog(page);
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("Description").fill("new words");

    const pushed = pushBarrier(page, "new words");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;

    await expect(dialog).toHaveCount(0);
    await expect(entryRow(page, "new words")).toBeVisible();
    await expect(page.locator(".item").filter({ hasText: "old words" })).toHaveCount(0);
  });

  test("930 in From normalises to 09:30 and changes the duration", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "normalise me", startedAt: todayAt(10, 0), stoppedAt: todayAt(11, 0) }] });
    await page.goto(server.url + "/#/");

    await entryRow(page, "normalise me").locator(".desc").click();
    const dialog = editorDialog(page);
    await expect(dialog.locator(".ed-head .ed-calc")).toHaveText("1h 00m");

    const from = dialog.locator("#ed-from");
    await from.fill("930");
    await from.blur();
    await expect(from).toHaveValue("09:30");
    await expect(dialog.locator(".ed-head .ed-calc")).toHaveText("1h 30m");
  });

  test("unparseable time blocks Save and sets aria-invalid", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "garbage time", startedAt: todayAt(10, 0), stoppedAt: todayAt(11, 0) }] });
    await page.goto(server.url + "/#/");

    await entryRow(page, "garbage time").locator(".desc").click();
    const dialog = editorDialog(page);
    const from = dialog.locator("#ed-from");
    await from.fill("abc");
    await expect(from).toHaveAttribute("aria-invalid", "true");
    await expect(dialog.locator(".ed-head .ed-calc")).toHaveText("—");
    await expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  test("typing an end time stops a running entry retroactively", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("forgot to stop");
    await page.getByRole("button", { name: "Start" }).click();

    const runningRow = runningCard(page).locator(".item").filter({ hasText: "forgot to stop" });
    await expect(runningRow).toBeVisible();
    await runningRow.locator(".desc").click();

    const dialog = editorDialog(page);
    const to = dialog.locator("#ed-to");
    await expect(to).toHaveAttribute("placeholder", "running");
    // A retroactive stop at the start minute: always >= started_at, so Save is valid.
    const startValue = await dialog.locator("#ed-from").inputValue();
    await to.fill(startValue);
    await to.blur();

    const pushed = pushBarrier(page, "forgot to stop");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;

    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    await expect(entryRow(page, "forgot to stop")).toBeVisible();
  });

  test("a midnight-crossing entry saves with the auto-bumped +1d offset", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "night shift", startedAt: todayAt(10, 0), stoppedAt: todayAt(11, 0) }] });
    await page.goto(server.url + "/#/");

    await entryRow(page, "night shift").locator(".desc").click();
    const dialog = editorDialog(page);
    const from = dialog.locator("#ed-from");
    const to = dialog.locator("#ed-to");
    await from.fill("23:40");
    await from.blur();
    await to.fill("00:20");
    await to.blur();

    // The end typed before the start bumped the end-day offset automatically.
    await expect(dialog.locator(".seg").last()).toContainText("+1d");
    await expect(dialog.locator(".ed-head .ed-calc")).toHaveText("40m");

    // pushBarrier only matches an OK response: a server 400 on
    // stopped_at < started_at would time this out.
    const pushed = pushBarrier(page, "night shift");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;

    await expect(entryRow(page, "night shift").locator(".dur")).toHaveText("40m");
  });

  test("delete shows the undo toast and Undo restores the entry on the server", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "come back", startedAt: todayAt(9, 0), stoppedAt: todayAt(10, 0) }] });
    await page.goto(server.url + "/#/");

    const row = entryRow(page, "come back");
    await expect(row).toBeVisible();

    const tombstoned = pushBarrier(page, "come back");
    await row.getByRole("button", { name: "Entry actions" }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await tombstoned;

    await expect(row).toHaveCount(0);
    const toast = page.getByRole("status");
    await expect(toast).toContainText("Deleted");
    await expect(toast).toContainText("come back");

    const restored = pushBarrier(page, "come back");
    await toast.getByRole("button", { name: "Undo" }).click();
    await restored;

    await expect(entryRow(page, "come back")).toBeVisible();
    await expect(page.getByRole("status")).toHaveCount(0);
  });

  test("tags picked in the editor appear as a chip on the row", async ({ page, server }) => {
    await seedServer(server.url, { entries: [{ description: "tag me", startedAt: todayAt(9, 0), stoppedAt: todayAt(10, 0) }] });
    await page.goto(server.url + "/#/");

    await entryRow(page, "tag me").locator(".desc").click();
    const dialog = editorDialog(page);
    await dialog.locator(".tagpick").getByRole("button", { name: "development", exact: true }).click();
    await dialog.getByLabel("Tags").fill("focus");
    await dialog.getByRole("button", { name: "Create tag focus" }).click();

    const pushed = pushBarrier(page, '"development","focus"');
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;

    // One chip plus the +N overflow: tags are stored sorted, development first.
    const chips = entryRow(page, "tag me").locator(".tags");
    await expect(chips.locator(".tag").first()).toHaveText("development");
    await expect(chips.locator(".tag").nth(1)).toHaveText("+1");
  });
});
