import { expect, test } from "./fixtures";

test.describe("auth and persistence", () => {
  test("without dev auth the sign-in screen renders", async ({ page, serverNoAuth }) => {
    await page.goto(serverNoAuth.url + "/#/");
    await expect(page.getByRole("heading", { name: "Sign in to WorkTime" })).toBeVisible();
    await expect(page.getByText("Google sign-in is not configured")).toBeVisible();
  });

  test("with dev auth the app loads and /api/me returns the dev user", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    await expect(page.getByPlaceholder("What are you working on?")).toBeVisible();

    const me = await (await fetch(server.url + "/api/me")).json();
    expect(me.email).toBe("dev@worktime.local");
  });

  test("data survives reload from IndexedDB while the server is unreachable", async ({ browser, server }) => {
    // Service workers are blocked so route interception reliably sees /api requests.
    const context = await browser.newContext({ serviceWorkers: "block" });
    const page = await context.newPage();
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("persisted");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toBeVisible();

    await context.route("**/api/**", (route) => route.abort());
    await page.reload();
    await expect(page.locator(".item").filter({ hasText: "persisted" })).toBeVisible();

    await page.goto(server.url + "/#/settings");
    await expect(page.getByText("Server is unreachable - settings need a connection.")).toBeVisible();
    await context.close();
  });

  test("sign out wipes IndexedDB and lands on the sign-in screen", async ({ browser, server }) => {
    const context = await browser.newContext({ serviceWorkers: "block" });
    const page = await context.newPage();
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("mine");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toBeVisible();

    // Open Settings first (it needs live /api to render the user card at all).
    await page.goto(server.url + "/#/settings");
    await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();

    // After logout the dev-auth server would silently sign us back in, so stub
    // the API to behave like a real signed-out instance before clicking.
    await context.route("**/auth/logout", (route) => route.fulfill({ status: 204 }));
    await context.route("**/auth/config", (route) =>
      route.fulfill({ status: 200, contentType: "application/json", body: '{"google":false,"dev_auth":false}' }),
    );
    await context.route("**/api/**", (route) => route.fulfill({ status: 401, body: "unauthorized" }));

    await page.getByRole("button", { name: "Sign out" }).click();

    await expect(page.getByRole("heading", { name: "Sign in to WorkTime" })).toBeVisible({ timeout: 10_000 });
    // The app may recreate an empty database after the wipe; what matters is that no entries survived.
    const entryCount = await page.evaluate(
      async () =>
        new Promise<number>((resolve) => {
          const open = indexedDB.open("worktime");
          open.onsuccess = () => {
            const database = open.result;
            if (!database.objectStoreNames.contains("time_entries")) {
              database.close();
              resolve(0);
              return;
            }
            const request = database.transaction("time_entries", "readonly").objectStore("time_entries").count();
            request.onsuccess = () => {
              database.close();
              resolve(request.result);
            };
          };
        }),
    );
    expect(entryCount).toBe(0);
    await context.close();
  });

  test("API token lifecycle: plaintext once, Bearer works, revoke invalidates", async ({ page, server }) => {
    await page.goto(server.url + "/#/settings");
    await page.getByPlaceholder("Token name (e.g. claude-mcp)").fill("e2e token");
    await page.getByRole("button", { name: "Create" }).click();

    const tokenCode = page.locator(".fresh code");
    await expect(tokenCode).toBeVisible();
    const plaintext = (await tokenCode.textContent())!.trim();
    expect(plaintext).toMatch(/^wt_/);

    const authed = await fetch(server.url + "/api/me", { headers: { Authorization: `Bearer ${plaintext}` } });
    expect(authed.status).toBe(200);

    await page.getByRole("button", { name: "Revoke" }).click();
    await expect(page.locator(".item").filter({ hasText: "e2e token" })).toHaveCount(0);

    const revoked = await fetch(server.url + "/api/me", { headers: { Authorization: `Bearer ${plaintext}` } });
    expect(revoked.status).toBe(401);
  });

  test("another account on the same browser wipes the previous user's local data", async ({ browser, server }) => {
    const context = await browser.newContext({ serviceWorkers: "block" });
    const page = await context.newPage();
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("dev user data");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toBeVisible();

    // A different account signs in: /api/me returns another user id and the
    // server-side pull returns that user's (empty) data set.
    await context.route("**/api/me", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ id: "00000000-0000-7000-8000-000000000001", email: "other@test.local", name: "Other", picture_url: "" }),
      }),
    );
    await context.route("**/api/sync", (route) =>
      route.fulfill({ status: 200, contentType: "application/json", body: '{"seq":0,"changes":{}}' }),
    );

    await page.reload();
    await expect(page.getByText("No entries yet. Start your first timer above.")).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText("dev user data")).toHaveCount(0);
    await context.close();
  });
});
