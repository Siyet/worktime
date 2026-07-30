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
    // 9:00 local keeps the entry inside the default Month range even right after midnight.
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [{ description: "csv work", startedAt: todayNine, stoppedAt: todayNine + HOUR, projectID }],
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
    // Anchor entries to 9:00 local time so "today" stays today even when the
    // suite runs shortly after midnight (Date.now() - 2h would cross the date line).
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Charted" }],
      entries: [
        { description: "today work", startedAt: todayNine, stoppedAt: todayNine + HOUR, projectID },
        { description: "yesterday work", startedAt: todayNine - DAY, stoppedAt: todayNine - DAY + 2 * HOUR },
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

  test("overlaps-once option counts concurrent work a single time", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    // Two fully overlapping one-hour entries at 9:00 local: raw total is 2h,
    // overlap-aware total is 1h (30m attributed to each entry).
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Overlapped" }],
      entries: [
        { description: "task one", startedAt: todayNine, stoppedAt: todayNine + HOUR, projectID },
        { description: "task two", startedAt: todayNine, stoppedAt: todayNine + HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    // Assert the "total tracked" tile specifically: other KPI tiles (e.g. avg
    // per work day) could contain the same substring by coincidence.
    const totalStat = page.locator(".stat").first();
    await expect(totalStat).toContainText("2.0h");

    await page.getByLabel("Overlaps once").check();
    await expect(totalStat).toContainText("1.0h");
    // Each side of the overlap gets half the wall-clock hour.
    const projectRow = page.locator(".proj-item").filter({ hasText: "Overlapped" });
    await expect(projectRow).toContainText("30m");

    await page.getByLabel("Overlaps once").uncheck();
    await expect(totalStat).toContainText("2.0h");
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
