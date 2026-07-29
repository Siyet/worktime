import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

async function addProject(page: Page, name: string) {
  await page.getByPlaceholder("New project name").fill(name);
  await page.getByRole("button", { name: "Add" }).click();
}

test.describe("projects", () => {
  test("created project shows in the list and in the Timer select", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await addProject(page, "Website");
    await expect(page.locator("input[aria-label='Name']")).toHaveValue("Website");

    await page.goto(server.url + "/#/");
    await page.getByLabel("Project", { exact: true }).click();
    await expect(page.getByRole("option", { name: "Website" })).toBeVisible();
  });

  test("rename persists across reload", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await addProject(page, "Alpha");
    const nameInput = page.locator("input[aria-label='Name']");
    await expect(nameInput).toHaveValue("Alpha");

    await nameInput.fill("Alpha Renamed");
    await nameInput.blur();
    // Persistence proof is the reload, not the input value (typed text alone would pass).
    await page.reload();
    await expect(page.locator("input[aria-label='Name']")).toHaveValue("Alpha Renamed");
  });

  test("archive hides project from the Timer select, unarchive restores it", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await addProject(page, "Seasonal");
    await page.getByRole("button", { name: "Archive" }).click();
    await expect(page.getByRole("button", { name: "Unarchive" })).toBeVisible();

    await page.goto(server.url + "/#/");
    await page.getByLabel("Project", { exact: true }).click();
    await expect(page.getByRole("option", { name: "No project" })).toBeVisible();
    await expect(page.getByRole("option", { name: "Seasonal" })).toHaveCount(0);

    await page.goto(server.url + "/#/projects");
    await page.getByRole("button", { name: "Unarchive" }).click();
    await expect(page.getByRole("button", { name: "Archive" })).toBeVisible();
    await page.goto(server.url + "/#/");
    await page.getByLabel("Project", { exact: true }).click();
    await expect(page.getByRole("option", { name: "Seasonal" })).toBeVisible();
  });

  test("deleting a project keeps its time entries but strips the label", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await addProject(page, "Doomed");

    await page.goto(server.url + "/#/");
    await page.getByLabel("Project", { exact: true }).click();
    await page.getByRole("option", { name: "Doomed" }).click();
    await page.getByPlaceholder("What are you working on?").fill("labeled work");
    await page.getByRole("button", { name: "Start" }).click();
    const row = page.locator(".item").filter({ hasText: "labeled work" });
    await row.getByRole("button", { name: "Stop" }).click();
    await expect(row).toContainText("Doomed");

    await page.goto(server.url + "/#/projects");
    await page.getByTitle("Delete project").click();
    await expect(page.getByText("No projects yet.")).toBeVisible();

    await page.goto(server.url + "/#/");
    await expect(row).toBeVisible();
    await expect(row).not.toContainText("Doomed");
  });

  test("blank project name is not created", async ({ page, server }) => {
    await page.goto(server.url + "/#/projects");
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.getByText("No projects yet.")).toBeVisible();

    // Positive anchor proving creation works right after the rejected attempt.
    await addProject(page, "Real");
    await expect(page.locator("input[aria-label='Name']")).toHaveValue("Real");
    await expect(page.locator("input[aria-label='Name']")).toHaveCount(1);
  });
});

test.describe("time off", () => {
  test("vacation today: banner on Timer, timers still start, delete removes both", async ({ page, server }) => {
    await page.goto(server.url + "/#/timeoff");
    // Date inputs default to today; kind defaults to Vacation.
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.locator(".item").filter({ hasText: "Vacation" })).toBeVisible();

    await page.goto(server.url + "/#/");
    await expect(page.getByText(/Today is marked as vacation - timers still work/)).toBeVisible();

    await page.getByPlaceholder("What are you working on?").fill("working on vacation");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toBeVisible();

    await page.goto(server.url + "/#/timeoff");
    await page.getByTitle("Delete").click();
    await expect(page.getByText("No sick leaves or vacations recorded.")).toBeVisible();
    await page.goto(server.url + "/#/");
    await expect(page.getByText(/Today is marked as/)).toHaveCount(0);
  });

  test("validation and sick banner: bad range disables Add; sick range covering today shows sick banner", async ({
    page,
    server,
  }) => {
    await page.goto(server.url + "/#/timeoff");
    const isoDate = (offsetDays: number) => {
      const date = new Date(Date.now() + offsetDays * 86_400_000);
      return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    };

    // End before start: Add disabled with a message.
    await page.getByLabel("From").fill(isoDate(0));
    await page.getByLabel("To").fill(isoDate(-2));
    await expect(page.getByRole("button", { name: "Add" })).toBeDisabled();
    await expect(page.getByText("End date is before start date.")).toBeVisible();

    // Valid sick range NOT covering today: recorded, but no banner.
    await page.getByLabel("Kind").selectOption("Sick leave");
    await page.getByLabel("From").fill(isoDate(-5));
    await page.getByLabel("To").fill(isoDate(-3));
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.locator(".item").filter({ hasText: "Sick" })).toBeVisible();
    await page.goto(server.url + "/#/");
    await expect(page.getByText(/Today is marked as/)).toHaveCount(0);

    // Sick range covering today: sick-leave banner text.
    await page.goto(server.url + "/#/timeoff");
    await page.getByLabel("Kind").selectOption("Sick leave");
    await page.getByLabel("From").fill(isoDate(-1));
    await page.getByLabel("To").fill(isoDate(1));
    await page.getByRole("button", { name: "Add" }).click();
    await page.goto(server.url + "/#/");
    await expect(page.getByText(/Today is marked as sick leave - timers still work/)).toBeVisible();
  });

  test("day off: badge in the list, banner on Timer, counted in the report", async ({ page, server }) => {
    await page.goto(server.url + "/#/timeoff");
    await page.getByLabel("Kind").selectOption("Day off");
    // Date inputs default to today.
    await page.getByRole("button", { name: "Add" }).click();
    await expect(page.locator(".item").filter({ hasText: "Day off" })).toBeVisible();

    await page.goto(server.url + "/#/");
    await expect(page.getByText(/Today is marked as a day off - timers still work/)).toBeVisible();

    await page.goto(server.url + "/#/reports");
    await expect(page.locator(".stats")).toContainText("1 dayoff");
  });
});
