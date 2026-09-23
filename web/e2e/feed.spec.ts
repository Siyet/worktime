import { expect, seedServer, test, triggerSync } from "./fixtures";
import type { Page } from "@playwright/test";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

// 9:00 local keeps every seeded entry on the intended day even right after midnight.
function dayNine(offsetDays: number): number {
  return new Date().setHours(9, 0, 0, 0) + offsetDays * DAY;
}

function isoDay(ms: number): string {
  const date = new Date(ms);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

// One day card for every calendar day back to `days` ago, three entries each, so
// nothing below depends on the weekday the suite runs on.
async function seedHistory(serverURL: string, days: number): Promise<void> {
  const entries = [];
  for (let offset = 1; offset <= days; offset++) {
    for (let index = 0; index < 3; index++) {
      const startedAt = dayNine(-offset) + index * 2 * HOUR;
      entries.push({ description: `Day ${offset} task ${index}`, startedAt, stoppedAt: startedAt + HOUR });
    }
  }
  await seedServer(serverURL, { entries });
}

// Collected from the page itself: Chromium reports ResizeObserver loop errors to
// window "error" listeners only - never to Playwright's pageerror or the console.
async function trackErrors(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const errors: string[] = [];
    (window as unknown as { feedErrors: string[] }).feedErrors = errors;
    window.addEventListener("error", (event) => errors.push(String(event.message)));
  });
}

async function pageErrors(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { feedErrors: string[] }).feedErrors);
}

// A handful of frames: the scroller plans on one frame, the ResizeObserver
// measures after it, and the next plan uses those heights.
async function settle(page: Page): Promise<void> {
  for (let frame = 0; frame < 4; frame++) {
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => setTimeout(resolve, 0))));
  }
}

async function mountedDays(page: Page): Promise<string[]> {
  return page.locator(".feed > .day").evaluateAll((days) => days.map((day) => (day as HTMLElement).dataset.key ?? ""));
}

// Scrolls a screen at a time until `text` is on screen, the way a reader would.
async function scrollUntilVisible(page: Page, text: string): Promise<void> {
  const target = page.locator(".feed .item").filter({ hasText: text });
  for (let step = 0; step < 200; step++) {
    if ((await target.count()) > 0 && (await target.first().isVisible())) {
      await target.first().scrollIntoViewIfNeeded();
      await settle(page);
      return;
    }
    await page.evaluate(() => window.scrollBy(0, window.innerHeight * 0.8));
    await settle(page);
  }
  throw new Error(`never reached "${text}"`);
}

// The first feed row wholly below `line` (viewport pixels): a row the reader sees.
async function probeRow(page: Page, line = 0): Promise<{ text: string; y: number }> {
  return page.evaluate((line) => {
    for (const row of document.querySelectorAll<HTMLElement>(".feed .item")) {
      const rect = row.getBoundingClientRect();
      if (rect.top >= line) return { text: row.querySelector(".desc")!.textContent!.trim(), y: rect.top };
    }
    throw new Error("no row below the line");
  }, line);
}

async function rowY(page: Page, text: string): Promise<number> {
  const box = await page.locator(".feed .item").filter({ hasText: text }).first().boundingBox();
  if (box === null) throw new Error(`row "${text}" is not rendered`);
  return box.y;
}

function runningCard(page: Page) {
  return page.locator(".card").filter({ has: page.getByRole("heading", { name: "Running" }) });
}

test.describe("feed paging", () => {
  test("older days load while scrolling and the ones far behind are unmounted", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 90);
    await page.goto(server.url + "/#/");
    await expect(page.locator(".feed .item").filter({ hasText: "Day 1 task 0" })).toBeVisible();
    await settle(page);

    // Only the first days are in the DOM, not the whole history.
    const initial = await mountedDays(page);
    expect(initial.length).toBeLessThan(20);
    expect(initial).not.toContain(isoDay(dayNine(-60)));

    // The reader can reach the oldest day - the old seven-day cut is gone.
    await scrollUntilVisible(page, "Day 90 task 0");
    const deep = await mountedDays(page);
    expect(deep.length).toBeLessThan(40);
    // The newest days are several screens behind by now and have been unmounted...
    expect(deep).not.toContain(isoDay(dayNine(-1)));
    // ...but their height stays: the page did not collapse under the reader.
    const gap = await page.locator(".feed > .feed-gap").first().evaluate((element) => element.getBoundingClientRect().height);
    expect(gap).toBeGreaterThan(1000);

    // Home jumps straight back over the gap to the newest day.
    await page.keyboard.press("Home");
    await expect(page.locator(".feed .item").filter({ hasText: "Day 1 task 0" })).toBeVisible();
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the reader's row stays put while things above it change", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 60);
    await page.setViewportSize({ width: 1200, height: 800 });
    await page.goto(server.url + "/#/");
    // Without the browser's own anchoring - Safari has none, and it would hide a
    // missing correction in Chromium.
    await page.addStyleTag({ content: "html { overflow-anchor: none !important; }" });
    await scrollUntilVisible(page, "Day 30 task 0");

    // A mounted day above the viewport gains rows through a sync.
    const aboveKey = await page.evaluate(() => {
      const above = [...document.querySelectorAll<HTMLElement>(".feed > .day")].filter(
        (day) => day.getBoundingClientRect().bottom < 0,
      );
      return above.at(-1)!.dataset.key!;
    });
    let probe = await probeRow(page);
    const aboveDay = new Date(aboveKey + "T20:00").getTime();
    await seedServer(server.url, {
      entries: [0, 1, 2].map((index) => ({
        description: `Late addition ${index}`,
        startedAt: aboveDay + index * 10 * 60_000,
        stoppedAt: aboveDay + index * 10 * 60_000 + 5 * 60_000,
      })),
    });
    await triggerSync(page);
    await expect(page.locator(`.feed > .day[data-key="${aboveKey}"] .item`).filter({ hasText: "Late addition" })).toHaveCount(3);
    await settle(page);
    expect(Math.abs((await rowY(page, probe.text)) - probe.y)).toBeLessThanOrEqual(1);

    // The header above the page grows.
    probe = await probeRow(page);
    await page.addStyleTag({ content: "header { padding-bottom: 120px !important; }" });
    await settle(page);
    expect(Math.abs((await rowY(page, probe.text)) - probe.y)).toBeLessThanOrEqual(1);

    // A day that is no longer mounted changes, so its remembered height is stale;
    // scrolling up far enough to mount it again must not move what the reader sees
    // by anything more than the scroll itself.
    const staleKey = await page.evaluate(() => {
      const first = document.querySelector<HTMLElement>(".feed > .day")!.dataset.key!;
      const newer = new Date(first + "T12:00");
      newer.setDate(newer.getDate() + 1);
      return `${newer.getFullYear()}-${String(newer.getMonth() + 1).padStart(2, "0")}-${String(newer.getDate()).padStart(2, "0")}`;
    });
    expect(await mountedDays(page)).not.toContain(staleKey);
    const staleDay = new Date(staleKey + "T20:00").getTime();
    const staleSynced = page.waitForResponse((response) => response.url().includes("/api/sync") && response.ok());
    await seedServer(server.url, {
      entries: [0, 1, 2, 3, 4].map((index) => ({
        description: `Stale addition ${index}`,
        startedAt: staleDay + index * 10 * 60_000,
        stoppedAt: staleDay + index * 10 * 60_000 + 5 * 60_000,
      })),
    });
    await triggerSync(page);
    await staleSynced;
    await settle(page);
    probe = await probeRow(page);
    // Far enough that the stale day, just beyond the kept range, comes within the
    // mounting range; the probe stays mounted below.
    const lift = 1800;
    await page.evaluate((lift) => window.scrollBy(0, -lift), lift);
    await settle(page);
    await expect(page.locator(`.feed > .day[data-key="${staleKey}"] .item`).filter({ hasText: "Stale addition" })).toHaveCount(5);
    await settle(page);
    expect(Math.abs((await rowY(page, probe.text)) - (probe.y + lift))).toBeLessThanOrEqual(1);

    expect(await pageErrors(page)).toEqual([]);
  });

  test("a narrow window and back leaves the reader where they were", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 800 });
    await page.goto(server.url + "/#/");
    await page.addStyleTag({ content: "html { overflow-anchor: none !important; }" });
    await scrollUntilVisible(page, "Day 20 task 0");

    // The row the scroller holds is the first one crossing the top edge, so that
    // is the one that must not move; rows further down reflow at 360px.
    const anchor = await page.evaluate(() => {
      for (const row of document.querySelectorAll<HTMLElement>(".feed [data-anchor]")) {
        const rect = row.getBoundingClientRect();
        if (rect.bottom > 0) {
          row.setAttribute("data-probe", "");
          return rect.top;
        }
      }
      throw new Error("no row at the top");
    });
    await page.setViewportSize({ width: 360, height: 800 });
    await settle(page);
    await page.setViewportSize({ width: 1200, height: 800 });
    await settle(page);
    const after = await page.locator("[data-probe]").boundingBox();
    expect(Math.abs(after!.y - anchor)).toBeLessThanOrEqual(1);
    expect(await pageErrors(page)).toEqual([]);
  });
});

test.describe("pinned running timers", () => {
  test("stay on screen through the feed, and Stop keeps the reader's row", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.goto(server.url + "/#/");
    await page.addStyleTag({ content: "html { overflow-anchor: none !important; }" });
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();

    await scrollUntilVisible(page, "Day 25 task 0");
    const card = await runningCard(page).boundingBox();
    // Stuck to the top of the viewport, with its Stop within reach.
    expect(card!.y).toBeLessThanOrEqual(1);
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);
    // Paging and focus scrolling keep rows out from under the card.
    const padding = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop));
    expect(padding).toBeGreaterThanOrEqual(card!.height);

    const probe = await probeRow(page, card!.y + card!.height);
    await runningCard(page).getByRole("button", { name: "Stop" }).click();
    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    await settle(page);
    expect(Math.abs((await rowY(page, probe.text)) - probe.y)).toBeLessThanOrEqual(1);
    // Nothing is stuck any more, so nothing is padded either.
    expect(await page.evaluate(() => document.documentElement.style.scrollPaddingTop)).toBe("");

    // The stopped entry landed in today's card at the top of the feed.
    await page.evaluate(() => window.scrollTo(0, 0));
    await expect(page.locator(".feed .item").filter({ hasText: "Pinned work" })).toBeVisible();
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the description suggestions open over the running card", async ({ page, server }) => {
    await seedServer(server.url, {
      entries: [{ description: "Write e2e tests", startedAt: dayNine(-1), stoppedAt: dayNine(-1) + HOUR }],
    });
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();

    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write");
    await page.getByRole("listbox", { name: "Recent tasks" }).getByRole("option", { name: /Write e2e tests/ }).click();
    await expect(input).toHaveValue("Write e2e tests");
  });
});
