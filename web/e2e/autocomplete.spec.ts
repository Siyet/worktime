import { expect, seedServer, test } from "./fixtures";
import type { Page } from "@playwright/test";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

function dayNine(offsetDays = 0): number {
  return new Date().setHours(9, 0, 0, 0) + offsetDays * DAY;
}

function suggestions(page: Page) {
  return page.getByRole("listbox", { name: "Recent tasks" });
}

async function seedHistory(serverURL: string): Promise<string> {
  const projectID = crypto.randomUUID();
  const base = dayNine(-1);
  await seedServer(serverURL, {
    projects: [{ id: projectID, name: "Backend" }],
    entries: [
      {
        description: "Write e2e tests",
        startedAt: base,
        stoppedAt: base + HOUR,
        projectID,
        tags: ["development"],
      },
      { description: "Write docs", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      { description: "Rewrite the sync engine", startedAt: base + 4 * HOUR, stoppedAt: base + 5 * HOUR },
      // Older than the window the page shows, so it must never be suggested.
      { description: "Ancient ritual", startedAt: dayNine(-30), stoppedAt: dayNine(-30) + HOUR },
    ],
  });
  return projectID;
}

test.describe("description suggestions", () => {
  test("the list opens on typing, never on a bare focus", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");

    await input.click();
    await expect(suggestions(page)).toHaveCount(0);

    await input.fill("write");
    await expect(suggestions(page).getByRole("option")).toHaveCount(3);
    // Prefix matches first (most recent of them on top), the substring match last.
    await expect(suggestions(page).getByRole("option").nth(0)).toContainText("Write docs");
    await expect(suggestions(page).getByRole("option").nth(1)).toContainText("Write e2e tests");
    await expect(suggestions(page).getByRole("option").last()).toContainText("Rewrite the sync engine");
    await expect(suggestions(page)).not.toContainText("Ancient ritual");
  });

  test("keyboard picking fills the description, project and tags", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write e2e");
    await page.keyboard.press("ArrowDown");
    await expect(suggestions(page).getByRole("option").first()).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Enter");

    await expect(input).toHaveValue("Write e2e tests");
    await expect(suggestions(page)).toHaveCount(0);
    await expect(page.getByLabel("Project", { exact: true })).toContainText("Backend");
    await expect(page.getByRole("button", { name: "Tags", exact: true })).toContainText("development");
  });

  test("Enter without an active suggestion still starts the timer", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write");
    await expect(suggestions(page)).toBeVisible();
    await input.press("Enter");

    const running = page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
    await expect(running.locator(".item").filter({ hasText: "write" })).toBeVisible();
  });

  test("a suggestion can be picked with the mouse", async ({ page, server }) => {
    // A regression guard: closing the list on blur would swallow this click.
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("docs");
    await suggestions(page).getByRole("option", { name: /Write docs/ }).click();
    await expect(input).toHaveValue("Write docs");
  });

  test("a project chosen by hand is not overwritten by a suggestion", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/projects");
    await page.getByPlaceholder("New project name").fill("Frontend");
    await page.getByRole("button", { name: "Add" }).click();

    await page.goto(server.url + "/#/");
    const projectSelect = page.getByLabel("Project", { exact: true });
    await projectSelect.click();
    await page.getByRole("option", { name: "Frontend" }).click();
    await expect(projectSelect).toContainText("Frontend");

    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write e2e");
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Enter");
    await expect(input).toHaveValue("Write e2e tests");
    await expect(projectSelect).toContainText("Frontend");
  });

  test("Escape closes the list and keeps the typed text", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write");
    await expect(suggestions(page)).toBeVisible();
    await input.press("Escape");
    await expect(suggestions(page)).toHaveCount(0);
    await expect(input).toHaveValue("write");
  });

  test("Escape with the list open does not close the entry editor", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    const entry = page.locator(".item").filter({ hasText: "Write docs" });
    await entry.locator(".desc").click();
    await expect(page.locator("dialog")).toBeVisible();

    const field = page.locator("#ed-desc");
    await field.fill("write");
    await expect(suggestions(page)).toBeVisible();
    await field.press("Escape");
    await expect(suggestions(page)).toHaveCount(0);
    await expect(page.locator("dialog")).toBeVisible();

    // A second Escape, with the list already closed, closes the dialog as usual.
    await field.press("Escape");
    await expect(page.locator("dialog")).toHaveCount(0);
  });
});
