import { expect, pushBarrier, seedServer, test } from "./fixtures";
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
    await expect(group.locator(".count")).toHaveText("×3");
    await expect(group.locator(".when .dur")).toHaveText("3h 00m");
    // The summary row is not an entry: specs that count .item must keep counting
    // entries, so only the ungrouped "Standup" is one here.
    await expect(card.locator(".item")).toHaveCount(1);
    await expect(card.locator(".row > .mono").last()).toHaveText("3h 30m");

    const disclosure = group.getByRole("button", { name: "Expand" });
    await expect(disclosure).toHaveAttribute("aria-expanded", "false");
    await disclosure.click();
    await expect(card.locator(".item.member")).toHaveCount(3);
    await expect(group.getByRole("button", { name: "Collapse" })).toHaveAttribute("aria-expanded", "true");
    await group.getByRole("button", { name: "Collapse" }).click();
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
    await expect(group.locator(".count")).toHaveText("×3");

    await group.getByRole("button", { name: "Expand" }).click();
    const member = card.locator(".item.member").first();
    await member.getByRole("button", { name: "Entry actions" }).click();
    await page.getByRole("menuitem", { name: "Delete" }).click();
    await expect(group.locator(".count")).toHaveText("×2");

    await page.getByRole("button", { name: "Undo" }).click();
    await expect(group.locator(".count")).toHaveText("×3");
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
    await expect(group.locator(".count")).toHaveText("×2");

    await group.getByRole("button", { name: "Expand" }).click();
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
    await todayCard.locator(".group-row").getByRole("button", { name: "Expand" }).click();

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
    // The day header carries the same pair, so the two never disagree.
    const header = card.locator(".row").first();
    await expect(header.locator(".wall")).toContainText("1h 00m");
    await expect(header.locator(".mono").last()).toHaveText("2h 00m");
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
    await expect(group.locator(".count")).toHaveText("×2");
    // A collapsed running group has no Stop of its own: stopping several timers
    // at once would need an undo that does not exist.
    await expect(card.getByRole("button", { name: "Stop" })).toHaveCount(0);

    await group.getByRole("button", { name: "Expand" }).click();
    await expect(card.locator(".item.member")).toHaveCount(2);
    await card.locator(".item.member").first().getByRole("button", { name: "Stop" }).click();
    await expect(card.locator(".item")).toHaveCount(1);
    await expect(card.locator(".group-row")).toHaveCount(0);
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
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });
});
