import { expect, seedServer, test } from "./fixtures";
import { statSync } from "node:fs";
import path from "node:path";

test.describe("sqlite export", () => {
  test("the Settings button downloads a non-empty .sqlite file", async ({ page, server }) => {
    const todayNine = new Date().setHours(9, 0, 0, 0);
    await seedServer(server.url, {
      entries: [{ description: "exported work", startedAt: todayNine, stoppedAt: todayNine + 3_600_000 }],
    });

    await page.goto(server.url + "/#/settings");
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("link", { name: "Download .sqlite" }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("worktime-export.sqlite");

    const savedPath = path.join(test.info().outputDir, "export.sqlite");
    await download.saveAs(savedPath);
    expect(statSync(savedPath).size).toBeGreaterThan(0);
  });

  test("Settings shows build identity and keeps update controls read-only without an admin allowlist", async ({ page, server }) => {
    await page.goto(server.url + "/#/settings");

    const updates = page.getByRole("heading", { name: "Version and updates" }).locator("..");
    await expect(updates).toContainText("Current version");
    await expect(updates).toContainText("dev");
    await expect(updates).toContainText("Only an administrator can manage updates.");
    await expect(updates.getByRole("button", { name: "Check now" })).toBeDisabled();
  });
});
