import { expect, test } from "./fixtures";

function updateStatus(state: "available" | "applying", currentVersion: string) {
  return {
    state,
    current_version: currentVersion,
    latest_version: "v1.1.0",
    update_available: true,
    apply_ready: state === "available",
    checked_at: Date.now(),
    changelog_url: "https://github.com/Siyet/worktime/releases/tag/v1.1.0",
    auto_apply: false,
    can_manage: true,
    apply_mode: "automatic",
    message: null,
  };
}

test.describe("instance restart updates", () => {
  test("manual apply polls through an outage and reloads only after the target version", async ({ page, server }) => {
    let applying = false;
    let returnedOutage = false;
    let versionCallsAfterApply = 0;
    await page.route("**/api/system/version", async (route) => {
      if (applying) versionCallsAfterApply++;
      await route.fulfill({ json: { version: applying ? "v1.1.0" : "dev" } });
    });
    await page.route("**/api/system/update", (route) => route.fulfill({ json: updateStatus(applying ? "applying" : "available", applying ? "v1.1.0" : "dev") }));
    await page.route("**/api/system/update/apply", async (route) => {
      applying = true;
      await route.fulfill({ status: 202, json: updateStatus("applying", "dev") });
    });
    await page.route("**/healthz", async (route) => {
      if (applying && !returnedOutage) {
        returnedOutage = true;
        await route.fulfill({ status: 503, body: "maintenance" });
        return;
      }
      await route.fulfill({ status: 200, body: "ok" });
    });
    page.on("dialog", (dialog) => void dialog.accept());

    await page.goto(server.url + "/#/settings");
    await expect(page.getByRole("button", { name: "Install update" })).toBeEnabled();
    const reloaded = page.waitForEvent("load");
    await page.getByRole("button", { name: "Install update" }).click();
    await reloaded;

    expect(returnedOutage).toBe(true);
    expect(versionCallsAfterApply).toBeGreaterThan(0);
    await expect(page.getByRole("heading", { name: "Version and updates" })).toBeVisible();
  });

  test("automatic replacement is detected by version polling and reloads Settings", async ({ page, server }) => {
    let updated = false;
    await page.route("**/api/system/version", (route) => route.fulfill({ json: { version: updated ? "v1.1.0" : "dev" } }));
    await page.route("**/api/system/update", (route) => route.fulfill({ json: updateStatus("available", updated ? "v1.1.0" : "dev") }));
    await page.route("**/healthz", (route) => route.fulfill({ status: 200, body: "ok" }));

    await page.goto(server.url + "/#/settings");
    await expect(page.getByText("dev", { exact: true }).first()).toBeVisible();
    const reloaded = page.waitForEvent("load");
    updated = true;
    await reloaded;

    await expect(page.getByRole("heading", { name: "Version and updates" })).toBeVisible();
  });

  test("persisted offline discovery stays display-only until a fresh check", async ({ page, server }) => {
    let fresh = false;
    let autoApply = false;
    await page.route("**/api/system/version", (route) => route.fulfill({ json: { version: "dev" } }));
    await page.route("**/api/system/update", (route) => route.fulfill({
      json: { ...updateStatus("available", "dev"), auto_apply: autoApply, apply_ready: fresh },
    }));
    await page.route("**/api/system/update/policy", async (route) => {
      autoApply = true;
      await route.fulfill({
        json: { ...updateStatus("available", "dev"), auto_apply: true, apply_ready: false },
      });
    });
    await page.route("**/api/system/update/check", async (route) => {
      fresh = true;
      await route.fulfill({ json: { ...updateStatus("available", "dev"), apply_ready: true } });
    });
    await page.route("**/healthz", (route) => route.fulfill({ status: 200, body: "ok" }));

    await page.goto(server.url + "/#/settings");
    await expect(page.getByRole("button", { name: "Install update" })).toBeDisabled();
    await page.getByRole("checkbox", { name: "Install updates automatically" }).check();
    await expect(page.getByRole("checkbox", { name: "Install updates automatically" })).toBeChecked();
    await expect(page.getByRole("button", { name: "Install update" })).toBeDisabled();
    await page.getByRole("button", { name: "Check now" }).click();
    await expect(page.getByRole("button", { name: "Install update" })).toBeEnabled();
  });

  test("a failed attempt can be freshly checked, retried, and then reloads", async ({ page, server }) => {
    let currentVersion = "dev";
    let state: "available" | "applying" | "failed" | "up_to_date" = "available";
    let attempts = 0;
    const status = () => ({
      ...updateStatus(state === "up_to_date" ? "available" : state === "failed" ? "available" : state, currentVersion),
      state,
      update_available: currentVersion === "dev",
      apply_ready: state === "available",
      message: state === "failed" ? "preflight failed" : null,
    });
    await page.route("**/api/system/version", (route) => route.fulfill({ json: { version: currentVersion } }));
    await page.route("**/api/system/update", (route) => route.fulfill({ json: status() }));
    await page.route("**/api/system/update/check", async (route) => {
      state = "available";
      await route.fulfill({ json: status() });
    });
    await page.route("**/api/system/update/apply", async (route) => {
      attempts++;
      const applying = { ...status(), state: "applying", apply_ready: false };
      if (attempts === 1) {
        state = "failed";
      } else {
        currentVersion = "v1.1.0";
        state = "up_to_date";
      }
      await route.fulfill({ status: 202, json: applying });
    });
    await page.route("**/healthz", (route) => route.fulfill({ status: 200, body: "ok" }));
    page.on("dialog", (dialog) => void dialog.accept());

    await page.goto(server.url + "/#/settings");
    await page.getByRole("button", { name: "Install update" }).click();
    await expect(page.getByRole("alert")).toContainText("update request failed");
    await page.getByRole("button", { name: "Check now" }).click();
    await expect(page.getByRole("button", { name: "Install update" })).toBeEnabled();
    const reloaded = page.waitForEvent("load");
    await page.getByRole("button", { name: "Install update" }).click();
    await reloaded;

    expect(attempts).toBe(2);
    await expect(page.getByRole("heading", { name: "Version and updates" })).toBeVisible();
  });
});
