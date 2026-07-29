import { expect, seedServer, test } from "./fixtures";
import { readFileSync } from "node:fs";
import path from "node:path";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

function isoDate(offsetDays: number): string {
  const date = new Date(Date.now() + offsetDays * DAY);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

test.describe("reports v3", () => {
  test("CSV export downloads current filter contents", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [
        { description: "csv work", startedAt: Date.now() - 3 * HOUR, stoppedAt: Date.now() - 2 * HOUR, projectID },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await expect(page.locator(".proj-item").filter({ hasText: "Backend" })).toBeVisible();

    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export CSV" }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("worktime-report.csv");

    const savedPath = path.join(test.info().outputDir, "report.csv");
    await download.saveAs(savedPath);
    const csv = readFileSync(savedPath, "utf8");
    expect(csv).toContain("Date,Project,Description,Start,Duration (min)");
    expect(csv).toContain('"csv work"');
    expect(csv).toContain("Backend");
    expect(csv).toContain(",60");
  });

  test("clicking a chart day filters the table, clicking again clears it", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Charted" }],
      entries: [
        { description: "today work", startedAt: Date.now() - 2 * HOUR, stoppedAt: Date.now() - 1 * HOUR, projectID },
        { description: "yesterday work", startedAt: Date.now() - DAY - 3 * HOUR, stoppedAt: Date.now() - DAY - 1 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "30 days" }).click();
    const table = page.locator("table");
    await expect(table).toContainText("Charted");
    await expect(table).toContainText("No project");

    // The last chart column is today: filtering by it drops yesterday's no-project group.
    await page.getByRole("button", { name: /^Filter by/ }).last().click();
    await expect(table).not.toContainText("No project");
    await expect(table).toContainText("Charted");

    // The amber day pill clears the filter.
    await expect(page.locator(".filterpill")).toBeVisible();
    await page.getByRole("button", { name: /^Filter by/ }).last().click();
    await expect(page.locator(".filterpill")).toHaveCount(0);
    await expect(table).toContainText("No project");
  });

  test("printable report route renders Russian report with real data", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Printable" }],
      entries: [
        { description: "printed work", startedAt: Date.now() - 5 * HOUR, stoppedAt: Date.now() - 3 * HOUR, projectID },
      ],
      timeOff: [{ kind: "dayoff", dateFrom: isoDate(-2), dateTo: isoDate(-2) }],
    });

    await page.goto(server.url + `/#/reports/print?from=${isoDate(-6)}&to=${isoDate(0)}`);
    await expect(page.getByText("отчёт по времени")).toBeVisible();
    await expect(page.getByText("printed work")).toBeVisible();
    await expect(page.getByText("Printable").first()).toBeVisible();
    await expect(page.getByText(/дей-офф/).first()).toBeVisible();
    await expect(page.getByText(/Среднее считается/)).toBeVisible();
  });
});
