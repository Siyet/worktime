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
    expect(csv).toContain("Date,Project,Description,Tags,Start,Duration (min)");
    expect(csv).toContain('"csv work"');
    expect(csv).toContain('"(untagged)"');
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

  test("switching Group by never changes the report total", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Tagged" }],
      entries: [
        { description: "dual", startedAt: todayNine, stoppedAt: todayNine + HOUR, projectID, tags: ["development", "review"] },
        { description: "solo", startedAt: todayNine + HOUR, stoppedAt: todayNine + 2 * HOUR, tags: ["development"] },
        { description: "plain", startedAt: todayNine + 2 * HOUR, stoppedAt: todayNine + 2.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    const reportCard = page.locator(".card").filter({ has: page.locator("tbody") });
    const total = reportCard.locator(".row .muted.mono");
    await expect(total).toHaveText("2h 30m");

    for (const grouping of ["Tag", "Day", "Description", "Project"]) {
      await page.getByRole("button", { name: grouping, exact: true }).click();
      await expect(total).toHaveText("2h 30m");
      await expect(reportCard.locator("tr.total")).toContainText("2h 30m");
    }
  });

  test("a two-tag entry shows a 1/2 share in the detail rows that sums to the group header", async ({ page, server }) => {
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      entries: [
        { description: "dual", startedAt: todayNine, stoppedAt: todayNine + HOUR, tags: ["development", "review"] },
        { description: "solo", startedAt: todayNine + HOUR, stoppedAt: todayNine + 2 * HOUR, tags: ["development"] },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "Tag", exact: true }).click();
    await page.getByLabel("Show individual entries").check();

    // development = 60/2 + 60 = 1h 30m; review = 30m; the dual entry's detail
    // row carries its half share with the 1/k marker.
    const developmentGroup = page.locator("tr.group").filter({ hasText: "development" });
    await expect(developmentGroup).toContainText("1h 30m");
    await expect(page.locator("tr.group").filter({ hasText: "review" })).toContainText("30m");
    const dualDetail = page.locator("tr.entry").filter({ hasText: "dual" }).first();
    await expect(dualDetail).toContainText("30m");
    await expect(dualDetail.locator(".splitmark")).toHaveText("1/2");
  });

  test("the untagged bucket filters and never falls out of the totals", async ({ page, server }) => {
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      entries: [
        { description: "tagged work", startedAt: todayNine, stoppedAt: todayNine + HOUR, tags: ["development"] },
        { description: "plain work", startedAt: todayNine + HOUR, stoppedAt: todayNine + 1.5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/reports");
    await page.getByRole("button", { name: "Tag", exact: true }).click();
    const reportCard = page.locator(".card").filter({ has: page.locator("tbody") });
    await expect(reportCard.locator("tr.group").filter({ hasText: "untagged" })).toBeVisible();
    await expect(reportCard.locator(".row .muted.mono")).toHaveText("1h 30m");

    // Switching the development chip off leaves only the untagged entry, and
    // the untagged bucket still carries it in every total.
    await page.getByRole("button", { name: "development", exact: true }).click();
    await expect(reportCard.locator(".row .muted.mono")).toHaveText("30m");
    await expect(reportCard.locator("tr.group").filter({ hasText: "untagged" })).toContainText("30m");
    await expect(page.locator(".stat").first()).toContainText("0.5h");
  });

  test("rounding is locked while overlaps-once is on", async ({ page, server }) => {
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      entries: [{ description: "some work", startedAt: todayNine, stoppedAt: todayNine + HOUR }],
    });

    await page.goto(server.url + "/#/reports");
    const roundingButton = page.getByRole("button", { name: "15m", exact: true });
    await expect(roundingButton).toBeEnabled();

    await page.getByLabel("Overlaps once").check();
    await expect(roundingButton).toBeDisabled();
    await expect(page.getByText(/Rounding is off while overlaps/)).toBeVisible();

    await page.getByLabel("Overlaps once").uncheck();
    await expect(roundingButton).toBeEnabled();
  });

  test("printable report renders По тегам with Итого, and skips it for an untagged range", async ({ page, server }) => {
    const todayNine = new Date().setHours(9, 0, 0, 0);
    const oldDay = todayNine - 10 * DAY;
    await seedServer(server.url, {
      entries: [
        { description: "tagged print", startedAt: todayNine, stoppedAt: todayNine + HOUR, tags: ["development", "review"] },
        { description: "plain print", startedAt: todayNine + HOUR, stoppedAt: todayNine + 2 * HOUR },
        { description: "old plain", startedAt: oldDay, stoppedAt: oldDay + HOUR },
      ],
    });

    await page.goto(server.url + `/#/reports/print?from=${isoDate(0)}&to=${isoDate(0)}`);
    await expect(page.getByRole("heading", { name: "По тегам" })).toBeVisible();
    await expect(page.getByText("без тега")).toBeVisible();
    await expect(page.getByText("development").first()).toBeVisible();
    // Итого appears on both tables and their displayed values are apportioned
    // against the same 2h total.
    await expect(page.locator("tr.sumrow")).toHaveCount(2);
    await expect(page.getByText(/делит своё время/)).toBeVisible();

    // A range with no tagged entries must not grow an empty section.
    await page.goto(server.url + `/#/reports/print?from=${isoDate(-10)}&to=${isoDate(-10)}`);
    await expect(page.getByText("old plain")).toBeVisible();
    await expect(page.getByRole("heading", { name: "По тегам" })).toHaveCount(0);
    await expect(page.locator("tr.sumrow")).toHaveCount(1);
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
