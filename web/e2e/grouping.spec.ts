import { expect, pushBarrier, seedServer, test, triggerSync } from "./fixtures";
import type { Locator, Page } from "@playwright/test";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

// 9:00 local keeps every seeded entry on the intended day even right after midnight.
function dayNine(offsetDays = 0): number {
  return new Date().setHours(9, 0, 0, 0) + offsetDays * DAY;
}

function dayHeading(page: Page, offsetDays = 0): Promise<string> {
  return page.evaluate(
    (offset) =>
      new Date(Date.now() + offset * 86_400_000).toLocaleDateString([], {
        weekday: "short",
        month: "short",
        day: "numeric",
      }),
    offsetDays,
  );
}

async function dayCard(page: Page, offsetDays = 0): Promise<Locator> {
  return page.locator(".card").filter({ hasText: await dayHeading(page, offsetDays) });
}

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

async function holdGroupWrites(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("worktime");
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const transaction = database.transaction(["time_entries", "dirty"], "readwrite");
    const store = transaction.objectStore("time_entries");
    let holding = true;
    const keepAlive = () => {
      if (!holding) return;
      const request = store.get("__group_commit_hold__");
      request.onsuccess = keepAlive;
    };
    keepAlive();
    (window as unknown as { releaseGroupWrites: () => void }).releaseGroupWrites = () => {
      holding = false;
    };
  });
}

async function releaseGroupWrites(page: Page): Promise<void> {
  await page.evaluate(() => (window as unknown as { releaseGroupWrites: () => void }).releaseGroupWrites());
}

test.describe("task grouping", () => {
  test("repeats of one task collapse into a single row that unfolds", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    const base = dayNine();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [
        { description: "Write e2e tests", startedAt: base, stoppedAt: base + HOUR, projectID, tags: ["review"] },
        // A different spelling of the same task must not read as a second task.
        {
          description: "write  E2E   tests",
          startedAt: base + 2 * HOUR,
          stoppedAt: base + 3 * HOUR,
          projectID,
          tags: ["review"],
        },
        {
          description: "Write e2e tests",
          startedAt: base + 4 * HOUR,
          stoppedAt: base + 5 * HOUR,
          projectID,
          tags: ["review"],
        },
        { description: "Standup", startedAt: base + 5 * HOUR, stoppedAt: base + 5.5 * HOUR, projectID },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-row").filter({ hasText: "Write e2e tests" });
    await expect(group.locator(".count-value")).toHaveText("3");
    await expect(group.locator(".when .dur")).toHaveText("3h 00m");
    // The summary row is not an entry: specs that count .item must keep counting
    // entries, so only the ungrouped "Standup" is one here.
    await expect(card.locator(".item")).toHaveCount(1);
    await expect(card.locator(".row > .mono").last()).toHaveText("3h 30m");

    // The whole summary row is the control - a group has nothing else to click.
    await expect(group).toHaveAttribute("aria-expanded", "false");
    await group.click();
    await expect(card.locator(".item.member")).toHaveCount(3);
    await expect(group).toHaveAttribute("aria-expanded", "true");
    await group.click();
    await expect(card.locator(".item.member")).toHaveCount(0);
  });

  test("deleting a member shrinks the group and undo brings it back", async ({ page, server }) => {
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "Repeated", startedAt: base, stoppedAt: base + HOUR },
        { description: "Repeated", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
        { description: "Repeated", startedAt: base + 4 * HOUR, stoppedAt: base + 5 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-row").filter({ hasText: "Repeated" });
    await expect(group.locator(".count-value")).toHaveText("3");

    await group.click();
    const member = card.locator(".item.member").first();
    await member.getByRole("button", { name: "Entry actions" }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await expect(group.locator(".count-value")).toHaveText("2");

    await page.getByRole("button", { name: "Undo" }).click();
    await expect(group.locator(".count-value")).toHaveText("3");
  });

  test("renaming an entry takes it out of the group", async ({ page, server }) => {
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "Shared name", startedAt: base, stoppedAt: base + HOUR },
        { description: "Shared name", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-row").filter({ hasText: "Shared name" });
    await expect(group.locator(".count-value")).toHaveText("2");

    await group.click();
    const barrier = pushBarrier(page, "Its own thing");
    await card.locator(".item.member").first().locator(".desc").click();
    await page.locator("#ed-desc").fill("Its own thing");
    await page.getByRole("button", { name: "Save" }).click();
    await barrier;

    await expect(card.locator(".group-row")).toHaveCount(0);
    await expect(card.locator(".item")).toHaveCount(2);
  });

  test("the same description in two projects stays two rows", async ({ page, server }) => {
    const backend = crypto.randomUUID();
    const frontend = crypto.randomUUID();
    const base = dayNine();
    await seedServer(server.url, {
      projects: [
        { id: backend, name: "Backend" },
        { id: frontend, name: "Frontend" },
      ],
      entries: [
        { description: "Code review", startedAt: base, stoppedAt: base + HOUR, projectID: backend },
        { description: "Code review", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR, projectID: frontend },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    await expect(card.locator(".group-row")).toHaveCount(0);
    await expect(card.locator(".item")).toHaveCount(2);
  });

  test("expanding a group in one day leaves the same task in another day collapsed", async ({ page, server }) => {
    const today = dayNine();
    const yesterday = dayNine(-1);
    await seedServer(server.url, {
      entries: [
        { description: "Claude Code #ab12cd34", startedAt: today, stoppedAt: today + HOUR },
        { description: "Claude Code #ab12cd34", startedAt: today + 2 * HOUR, stoppedAt: today + 3 * HOUR },
        { description: "Claude Code #ab12cd34", startedAt: yesterday, stoppedAt: yesterday + HOUR },
        { description: "Claude Code #ab12cd34", startedAt: yesterday + 2 * HOUR, stoppedAt: yesterday + 3 * HOUR },
      ],
    });

    await page.goto(server.url + "/#/");
    const todayCard = await dayCard(page);
    const yesterdayCard = await dayCard(page, -1);
    await todayCard.locator(".group-row").click();

    await expect(todayCard.locator(".item.member")).toHaveCount(2);
    await expect(yesterdayCard.locator(".item.member")).toHaveCount(0);
  });

  test("overlapping work shows tracked time and wall-clock time side by side", async ({ page, server }) => {
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "Parallel agents", startedAt: base, stoppedAt: base + HOUR },
        { description: "Parallel agents", startedAt: base, stoppedAt: base + HOUR },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-row").filter({ hasText: "Parallel agents" });
    await expect(group.locator(".when .dur")).toHaveText("2h 00m");
    await expect(group.locator(".wall")).toContainText("1h 00m");
    // The day header carries the same pair, so the two never disagree - with a
    // divider between them, because unlabelled they read as one number twice.
    const header = card.locator(".row").first();
    await expect(header.locator(".wall")).toContainText("1h 00m");
    await expect(header.locator(".totals .sep")).toHaveText("/");
    await expect(header.locator(".tracked")).toHaveText("2h 00m");
    await expect(header.locator(".totals")).toHaveAttribute(
      "title",
      "1h 00m on the clock, 2h 00m tracked - work that ran in parallel is counted once",
    );
  });

  test("two running timers of one task are one row, stopped from the unfolded list", async ({ page, server }) => {
    await page.goto(server.url + "/#/");
    const input = page.getByPlaceholder("What are you working on?");
    for (let index = 0; index < 2; index++) {
      await input.fill("Claude Code");
      await page.getByRole("button", { name: "Start" }).click();
      await expect(input).toHaveValue("");
    }

    const card = runningCard(page);
    const group = card.locator(".group-row");
    await expect(group.locator(".count-value")).toHaveText("2");
    // A collapsed running group has no Stop of its own: stopping several timers
    // at once would need an undo that does not exist.
    await expect(card.getByRole("button", { name: "Stop" })).toHaveCount(0);
    // Nor a repeat: this task is running right now, so repeating it would only
    // add a third timer for the same work.
    await expect(card.locator(".repeat")).toHaveCount(0);

    await group.click();
    await expect(card.locator(".item.member")).toHaveCount(2);
    await card.locator(".item.member").first().getByRole("button", { name: "Stop" }).click();
    await expect(card.locator(".item")).toHaveCount(1);
    await expect(card.locator(".group-row")).toHaveCount(0);
  });

  test("repeating a group starts the task again with its project and tags", async ({ page, server }) => {
    const projectID = crypto.randomUUID();
    const base = dayNine();
    await seedServer(server.url, {
      projects: [{ id: projectID, name: "Backend" }],
      entries: [
        { description: "Write e2e tests", startedAt: base, stoppedAt: base + HOUR, projectID, tags: ["review"] },
        {
          description: "Write e2e tests",
          startedAt: base + 2 * HOUR,
          stoppedAt: base + 3 * HOUR,
          projectID,
          tags: ["review"],
        },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-line").filter({ hasText: "Write e2e tests" });

    const barrier = pushBarrier(page, "Write e2e tests");
    await group.locator(".repeat").click();
    await barrier;

    // Everything that made the entries one group is what the repeat carries over.
    const started = runningCard(page).locator(".item");
    await expect(started).toHaveCount(1);
    await expect(started.locator(".desc")).toHaveText("Write e2e tests");
    await expect(started.locator(".proj")).toHaveText("Backend");
    await expect(started.locator(".meta")).toContainText("review");
    // The click belongs to the button, not to the row it sits in.
    await expect(group.locator(".group-row")).toHaveAttribute("aria-expanded", "false");
  });

  test("the separate group editor changes shared metadata atomically and preserves every boundary", async ({
    page,
    request,
    server,
  }) => {
    const backendID = crypto.randomUUID();
    const frontendID = crypto.randomUUID();
    const entryIDs = [crypto.randomUUID(), crypto.randomUUID()];
    const joinedLaterID = crypto.randomUUID();
    const base = dayNine();
    await seedServer(server.url, {
      projects: [
        { id: backendID, name: "Backend", archived: true },
        { id: frontendID, name: "Frontend" },
      ],
      entries: [
        { id: entryIDs[0], description: "Grouped Work", startedAt: base, stoppedAt: base + HOUR, projectID: backendID, tags: ["review"] },
        { id: entryIDs[1], description: "grouped   work", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR, projectID: backendID, tags: ["review"] },
      ],
    });
    await page.goto(server.url + "/#/");

    const card = await dayCard(page);
    const summary = card.locator(".group-row");
    const edit = card.getByRole("button", { name: /Edit group grouped work, 2 entries/i });
    await edit.click();
    await expect(summary).toHaveAttribute("aria-expanded", "false");

    const dialog = page.getByRole("dialog", { name: "Edit task group" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel("Description")).toBeFocused();
    await expect(dialog.getByLabel("Project", { exact: true })).toContainText("Backend");
    await expect(dialog.getByText("Date", { exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Time", { exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Session identifier", { exact: true })).toHaveCount(0);

    // A matching row arriving after open is visible after pull but deliberately
    // excluded from the fixed two-member edit.
    const joinedAt = base + 4 * HOUR;
    const joined = await request.post(server.url + "/api/sync", {
      data: {
        since: Number.MAX_SAFE_INTEGER,
        changes: {
          time_entries: [{
            id: joinedLaterID,
            project_id: backendID,
            description: "Grouped Work",
            tags: ["review"],
            started_at: joinedAt,
            stopped_at: joinedAt + HOUR,
            created_at: joinedAt,
            updated_at: joinedAt,
            deleted_at: null,
          }],
        },
      },
    });
    expect(joined.ok()).toBe(true);
    await triggerSync(page);
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("Description").fill("Unified task");
    await dialog.getByLabel("Project", { exact: true }).click();
    await dialog.getByRole("option", { name: "Frontend" }).click();
    await dialog.getByRole("button", { name: "review", exact: true }).click();
    await dialog.getByRole("button", { name: "development", exact: true }).click();

    const pushed = pushBarrier(page, "Unified task");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;
    await expect(page.getByRole("status")).toContainText("2/2 group entries synced");

    const serverEntries = (await (await request.get(server.url + "/api/entries")).json()) as Array<{
      id: string;
      project_id: string | null;
      description: string;
      tags: string[];
      started_at: number;
      stopped_at: number;
    }>;
    const edited = serverEntries.filter((entry) => entryIDs.includes(entry.id));
    expect(edited).toHaveLength(2);
    expect(edited.map((entry) => ({
      id: entry.id,
      description: entry.description,
      projectID: entry.project_id,
      tags: entry.tags,
      startedAt: entry.started_at,
      stoppedAt: entry.stopped_at,
    }))).toEqual(expect.arrayContaining([
      { id: entryIDs[0], description: "Unified task", projectID: frontendID, tags: ["development"], startedAt: base, stoppedAt: base + HOUR },
      { id: entryIDs[1], description: "Unified task", projectID: frontendID, tags: ["development"], startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
    ]));
    expect(serverEntries.find((entry) => entry.id === joinedLaterID)).toMatchObject({
      description: "Grouped Work",
      project_id: backendID,
      tags: ["review"],
    });

    const replacementEdit = card.getByRole("button", { name: /Edit group Unified task, 2 entries/ });
    await expect(replacementEdit).toBeFocused();
    await replacementEdit.locator("xpath=ancestor::*[contains(@class, 'group-line')]").locator(".group-row").press("Enter");
    await expect(card.locator(".item.member")).toHaveCount(2);
  });

  test("a no-op preserves normalised spelling variants and cancel restores focus", async ({ page, server }) => {
    const archivedID = crypto.randomUUID();
    const base = dayNine();
    await seedServer(server.url, {
      projects: [{ id: archivedID, name: "Archived current", archived: true }],
      entries: [
        { description: "Keep Spelling", startedAt: base, stoppedAt: base + HOUR, projectID: archivedID },
        { description: "keep   spelling", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR, projectID: archivedID },
      ],
    });
    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const edit = card.getByRole("button", { name: /Edit group keep spelling, 2 entries/i });

    await edit.click();
    let dialog = page.getByRole("dialog", { name: "Edit task group" });
    await expect(dialog.getByLabel("Project", { exact: true })).toContainText("Archived current");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(edit).toBeFocused();

    await card.locator(".group-row").click();
    await expect(card.locator(".item.member .desc")).toHaveText(["keep spelling", "Keep Spelling"]);
    await card.locator(".group-row").click();

    await edit.click();
    dialog = page.getByRole("dialog", { name: "Edit task group" });
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(edit).toBeFocused();

    await edit.click();
    await expect(dialog).toBeVisible();
    await page.mouse.click(4, 4);
    await expect(dialog).toHaveCount(0);
    await expect(edit).toBeFocused();
  });

  test("an in-flight local group commit cannot be cancelled or dismissed", async ({ page, server }) => {
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "Commit group", startedAt: base, stoppedAt: base + HOUR },
        { description: "Commit group", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });
    await page.goto(server.url + "/#/");
    await page.getByRole("button", { name: /Edit group Commit group, 2 entries/ }).click();
    const dialog = page.getByRole("dialog", { name: "Edit task group" });
    await dialog.getByLabel("Description").fill("Committed together");

    // Keep a readwrite transaction alive so the editor's transaction queues
    // behind it. This makes the saving state deterministic instead of racing a
    // normally sub-millisecond IndexedDB commit.
    await holdGroupWrites(page);

    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toHaveAttribute("aria-busy", "true");
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    await page.mouse.click(4, 4);
    await expect(dialog).toBeVisible();

    await releaseGroupWrites(page);
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole("status")).toContainText("group entries synced");
    await expect(page.getByText("Committed together", { exact: true }).first()).toBeVisible();
  });

  test("a remote Stop is merged but remote grouping metadata blocks the fixed-set save", async ({ page, request, server }) => {
    const runningIDs = [crypto.randomUUID(), crypto.randomUUID()];
    const base = Date.now() - HOUR;
    await seedServer(server.url, {
      entries: runningIDs.map((id) => ({ id, description: "Live group", startedAt: base, stoppedAt: null })),
    });
    await page.goto(server.url + "/#/");
    await runningCard(page).getByRole("button", { name: /Edit group Live group, 2 entries/ }).click();
    const dialog = page.getByRole("dialog", { name: "Edit task group" });

    const stop = await request.post(server.url + "/api/sync", {
      data: {
        since: Number.MAX_SAFE_INTEGER,
        changes: { time_entries: [{
          id: runningIDs[0], project_id: null, description: "Live group", tags: [],
          started_at: base, stopped_at: base + 30_000, created_at: base,
          updated_at: Date.now() + 1_000, deleted_at: null,
        }] },
      },
    });
    expect(stop.ok()).toBe(true);
    await triggerSync(page);
    await expect(dialog.getByRole("button", { name: "Save" })).toBeEnabled();

    await dialog.getByLabel("Description").fill("Live corrected");
    const pushed = pushBarrier(page, "Live corrected");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;
    const entries = (await (await request.get(server.url + "/api/entries")).json()) as Array<{ id: string; description: string; stopped_at: number | null }>;
    expect(entries.filter((entry) => runningIDs.includes(entry.id)).map((entry) => entry.description)).toEqual([
      "Live corrected",
      "Live corrected",
    ]);
    expect(entries.find((entry) => entry.id === runningIDs[0])?.stopped_at).toBe(base + 30_000);

    // Recreate a finished group and prove a grouping-metadata conflict is not
    // silently overwritten by a stale dialog.
    const conflictIDs = [crypto.randomUUID(), crypto.randomUUID()];
    await seedServer(server.url, {
      entries: conflictIDs.map((id, index) => ({
        id,
        description: "Conflict group",
        startedAt: dayNine() + index * 2 * HOUR,
        stoppedAt: dayNine() + (index * 2 + 1) * HOUR,
      })),
    });
    await triggerSync(page);
    const conflictEdit = page.getByRole("button", { name: /Edit group Conflict group, 2 entries/ });
    await conflictEdit.click();
    const conflictDialog = page.getByRole("dialog", { name: "Edit task group" });
    const remoteStart = dayNine();
    const remote = await request.post(server.url + "/api/sync", {
      data: {
        since: Number.MAX_SAFE_INTEGER,
        changes: { time_entries: [{
          id: conflictIDs[0], project_id: null, description: "Remote split", tags: [],
          started_at: remoteStart, stopped_at: remoteStart + HOUR, created_at: remoteStart,
          updated_at: remoteStart + DAY, deleted_at: null,
        }] },
      },
    });
    expect(remote.ok()).toBe(true);
    await expect(async () => {
      await triggerSync(page);
      await expect(conflictDialog.getByRole("alert")).toContainText("changed on another device");
    }).toPass({ timeout: 15_000 });
    await expect(conflictDialog.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  test("a real LWW refusal reports N/M conflict and Review focuses the resulting entry", async ({ page, request, server }) => {
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "LWW group", startedAt: base, stoppedAt: base + HOUR },
        { description: "LWW group", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });
    await page.goto(server.url + "/#/");
    await page.route("**/api/sync", async (route) => {
      const body = JSON.parse(route.request().postData() ?? "{}") as {
        changes?: { time_entries?: Array<Record<string, unknown>> };
      };
      const pushedRows = body.changes?.time_entries ?? [];
      if (pushedRows.some((entry) => entry.description === "Local attempt")) {
        const remoteWinner = {
          ...pushedRows[0],
          description: "Remote winner",
          updated_at: Number(pushedRows[0]?.updated_at) + 1,
        };
        const remote = await request.post(server.url + "/api/sync", {
          data: { since: Number.MAX_SAFE_INTEGER, changes: { time_entries: [remoteWinner] } },
        });
        expect(remote.ok()).toBe(true);
      }
      await route.continue();
    });

    await page.getByRole("button", { name: /Edit group LWW group, 2 entries/ }).click();
    const dialog = page.getByRole("dialog", { name: "Edit task group" });
    await dialog.getByLabel("Description").fill("Local attempt");
    const pushed = pushBarrier(page, "Local attempt");
    await dialog.getByRole("button", { name: "Save" }).click();
    await pushed;

    const result = page.locator(".group-sync-result");
    await expect(result.getByRole("status")).toContainText("1/2 group entries synced");
    await expect(result.getByRole("status")).toContainText("1 changed elsewhere");
    await result.getByRole("button", { name: "Review entries" }).click();
    await expect(page.locator(".item .desc:focus")).toHaveCount(1);
  });

  test("offline group save survives reload and permanent rejection has an N/M recovery path", async ({
    page,
    context,
    server,
  }) => {
    const base = dayNine();
    const offlineIDs = [crypto.randomUUID(), crypto.randomUUID()];
    await seedServer(server.url, {
      entries: [
        { id: offlineIDs[0], description: "Offline group", startedAt: base, stoppedAt: base + HOUR },
        { id: offlineIDs[1], description: "Offline group", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });
    await page.goto(server.url + "/#/");
    await page.getByRole("button", { name: /Edit group Offline group, 2 entries/ }).click();
    const dialog = page.getByRole("dialog", { name: "Edit task group" });
    await context.setOffline(true);
    await dialog.getByLabel("Description").fill("Saved offline");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("status")).toContainText("0/2 group entries synced");
    await expect(page.getByRole("status")).toContainText("2 pending");
    await page.route("**/api/sync", (route) => route.fulfill({ status: 500, body: "still offline for reload" }));
    await context.setOffline(false);
    await page.reload();
    await expect(page.getByText("Saved offline", { exact: true }).first()).toBeVisible();
    await page.unroute("**/api/sync");
    const pushed = pushBarrier(page, "Saved offline");
    await triggerSync(page);
    await pushed;

    // A permanent 400 is not presented as success: exact isolated outcomes reach
    // both the operation result and the global recovery link.
    let rejecting = true;
    const rejectedID = offlineIDs[0];
    const retriedIDs: string[] = [];
    await page.route("**/api/sync", async (route) => {
      const bodyText = route.request().postData() ?? "";
      if (!bodyText.includes("Rejected group")) {
        await route.continue();
        return;
      }
      const body = JSON.parse(bodyText) as { changes?: { time_entries?: Array<{ id: string }> } };
      const rows = body.changes?.time_entries ?? [];
      if (rejecting && (rows.length > 1 || rows[0]?.id === rejectedID)) {
        await route.fulfill({ status: 400, body: "refused for test" });
        return;
      }
      if (!rejecting) retriedIDs.push(...rows.map((row) => row.id));
      await route.continue();
    });
    await page.getByRole("button", { name: /Edit group Saved offline, 2 entries/ }).click();
    const retryDialog = page.getByRole("dialog", { name: "Edit task group" });
    await retryDialog.getByLabel("Description").fill("Rejected group");
    await retryDialog.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("status")).toContainText("1/2 group entries synced");
    await expect(page.getByRole("status")).toContainText("1 rejected", { timeout: 15_000 });
    await expect(page.getByRole("link", { name: "1 changes need attention" })).toBeVisible();
    const retry = page.getByRole("button", { name: "Retry" });
    await expect(retry).toBeEnabled();
    await expect(page.getByRole("link", { name: "Sync details" })).toHaveAttribute("href", "#/settings");
    rejecting = false;
    await holdGroupWrites(page);
    await retry.evaluate((button: HTMLButtonElement) => {
      button.click();
      button.click();
    });
    await expect(retry).toBeDisabled();
    await expect(page.getByRole("button", { name: "Dismiss" })).toBeDisabled();
    await releaseGroupWrites(page);
    await expect(page.getByRole("status")).toContainText("2/2 group entries synced");
    expect(retriedIDs).toEqual([rejectedID]);
  });

  test("Retry turns a superseded rejected member into a conflict without overwriting it", async ({
    page,
    request,
    server,
  }) => {
    const base = dayNine();
    const memberIDs = [crypto.randomUUID(), crypto.randomUUID()];
    await seedServer(server.url, {
      entries: [
        { id: memberIDs[0], description: "Superseded group", startedAt: base, stoppedAt: base + HOUR },
        { id: memberIDs[1], description: "Superseded group", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });
    await page.goto(server.url + "/#/");

    const rejectedID = memberIDs[0];
    let rejectGroupEdit = true;
    const retriedIDs: string[] = [];
    await page.route("**/api/sync", async (route) => {
      const bodyText = route.request().postData() ?? "";
      const body = JSON.parse(bodyText) as { changes?: { time_entries?: Array<{ id: string; description: string }> } };
      const rows = body.changes?.time_entries ?? [];
      if (
        rejectGroupEdit &&
        rows.some((row) => row.description === "Rejected superseded") &&
        (rows.length > 1 || rows[0]?.id === rejectedID)
      ) {
        await route.fulfill({ status: 400, body: "refused for superseded retry test" });
        return;
      }
      if (!rejectGroupEdit && rows.some((row) => row.description === "Rejected superseded")) {
        retriedIDs.push(...rows.map((row) => row.id));
      }
      await route.continue();
    });

    await page.getByRole("button", { name: /Edit group Superseded group, 2 entries/ }).click();
    const dialog = page.getByRole("dialog", { name: "Edit task group" });
    await dialog.getByLabel("Description").fill("Rejected superseded");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("status")).toContainText("1 rejected", { timeout: 15_000 });

    // Edit the quarantined member through the ordinary editor. It is the older
    // (second rendered) row, so this produces a newer local/server version after
    // the exact rejected marker the toast retained.
    await page.locator(".group-row").filter({ hasText: "Rejected superseded" }).click();
    const rejectedMember = page.locator(".item.member").nth(1);
    await rejectedMember.locator(".desc").click();
    await page.locator("#ed-desc").fill("Newer individual edit");
    const newerPush = pushBarrier(page, "Newer individual edit");
    await page.getByRole("button", { name: "Save" }).click();
    await newerPush;

    rejectGroupEdit = false;
    await page.getByRole("button", { name: "Retry" }).click();
    await expect(page.getByRole("status")).toContainText("1 changed elsewhere");
    await expect(page.getByRole("button", { name: "Review entries" })).toBeVisible();
    expect(retriedIDs).toEqual([]);
    const serverEntries = (await (await request.get(server.url + "/api/entries")).json()) as Array<{
      id: string;
      description: string;
    }>;
    expect(serverEntries.find((entry) => entry.id === rejectedID)?.description).toBe("Newer individual edit");
  });

  test("the group row fits a 360px screen", async ({ page, server }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    const base = dayNine();
    await seedServer(server.url, {
      entries: [
        { description: "A rather long task description that has to wrap", startedAt: base, stoppedAt: base + HOUR },
        {
          description: "A rather long task description that has to wrap",
          startedAt: base + 2 * HOUR,
          stoppedAt: base + 3 * HOUR,
        },
      ],
    });

    await page.goto(server.url + "/#/");
    const card = await dayCard(page);
    const group = card.locator(".group-row");
    await expect(group).toBeVisible();
    await expect(card.locator(".edit-group")).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);

    await card.locator(".edit-group").click();
    await expect(page.getByRole("dialog", { name: "Edit task group" })).toBeVisible();
    const dialogOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(dialogOverflow).toBeLessThanOrEqual(0);
  });
});
