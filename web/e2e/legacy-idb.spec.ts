import { expect, test } from "./fixtures";

const hour = 3_600_000;

function todayAt(hourOfDay: number): number {
  return new Date().setHours(hourOfDay, 0, 0, 0);
}

test("a v1 offline database compacts legacy pauses once before state load", async ({ browser, server }) => {
  const context = await browser.newContext({ serviceWorkers: "block" });
  const page = await context.newPage();
  const fixedNow = todayAt(17);
  const stoppedID = crypto.randomUUID();
  const runningID = crypto.randomUUID();
  const deletedID = crypto.randomUUID();
  const dirtyUpdatedAt = fixedNow - hour;

  // Establish the app's origin without running its scripts, then reproduce the
  // exact schema and paused_ms rows written by the old v1 browser database.
  await page.goto(server.url + "/manifest.webmanifest");
  await page.evaluate(
    async ({ stoppedID, runningID, deletedID, fixedNow, dirtyUpdatedAt, hour }) => {
      await new Promise<void>((resolve, reject) => {
        const request = indexedDB.deleteDatabase("worktime");
        request.onsuccess = () => resolve();
        request.onerror = () => reject(request.error);
      });
      await new Promise<void>((resolve, reject) => {
        const request = indexedDB.open("worktime", 1);
        request.onupgradeneeded = () => {
          for (const table of ["projects", "time_entries", "time_off"]) {
            request.result.createObjectStore(table, { keyPath: "id" });
          }
          request.result.createObjectStore("meta");
          request.result.createObjectStore("dirty");
        };
        request.onerror = () => reject(request.error);
        request.onsuccess = () => {
          const database = request.result;
          const transaction = database.transaction(["time_entries", "dirty", "meta"], "readwrite");
          const entries = transaction.objectStore("time_entries");
          const base = {
            project_id: null,
            tags: [],
            created_at: fixedNow - 8 * hour,
            updated_at: dirtyUpdatedAt,
            deleted_at: null,
            server_seq: 17,
            paused_ms: hour,
          };
          entries.put({ ...base, id: stoppedID, description: "legacy stopped", started_at: fixedNow - 8 * hour, stopped_at: fixedNow - 5 * hour });
          entries.put({ ...base, id: runningID, description: "legacy running", started_at: fixedNow - 3 * hour, stopped_at: null });
          entries.put({ ...base, id: deletedID, description: "legacy deleted", started_at: fixedNow - 8 * hour, stopped_at: fixedNow - 5 * hour, deleted_at: fixedNow - 4 * hour });
          transaction.objectStore("dirty").put(
            { table: "time_entries", id: stoppedID, updated_at: dirtyUpdatedAt },
            `time_entries:${stoppedID}`,
          );
          transaction.objectStore("meta").put(17, "cursor");
          transaction.objectStore("meta").put("legacy-user", "user_id");
          transaction.oncomplete = () => {
            database.close();
            resolve();
          };
          transaction.onerror = () => reject(transaction.error);
        };
      });
    },
    { stoppedID, runningID, deletedID, fixedNow, dirtyUpdatedAt, hour },
  );

  // Install directly at the target time. Installing at wall-clock time and
  // pausing at 17:00 fails whenever the suite itself starts after 17:00.
  await page.clock.install({ time: new Date(fixedNow) });
  await context.route("**/api/**", (route) => route.abort());
  await context.route("**/auth/**", (route) => route.abort());
  await page.goto(server.url + "/#/");

  const stoppedRow = page.locator(".item").filter({ hasText: "legacy stopped" });
  await expect(stoppedRow.locator(".dur")).toHaveText("2h 00m");
  const runningRow = page.locator(".item").filter({ hasText: "legacy running" });
  await expect(runningRow.locator(".elapsed")).toHaveText("2:00:00");

  await stoppedRow.locator(".desc").click();
  await expect(page.locator("dialog.sheet .ed-calc")).toHaveText("2h 00m");
  await page.locator("dialog.sheet").getByRole("button", { name: "Cancel" }).click();

  await page.goto(server.url + "/#/reports");
  await page.getByRole("button", { name: "Week", exact: true }).click();
  // Reports intentionally count only completed entries; the running boundary
  // is covered by its live elapsed display above.
  await expect(page.locator(".stats")).toContainText("2.0h");

  const snapshot = await page.evaluate(async ({ stoppedID, runningID, deletedID }) => {
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("worktime");
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["time_entries", "dirty", "meta"], "readonly");
    const read = <Value>(request: IDBRequest<Value>) =>
      new Promise<Value>((resolve, reject) => {
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    const entries = transaction.objectStore("time_entries");
    const result = {
      version: database.version,
      stopped: await read<Record<string, unknown>>(entries.get(stoppedID)),
      running: await read<Record<string, unknown>>(entries.get(runningID)),
      deleted: await read<Record<string, unknown>>(entries.get(deletedID)),
      dirty: await read<Record<string, unknown>>(transaction.objectStore("dirty").get(`time_entries:${stoppedID}`)),
      cursor: await read<number>(transaction.objectStore("meta").get("cursor")),
    };
    database.close();
    return result;
  }, { stoppedID, runningID, deletedID });
  expect(snapshot.version).toBe(2);
  expect(snapshot.stopped).not.toHaveProperty("paused_ms");
  expect(snapshot.stopped.id).toBe(stoppedID);
  expect(snapshot.stopped.stopped_at).toBe(fixedNow - 6 * hour);
  expect(snapshot.stopped.updated_at).toBe(dirtyUpdatedAt);
  expect(snapshot.stopped.server_seq).toBe(17);
  expect(snapshot.running).not.toHaveProperty("paused_ms");
  expect(snapshot.running.id).toBe(runningID);
  expect(snapshot.running.started_at).toBe(fixedNow - 2 * hour);
  expect(snapshot.running.updated_at).toBe(dirtyUpdatedAt);
  expect(snapshot.running.server_seq).toBe(17);
  expect(snapshot.deleted).not.toHaveProperty("paused_ms");
  expect(snapshot.deleted.deleted_at).toBe(fixedNow - 4 * hour);
  expect(snapshot.dirty).toEqual({ table: "time_entries", id: stoppedID, updated_at: dirtyUpdatedAt });
  expect(snapshot.cursor).toBe(17);

  await page.reload();
  await expect(page.locator(".stats")).toContainText("2.0h");
  await page.goto(server.url + "/#/");
  await expect(page.locator(".item").filter({ hasText: "legacy stopped" }).locator(".dur")).toHaveText("2h 00m");
  await expect(page.locator(".item").filter({ hasText: "legacy running" }).locator(".elapsed")).toHaveText("2:00:00");
  await context.close();
});
