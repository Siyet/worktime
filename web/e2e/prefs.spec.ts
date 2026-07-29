import { expect, seedServer, test } from "./fixtures";

test.describe("preferences", () => {
  test("language switch translates the UI and survives a reload", async ({ page, server }) => {
    await page.goto(server.url + "/#/settings");
    await expect(page.getByLabel("Language")).toBeVisible();

    await page.getByLabel("Language").selectOption("ru");
    await expect(page.getByRole("link", { name: "Таймер" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Отчёты" })).toBeVisible();

    // The preference is device-local and must survive a reload.
    await page.reload();
    await expect(page.getByRole("link", { name: "Таймер" })).toBeVisible();

    await page.getByRole("link", { name: "Таймер" }).click();
    await expect(page.getByPlaceholder("Над чем работаешь?")).toBeVisible();

    // Back to English via the same select (now labeled in Russian).
    await page.getByRole("link", { name: "Настройки" }).click();
    await page.getByLabel("Язык").selectOption("en");
    await expect(page.getByRole("link", { name: "Timer" })).toBeVisible();
  });

  test("date format switches time off dates between styles", async ({ page, server }) => {
    await seedServer(server.url, {
      timeOff: [{ kind: "vacation", dateFrom: "2026-07-01", dateTo: "2026-07-03" }],
    });

    await page.goto(server.url + "/#/settings");
    await page.getByLabel("Date format").selectOption("dmy");
    await page.goto(server.url + "/#/timeoff");
    const row = page.locator(".item").filter({ hasText: "Vacation" });
    await expect(row).toContainText("01.07.2026 - 03.07.2026");

    await page.goto(server.url + "/#/settings");
    await page.getByLabel("Date format").selectOption("ymd");
    await page.goto(server.url + "/#/timeoff");
    await expect(row).toContainText("2026-07-01 - 2026-07-03");
  });

  test("time format switches finished entries between 24h and AM/PM", async ({ page, server }) => {
    // A finished entry today at 15:00-15:30 local time.
    const start = new Date();
    start.setHours(15, 0, 0, 0);
    await seedServer(server.url, {
      entries: [{ description: "afternoon work", startedAt: start.getTime(), stoppedAt: start.getTime() + 1_800_000 }],
    });

    await page.goto(server.url + "/#/settings");
    await page.getByLabel("Time format").selectOption("24");
    await page.goto(server.url + "/#/");
    const row = page.locator(".item").filter({ hasText: "afternoon work" });
    await expect(row).toContainText("15:00");
    await expect(row).not.toContainText("PM");

    await page.goto(server.url + "/#/settings");
    await page.getByLabel("Time format").selectOption("12");
    await page.goto(server.url + "/#/");
    await expect(row).toContainText("3:00");
    await expect(row).toContainText("PM");
  });
});
