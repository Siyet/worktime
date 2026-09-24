import { expect, pushBarrier, seedServer, test, triggerSync } from "./fixtures";
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

// Keyboard scrolling keys act on the document only while nothing is focused.
async function blur(page: Page): Promise<void> {
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
}

// After a width change the scroller re-measures the days above the window while
// the reader idles; wait for it to start and to finish.
async function remeasured(page: Page): Promise<void> {
  await settle(page);
  await expect(page.locator(".feed")).toHaveAttribute("data-measuring", "");
  await expect(page.locator(".feed")).not.toHaveAttribute("data-measuring", /.*/, { timeout: 15_000 });
}

// The scroll position once a smooth scroll has come to rest: unchanged for a few
// frames running. A fixed wait reads WebKit's animated page step halfway.
async function restingScrollY(page: Page): Promise<number> {
  let last = Number.NaN;
  let still = 0;
  for (let frame = 0; frame < 600 && still < 6; frame++) {
    const scrollY = await page.evaluate(() => new Promise<number>((resolve) => requestAnimationFrame(() => resolve(window.scrollY))));
    still = scrollY === last ? still + 1 : 0;
    last = scrollY;
  }
  return last;
}

// Whether the element's centre really is drawn there, not covered or clipped.
async function hitsItself(target: import("@playwright/test").Locator): Promise<boolean> {
  return target.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    return hit !== null && element.contains(hit);
  });
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

function strip(page: Page) {
  return page.locator(".pinbar");
}

function stripLines(page: Page) {
  return page.locator(".pinbar > ul > li");
}

// Timers that each get a line of their own: different descriptions never group.
function timers(count: number, now = Date.now()) {
  return Array.from({ length: count }, (_, index) => ({
    description: `Agent task ${index}`,
    startedAt: now - (index + 1) * 60_000,
    stoppedAt: null,
  }));
}

interface StripGeometry {
  top: number;
  bottom: number;
  height: number;
  /** What the height has to be: padding, border and a fixed line per row. */
  expected: number;
  lines: number;
  coarse: boolean;
  boxTop: number;
  padding: number;
  focusInStrip: boolean;
}

// The strip's height is arithmetic - 4px of padding above and below, a 1px
// border and one fixed line per row, 2.75rem under a thumb and 2rem under a
// mouse - which is what lets its box stick at the bottom edge without measuring.
async function stripGeometry(page: Page): Promise<StripGeometry> {
  return page.evaluate(() => {
    const bar = document.querySelector<HTMLElement>(".pinbar")!;
    const rect = bar.getBoundingClientRect();
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
    const coarse = matchMedia("(pointer: coarse)").matches;
    const lines = bar.querySelectorAll(":scope > ul > li").length + (bar.querySelector(":scope > .pin-more") === null ? 0 : 1);
    return {
      top: rect.top,
      bottom: rect.bottom,
      height: rect.height,
      expected: 0.5 * rem + 1 + lines * (coarse ? 2.75 : 2) * rem,
      lines,
      coarse,
      boxTop: document.querySelector(".pinbox")!.getBoundingClientRect().top,
      padding: parseFloat(document.documentElement.style.scrollPaddingTop || "0"),
      focusInStrip: document.querySelector(".pinbox")!.contains(document.activeElement),
    };
  });
}

// Stuck, as tall as its lines, its box and the page's scroll padding both at its
// bottom edge. The padding is lifted while focus is in the strip: its controls
// sit in the padded band, and WebKit would scroll the page to reveal them.
async function expectStrip(page: Page, lines: number, coarse: boolean): Promise<StripGeometry> {
  await expect(strip(page)).toHaveClass(/stuck/);
  await settle(page);
  const geometry = await stripGeometry(page);
  expect(geometry.coarse).toBe(coarse);
  expect(geometry.lines).toBe(lines);
  expect(Math.abs(geometry.height - geometry.expected)).toBeLessThanOrEqual(0.5);
  expect(Math.abs(geometry.top)).toBeLessThanOrEqual(0.5);
  expect(Math.abs(geometry.boxTop - geometry.bottom)).toBeLessThanOrEqual(0.5);
  expect(Math.abs(geometry.padding - (geometry.focusInStrip ? 0 : geometry.bottom))).toBeLessThanOrEqual(1);
  return geometry;
}

// A real pointer's click. Playwright's own click first scrolls its target into
// view, and WebKit counts the strip's controls as hidden under the scroll padding
// they sit in - a scroll no reader's tap ever makes.
async function tap(page: Page, target: import("@playwright/test").Locator): Promise<void> {
  const box = (await target.boundingBox())!;
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
}

async function focusedTwin(page: Page): Promise<{ twin: string | null; inStrip: boolean; inCard: boolean }> {
  return page.evaluate(() => {
    const active = document.activeElement as HTMLElement | null;
    return {
      twin: active?.dataset.twin ?? null,
      inStrip: active?.closest(".pinbar") !== null && active?.closest(".pinbar") !== undefined,
      inCard: active?.closest(".running-full") !== null && active?.closest(".running-full") !== undefined,
    };
  });
}

test.describe("pinned running timers", () => {
  test("the full card hands over to the strip without moving the feed", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();
    await expect(strip(page)).toBeHidden();

    // Through the hand-off a few pixels at a time: the swap is visibility only,
    // so a feed row never moves in the document.
    const row = page.locator(".feed .item").filter({ hasText: "Day 2 task 0" });
    const documentTop = (): Promise<number> => row.evaluate((element) => element.getBoundingClientRect().top + window.scrollY);
    const before = await documentTop();
    let swaps = 0;
    let stuck = false;
    for (let scrollY = 0; scrollY <= 600; scrollY += 15) {
      await page.evaluate((scrollY) => window.scrollTo(0, scrollY), scrollY);
      await settle(page);
      expect(Math.abs((await documentTop()) - before)).toBeLessThanOrEqual(1);
      const now = await strip(page).evaluate((element) => element.classList.contains("stuck"));
      if (now !== stuck) swaps += 1;
      stuck = now;
    }
    expect(swaps).toBe(1);
    await expectStrip(page, 1, false);
    // The card stays in the page, hidden and out of reach, still holding the feed down.
    const card = page.locator(".running-full");
    await expect(card).toHaveCSS("visibility", "hidden");
    expect(await card.evaluate((element) => (element as HTMLElement).inert)).toBe(true);

    await scrollUntilVisible(page, "Day 25 task 0");
    const geometry = await expectStrip(page, 1, false);
    const probe = await probeRow(page, geometry.bottom);
    await tap(page, strip(page).getByRole("button", { name: "Stop" }));
    await expect(page.getByRole("heading", { name: "Running" })).toHaveCount(0);
    await expect(strip(page)).toHaveCount(0);
    await settle(page);
    expect(Math.abs((await rowY(page, probe.text)) - probe.y)).toBeLessThanOrEqual(1);
    // Nothing is stuck any more, so nothing is padded either.
    expect(await page.evaluate(() => document.documentElement.style.scrollPaddingTop)).toBe("");
    await expect(page.locator(".toast")).toContainText("Stopped");
    await expect(page.locator(".toast")).toContainText("Pinned work");

    // The stopped entry landed in today's card at the top of the feed.
    await page.evaluate(() => window.scrollTo(0, 0));
    await expect(page.locator(".feed .item").filter({ hasText: "Pinned work" })).toBeVisible();
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the description suggestions open over the running card", async ({ page, server }) => {
    await trackErrors(page);
    // Enough matching tasks that the list reaches well down over the card.
    await seedServer(server.url, {
      entries: ["Write e2e tests", "Write docs", "Write the changelog", "Write a spec", "Write release notes"].map(
        (description, index) => ({ description, startedAt: dayNine(-1) + index * HOUR, stoppedAt: dayNine(-1) + index * HOUR + 30 * 60_000 }),
      ),
    });
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();

    const input = page.getByPlaceholder("What are you working on?");
    await input.fill("write");
    const list = page.getByRole("listbox", { name: "Recent tasks" });
    const last = list.getByRole("option").last();
    // The option really is drawn over the card, not merely clickable beside it.
    const card = (await runningCard(page).boundingBox())!;
    const option = (await last.boundingBox())!;
    expect(option.y + option.height / 2).toBeGreaterThan(card.y);
    const onTop = await page.evaluate(
      ({ x, y }) => document.elementFromPoint(x, y)?.closest("[role=listbox]") !== null,
      { x: option.x + option.width / 2, y: option.y + option.height / 2 },
    );
    expect(onTop).toBe(true);
    const text = (await last.locator(".stext").textContent())!;
    await last.click();
    await expect(input).toHaveValue(text);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("under a mouse the strip grows a line per timer up to six", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    const now = Date.now();
    const all = timers(7, now);
    await seedServer(server.url, { entries: all.slice(0, 1) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 1, false);
    // A Stop under a mouse is a 28px target with a 26px face.
    const stop = (await strip(page).getByRole("button", { name: "Stop" }).boundingBox())!;
    expect([Math.round(stop.width), Math.round(stop.height)]).toEqual([28, 28]);

    // More timers arrive by sync while the reader is deep in the feed.
    for (const [from, to, lines] of [[1, 4, 4], [4, 6, 6], [6, 7, 5]] as const) {
      await seedServer(server.url, { entries: all.slice(from, to) });
      await triggerSync(page);
      await expect(stripLines(page)).toHaveCount(lines);
    }
    // Seven timers: five lines, and the sixth says what is behind it.
    await expect(strip(page).locator(".pin-more")).toContainText("+2 more");
    await expectStrip(page, 6, false);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a timer that starts while the reader is deep moves their row by its line", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    // The reader has been still for a moment: only then does the strip push
    // their row down rather than lie over it.
    await page.waitForTimeout(400);
    const probe = await probeRow(page);

    // A timer started elsewhere - another device, an agent - arrives by sync.
    await seedServer(server.url, {
      entries: [{ description: "Started elsewhere", startedAt: Date.now() - 60_000, stoppedAt: null }],
    });
    await triggerSync(page);
    const geometry = await expectStrip(page, 1, false);
    // Pushed down by exactly the strip, so it sits just as far below its bottom
    // edge as it sat below the top of the screen.
    expect(Math.abs((await rowY(page, probe.text)) - (probe.y + geometry.bottom))).toBeLessThanOrEqual(1);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("Repeat deep in the feed carries the pressed row down with the strip", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    const base = dayNine(-20) + 7 * HOUR;
    await seedServer(server.url, {
      entries: ["Weekly sync", "Weekly sync", "Retro", "Retro"].map((description, index) => ({
        description,
        startedAt: base + index * 20 * 60_000,
        stoppedAt: base + index * 20 * 60_000 + 10 * 60_000,
      })),
    });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    const groupLine = (task: string) => page.locator(".feed .group-line").filter({ hasText: task });

    // The row sits at the very top, and the page has been still for a while.
    await page.evaluate((top) => window.scrollBy(0, top - 4), (await groupLine("Retro").boundingBox())!.y);
    await page.waitForTimeout(400);
    const before = (await groupLine("Retro").boundingBox())!.y;
    // The press is not a scroll: the row moves down with the strip that appears.
    await tap(page, page.getByRole("button", { name: "Repeat Retro" }));
    const geometry = await expectStrip(page, 1, false);
    await settle(page);
    expect(Math.abs((await groupLine("Retro").boundingBox())!.y - (before + geometry.bottom))).toBeLessThanOrEqual(1);

    // Space on a button presses it rather than scrolling, so it carries too.
    const lift = (await groupLine("Weekly sync").boundingBox())!.y - geometry.bottom - 4;
    await page.evaluate((lift) => window.scrollBy(0, lift), lift);
    await page.waitForTimeout(400);
    const next = (await groupLine("Weekly sync").boundingBox())!.y;
    await page.getByRole("button", { name: "Repeat Weekly sync" }).focus();
    await page.keyboard.press("Space");
    const grown = await expectStrip(page, 2, false);
    await settle(page);
    expect(Math.abs((await groupLine("Weekly sync").boundingBox())!.y - (next + grown.bottom - geometry.bottom))).toBeLessThanOrEqual(1);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a timer that arrives while the page scrolls lets the scroll finish", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 60);
    await page.setViewportSize({ width: 1200, height: 900 });
    await seedServer(server.url, { entries: timers(1) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 45 task 0");
    await expectStrip(page, 1, false);
    // On the server already; the page only learns of it once the scroll is under way.
    await seedServer(server.url, {
      entries: [{ description: "Arrived mid-scroll", startedAt: Date.now() - 1_000, stoppedAt: null }],
    });
    const outcome = await page.evaluate(async () => {
      const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
      const target = window.scrollY - 5000;
      window.scrollTo({ top: target, behavior: "smooth" });
      await frame();
      await frame();
      window.dispatchEvent(new Event("online"));
      let last = Number.NaN;
      let still = 0;
      let count = 0;
      let grewAt = -1;
      while (still < 6 && count < 900) {
        await frame();
        count += 1;
        if (grewAt < 0 && document.querySelectorAll(".pinbar > ul > li").length === 2) grewAt = count;
        still = window.scrollY === last ? still + 1 : 0;
        last = window.scrollY;
      }
      return { target, rest: window.scrollY, grewAt, restedAt: count - 6 };
    });
    // The timer arrived while the page was still moving...
    expect(outcome.grewAt).toBeGreaterThan(0);
    expect(outcome.grewAt).toBeLessThan(outcome.restedAt);
    // ...and the scroll still went all the way.
    expect(Math.abs(outcome.rest - outcome.target)).toBeLessThanOrEqual(2);
    // Still again, the hidden card takes its real height and the strip is right.
    await expectStrip(page, 2, false);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a timer stopped elsewhere while the page scrolls lets the scroll finish", async ({ page, request, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 30);
    await page.setViewportSize({ width: 1200, height: 900 });
    const now = Date.now();
    const today = dayNine(0);
    const stopping = { id: crypto.randomUUID(), description: "Stopped elsewhere", startedAt: now - 30 * 60_000, stoppedAt: null };
    await seedServer(server.url, {
      entries: [
        ...[0, 1, 2, 3, 4, 5].map((index) => ({
          description: `Today ${index}`,
          startedAt: today + index * 20 * 60_000,
          stoppedAt: today + index * 20 * 60_000 + 10 * 60_000,
        })),
        stopping,
        ...timers(2, now),
      ],
    });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 8 task 0");
    await expectStrip(page, 3, false);
    // Stopped on another device: it lands in today's day, above the reader.
    const stoppedAt = Date.now();
    const pushed = await request.post(server.url + "/api/sync", {
      data: {
        since: Number.MAX_SAFE_INTEGER,
        changes: {
          time_entries: [
            {
              id: stopping.id,
              project_id: null,
              description: stopping.description,
              tags: [],
              started_at: stopping.startedAt,
              stopped_at: stoppedAt,
              created_at: stopping.startedAt,
              updated_at: stoppedAt,
              deleted_at: null,
            },
          ],
        },
      },
    });
    expect(pushed.ok()).toBe(true);
    const outcome = await page.evaluate(async () => {
      const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
      const sentinel = document.querySelector(".running-full + div")!;
      // Up, but not so far that the strip lets go.
      const lowest = sentinel.getBoundingClientRect().top + window.scrollY + 200;
      const target = Math.max(lowest, window.scrollY - 2000);
      window.scrollTo({ top: target, behavior: "smooth" });
      await frame();
      await frame();
      window.dispatchEvent(new Event("online"));
      let last = Number.NaN;
      let still = 0;
      let count = 0;
      let shrankAt = -1;
      while (still < 6 && count < 900) {
        await frame();
        count += 1;
        if (shrankAt < 0 && document.querySelectorAll(".pinbar > ul > li").length === 2) shrankAt = count;
        still = window.scrollY === last ? still + 1 : 0;
        last = window.scrollY;
      }
      return { target, rest: window.scrollY, shrankAt, restedAt: count - 6 };
    });
    expect(outcome.shrankAt).toBeGreaterThan(0);
    expect(outcome.shrankAt).toBeLessThan(outcome.restedAt);
    expect(Math.abs(outcome.rest - outcome.target)).toBeLessThanOrEqual(2);
    await expect(page.locator(".feed .item").filter({ hasText: "Stopped elsewhere" })).toHaveCount(1);
    await expectStrip(page, 2, false);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the card held during a scroll is never shown cut to its old height", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 60);
    await page.setViewportSize({ width: 1200, height: 900 });
    await seedServer(server.url, { entries: timers(3) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 25 task 0");
    await expectStrip(page, 3, false);
    await seedServer(server.url, {
      entries: [{ description: "Arrived mid-scroll", startedAt: Date.now() - 1_000, stoppedAt: null }],
    });
    // All the way back up in one scroll, with the timer arriving on the way.
    const shownHeld = await page.evaluate(async () => {
      const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
      window.scrollTo({ top: 0, behavior: "smooth" });
      await frame();
      await frame();
      window.dispatchEvent(new Event("online"));
      let shown = 0;
      for (let count = 0; count < 300 && window.scrollY > 0; count++) {
        await frame();
        const card = document.querySelector<HTMLElement>(".running-full");
        if (card !== null && getComputedStyle(card).visibility === "visible" && card.style.height !== "") shown += 1;
      }
      return shown;
    });
    expect(shownHeld).toBe(0);
    // And the scroll still reached the top: the card taking its new height as it
    // comes into view is no correction, which would stop a smooth scroll short.
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
    await expect(runningCard(page).locator(".item")).toHaveCount(4);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("Undo after Stop restarts the same row", async ({ page, request, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    const id = crypto.randomUUID();
    await seedServer(server.url, { entries: [{ id, description: "Pinned work", startedAt: Date.now() - 5 * 60_000, stoppedAt: null }] });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 1, false);

    await tap(page, strip(page).getByRole("button", { name: "Stop" }));
    const toast = page.locator(".toast");
    await expect(toast).toContainText("Stopped");
    await expect(toast).toContainText("Pinned work");
    await expect(strip(page)).toHaveCount(0);

    const restarted = pushBarrier(page, '"stopped_at":null');
    await toast.getByRole("button", { name: "Undo" }).click();
    await expect(toast).toHaveCount(0);
    await expectStrip(page, 1, false);
    await expect(stripLines(page)).toContainText("Pinned work");
    await restarted;

    // The server has the one row running again, with its original start - not a copy.
    const response = await request.post(server.url + "/api/sync", { data: { since: 0, changes: {} } });
    const { changes } = (await response.json()) as { changes: { time_entries: Array<{ id: string; description: string; stopped_at: number | null; deleted_at: number | null }> } };
    const pinned = changes.time_entries.filter((entry) => entry.description === "Pinned work" && entry.deleted_at === null);
    expect(pinned.map((entry) => [entry.id, entry.stopped_at])).toEqual([[id, null]]);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("two quick Stops are undone together", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    await seedServer(server.url, { entries: timers(3) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 3, false);

    // The line below slides up under the pointer, so a second press stops it too.
    const first = strip(page).getByRole("button", { name: "Stop" }).first();
    await tap(page, first);
    await expect(stripLines(page)).toHaveCount(2);
    await tap(page, first);
    await expect(stripLines(page)).toHaveCount(1);
    const toast = page.locator(".toast");
    await expect(toast).toContainText("Stopped");
    await expect(toast).toContainText("2 timers");

    await toast.getByRole("button", { name: "Undo" }).click();
    await expect(stripLines(page)).toHaveCount(3);
    await expect(toast).toHaveCount(0);

    // Stops made deliberately, a moment apart, are separate: Undo takes back
    // only the last one.
    await tap(page, first);
    await expect(stripLines(page)).toHaveCount(2);
    await page.waitForTimeout(1_700);
    await tap(page, first);
    await expect(stripLines(page)).toHaveCount(1);
    await expect(toast).not.toContainText("timers");
    await toast.getByRole("button", { name: "Undo" }).click();
    await expect(stripLines(page)).toHaveCount(2);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("Stop from the keyboard hands focus to the line that took its place", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    await seedServer(server.url, { entries: timers(4) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 4, false);
    const stops = strip(page).getByRole("button", { name: "Stop" });

    // The first line's Stop: the line below moves up into its place and its Stop
    // takes the focus.
    await stops.first().focus();
    await page.keyboard.press("Enter");
    await expect(stripLines(page)).toHaveCount(3);
    await expect(page.locator(".toast")).toContainText("Stopped");
    await expect(stripLines(page).first().locator(".pin-stop")).toBeFocused();
    await expectStrip(page, 3, false);

    // The last line's Stop: the line above takes the focus.
    await stops.last().focus();
    await page.keyboard.press("Enter");
    await expect(stripLines(page)).toHaveCount(2);
    await expect(stripLines(page).last().locator(".pin-stop")).toBeFocused();

    // A pointer Stop moves no focus. The button it removed held the focus - WebKit
    // reports no focusout for that - and the padding still comes back.
    await tap(page, stops.last());
    await expect(stripLines(page)).toHaveCount(1);
    await expect.poll(() => page.evaluate(() => document.activeElement === document.body)).toBe(true);
    expect((await expectStrip(page, 1, false)).focusInStrip).toBe(false);

    // The last timer: focus goes to the start form, never to the page body, and
    // the reader's row stays where it is.
    const probe = await probeRow(page, (await stripGeometry(page)).bottom);
    await stops.first().focus();
    await page.keyboard.press("Enter");
    await expect(strip(page)).toHaveCount(0);
    await expect(page.getByPlaceholder("What are you working on?")).toBeFocused();
    await settle(page);
    // The card's height is fractional and WebKit keeps the scroll position in
    // whole pixels, so the correction lands within a pixel and a half.
    expect(Math.abs((await rowY(page, probe.text)) - probe.y)).toBeLessThanOrEqual(1.5);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("focus moves between the card and the strip as they swap", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    await seedServer(server.url, { entries: timers(7) });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(7);

    // A Stop in the card goes to the same timer's Stop in the strip, and back.
    const first = runningCard(page).locator(".item").first().getByRole("button", { name: "Stop" });
    const twin = (await first.getAttribute("data-twin"))!;
    await first.focus();
    await page.evaluate(() => window.scrollTo(0, 1500));
    await expect(strip(page)).toHaveClass(/stuck/);
    await expect.poll(() => focusedTwin(page)).toEqual({ twin, inStrip: true, inCard: false });
    await page.evaluate(() => window.scrollTo(0, 0));
    await expect(strip(page)).not.toHaveClass(/stuck/);
    await expect.poll(() => focusedTwin(page)).toEqual({ twin, inStrip: false, inCard: true });

    // A timer that has no line of its own in the strip hands focus to "+N more".
    await runningCard(page).locator(".item").last().getByRole("button", { name: "Stop" }).focus();
    await page.evaluate(() => window.scrollTo(0, 1500));
    await expect.poll(() => focusedTwin(page)).toEqual({ twin: "more", inStrip: true, inCard: false });

    // Every control of the strip takes focus without moving the page.
    const scrollY = await page.evaluate(() => window.scrollY);
    const controls = strip(page).locator("button");
    for (let index = (await controls.count()) - 1; index >= 0; index--) {
      await controls.nth(index).focus();
      await settle(page);
      expect(await page.evaluate(() => window.scrollY)).toBe(scrollY);
    }
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a session of an expanded group hands focus to its group's line", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    const now = Date.now();
    await seedServer(server.url, {
      entries: [
        { description: "Pair task", startedAt: now - 60_000, stoppedAt: null },
        { description: "Pair task", startedAt: now - 2 * 60_000, stoppedAt: null },
        ...timers(2, now - 2 * 60_000),
      ],
    });
    await page.goto(server.url + "/#/");
    const card = runningCard(page);
    await card.locator(".group-row").click();
    await expect(card.locator(".item.member")).toHaveCount(2);
    await card.locator(".item.member").last().getByRole("button", { name: "Stop" }).focus();

    await page.evaluate(() => window.scrollTo(0, 1500));
    await expect(strip(page)).toHaveClass(/stuck/);
    const twin = await strip(page).locator(".pin-toggle").getAttribute("data-twin");
    await expect.poll(() => focusedTwin(page)).toEqual({ twin, inStrip: true, inCard: false });
    expect(await pageErrors(page)).toEqual([]);
  });

  test("page keys never slide unread rows under the strip", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await expect(runningCard(page)).toBeVisible();
    await blur(page);
    const height = await page.evaluate(() => window.innerHeight);

    // From the top: this step is the one that sticks the strip.
    let scrollY = await restingScrollY(page);
    expect(scrollY).toBe(0);
    await page.keyboard.press("PageDown");
    scrollY = await restingScrollY(page);
    // Whatever sat at the bottom of the screen must still be in view below the strip.
    const visible = height - (await expectStrip(page, 1, false)).bottom;
    expect(scrollY).toBeLessThanOrEqual(height);
    expect(scrollY).toBeGreaterThan(visible / 2);

    await scrollUntilVisible(page, "Day 15 task 0");
    await blur(page);
    scrollY = await restingScrollY(page);
    for (const [key, direction] of [["PageDown", 1], ["Space", 1], ["PageUp", -1], ["Shift+Space", -1]] as const) {
      await page.keyboard.press(key);
      const next = await restingScrollY(page);
      const step = (next - scrollY) * direction;
      expect(step, key).toBeLessThanOrEqual(visible);
      expect(step, key).toBeGreaterThan(visible / 2);
      scrollY = next;
    }

    // A scroll key between two quick page steps moves the page elsewhere: the
    // second step starts from there, not from where the first was heading.
    await page.keyboard.press("PageDown");
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("PageDown");
    const twoSteps = (await restingScrollY(page)) - scrollY;
    expect(twoSteps).toBeLessThan(2 * visible - 1);
    scrollY += twoSteps;

    // Focus in the strip pages the page the same way.
    await strip(page).getByRole("button", { name: "Stop" }).focus();
    await page.keyboard.press("PageDown");
    const fromStrip = (await restingScrollY(page)) - scrollY;
    expect(fromStrip).toBeLessThanOrEqual(visible);
    expect(fromStrip).toBeGreaterThan(visible / 2);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("page keys scroll an open overlay before the page", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    // Enough timers that the "+N more" list scrolls inside its own height.
    await seedServer(server.url, { entries: timers(20) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 6, false);
    await tap(page, strip(page).locator(".pin-more"));
    const overlay = strip(page).locator(".pin-overlay");
    await expect(overlay).toBeVisible();
    expect(await overlay.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);

    const scrollY = await restingScrollY(page);
    await overlay.getByRole("button", { name: "Stop" }).first().focus();
    await page.keyboard.press("PageDown");
    await expect.poll(() => overlay.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    expect(await restingScrollY(page)).toBe(scrollY);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("an open menu in the card closes when the strip takes over", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 900 });
    const projects = ["Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"].map((name) => ({ id: crypto.randomUUID(), name }));
    await seedServer(server.url, { projects, entries: timers(4) });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(4);

    // The card is never capped any more, so its menus open in full like any other.
    const row = runningCard(page).locator(".item").last();
    await row.getByRole("combobox", { name: "Edit project" }).click();
    const menu = page.getByRole("listbox", { name: "Project" });
    const last = menu.getByRole("option", { name: "Foxtrot" });
    expect(await hitsItself(last)).toBe(true);
    await last.click();
    await expect(row.getByRole("combobox", { name: "Edit project" })).toHaveText("Foxtrot");

    await row.getByRole("combobox", { name: "Edit project" }).click();
    await expect(menu).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 1500));
    await expect(strip(page)).toHaveClass(/stuck/);
    await expect(menu).toHaveCount(0);

    await page.evaluate(() => window.scrollTo(0, 0));
    await expect(strip(page)).not.toHaveClass(/stuck/);
    await row.getByRole("button", { name: "Add tags" }).click();
    const tags = page.getByRole("dialog", { name: "Tags" });
    await expect(tags).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 1500));
    await expect(strip(page)).toHaveClass(/stuck/);
    await expect(tags).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });
});

test.describe("pinned running timers under a thumb", () => {
  test.use({ hasTouch: true, viewport: { width: 390, height: 664 } });

  test("the strip takes four lines, two on a short screen, and flags the long-running", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    const now = Date.now();
    // The oldest ran through the night: it is behind "+N more", which says so.
    await seedServer(server.url, {
      entries: [...timers(5, now), { description: "Night shift", startedAt: now - 9 * HOUR, stoppedAt: null }],
    });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 4, true);
    const more = strip(page).locator(".pin-more");
    await expect(more).toHaveText(/\+3 more · 1 over 8h/);
    await expect(more).toHaveClass(/late/);
    // Thumb-sized: the whole 44px square is the Stop.
    const stop = (await strip(page).getByRole("button", { name: "Stop" }).first().boundingBox())!;
    expect([Math.round(stop.width), Math.round(stop.height)]).toEqual([44, 44]);

    // A phone's toolbar collapsing changes only the height: the strip stays put.
    await page.setViewportSize({ width: 390, height: 440 });
    await settle(page);
    await expectStrip(page, 4, true);

    // Landscape on a phone is short: one line and "+N more".
    await page.setViewportSize({ width: 740, height: 360 });
    await expect(stripLines(page)).toHaveCount(1);
    await expect(more).toHaveText(/\+5 more · 1 over 8h/);
    await expectStrip(page, 2, true);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("overlays open below the strip one at a time, and Escape closes them", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    const now = Date.now();
    await seedServer(server.url, {
      entries: [
        { description: "Review pull request", startedAt: now - 60_000, stoppedAt: null },
        { description: "Review pull request", startedAt: now - 2 * 60_000, stoppedAt: null },
        ...timers(4, now - 2 * 60_000),
        { description: "Night shift", startedAt: now - 9 * HOUR, stoppedAt: null },
      ],
    });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    const geometry = await expectStrip(page, 4, true);

    // The group's line opens its sessions, labelled by start time.
    const toggle = strip(page).locator(".pin-toggle");
    await expect(toggle).toContainText("Review pull request");
    await tap(page, toggle);
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    const groupOverlay = strip(page).locator(".group .pin-overlay");
    await expect(groupOverlay.locator(".pin-line")).toHaveCount(2);
    expect((await groupOverlay.boundingBox())!.y).toBeGreaterThanOrEqual(geometry.bottom);

    // "+N more" replaces it rather than stacking a second overlay.
    const more = strip(page).locator(".pin-more");
    await tap(page, more);
    await expect(groupOverlay).toHaveCount(0);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    const moreOverlay = strip(page).locator(".pin-more + .pin-overlay");
    await expect(moreOverlay.locator(".pin-line")).toHaveCount(3);
    await expect(moreOverlay.locator(".pin-time.late")).toHaveCount(1);
    // Nothing about the strip changed: the overlay lies over the feed.
    const open = await stripGeometry(page);
    expect([open.height, open.boxTop]).toEqual([geometry.height, geometry.boxTop]);

    await page.keyboard.press("Escape");
    await expect(moreOverlay).toHaveCount(0);
    await expect(more).toBeFocused();

    // A keyboard Stop inside a group's overlay that leaves one session behind
    // hands focus to that session's own line; the group comes back collapsed.
    await tap(page, toggle);
    await groupOverlay.getByRole("button", { name: "Stop" }).first().focus();
    await page.keyboard.press("Enter");
    await expect(groupOverlay).toHaveCount(0);
    const review = stripLines(page).filter({ hasText: "Review pull request" });
    await expect(review.locator(".pin-stop")).toBeFocused();
    await seedServer(server.url, { entries: [{ description: "Review pull request", startedAt: Date.now() - 1_000, stoppedAt: null }] });
    await triggerSync(page);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(strip(page).locator(".pin-overlay")).toHaveCount(0);

    // Leaving the feed for the top of the page closes whatever is open.
    await tap(page, more);
    await expect(moreOverlay).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 0));
    await expect(strip(page)).not.toHaveClass(/stuck/);
    await expect(moreOverlay).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("an overlay leaves Escape to a dialog opened from it and never reopens by itself", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await seedServer(server.url, { entries: timers(5) });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 4, true);
    const more = strip(page).locator(".pin-more");
    const overlay = strip(page).locator(".pin-more + .pin-overlay");

    // The editor of a timer behind "+N" closes on the first Escape.
    await tap(page, more);
    await tap(page, overlay.getByRole("button", { name: "Agent task 3" }));
    const editor = page.locator("dialog.sheet[open]");
    await expect(editor).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(editor).toHaveCount(0);

    // A Stop from the keyboard inside the overlay that ends the overflow hands
    // focus to the line the other hidden timer now has - not to an unrelated one.
    await expect(overlay).toBeVisible();
    await overlay.getByRole("button", { name: "Stop" }).first().focus();
    await page.keyboard.press("Enter");
    await expect(more).toHaveCount(0);
    await expect(stripLines(page)).toHaveCount(4);
    await expect(stripLines(page).filter({ hasText: "Agent task 4" }).locator(".pin-stop")).toBeFocused();

    // "+N" comes back with the next timer, closed.
    await seedServer(server.url, { entries: [{ description: "Arrived later", startedAt: Date.now() - 1_000, stoppedAt: null }] });
    await triggerSync(page);
    await expect(more).toBeVisible();
    await expect(more).toHaveAttribute("aria-expanded", "false");
    await expect(strip(page).locator(".pin-overlay")).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("titles cut to the same text carry their start time, the rest do not", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    const now = Date.now();
    await seedServer(server.url, {
      entries: [
        { description: "WorkTime #12 Pinned running timers need a compact strip", startedAt: now - 5 * 60_000, stoppedAt: null },
        { description: "WorkTime #12 Pinned running timers need a compact review", startedAt: now - 6 * 60_000, stoppedAt: null },
        { description: "Short one", startedAt: now - 7 * 60_000, stoppedAt: null },
      ],
    });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 20 task 0");
    await expectStrip(page, 3, true);
    const suffixes = strip(page).locator(".pin-suffix");
    await expect(suffixes).toHaveCount(2);
    const [first, second] = await suffixes.allTextContents();
    expect(first).toMatch(/\d:\d\d/);
    expect(first).not.toBe(second);
    await expect(stripLines(page).filter({ hasText: "Short one" }).locator(".pin-suffix")).toHaveCount(0);

    // Wide enough for the whole titles: nothing needs telling apart.
    await page.setViewportSize({ width: 1200, height: 800 });
    await expect(suffixes).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });
});

test.describe("feed after a width change", () => {
  // Stale remembered heights would make every remount correct the scroll, and an
  // instant correction cancels a smooth scroll: Home and the iOS status-bar tap
  // would stop halfway. The scroller re-measures stale days while the reader idles.
  test("Home and a smooth scroll to the top both arrive", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 120);
    await page.setViewportSize({ width: 1200, height: 800 });
    await page.goto(server.url + "/#/");
    await scrollUntilVisible(page, "Day 90 task 0");

    await page.setViewportSize({ width: 400, height: 800 });
    await remeasured(page);
    await blur(page);
    await page.keyboard.press("Home");
    await expect.poll(() => page.evaluate(() => window.scrollY), { timeout: 10_000 }).toBe(0);

    await scrollUntilVisible(page, "Day 90 task 0");
    await page.setViewportSize({ width: 1200, height: 800 });
    await remeasured(page);
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: "smooth" }));
    await expect.poll(() => page.evaluate(() => window.scrollY), { timeout: 10_000 }).toBe(0);
    expect(await pageErrors(page)).toEqual([]);
  });
});

test.describe("feed and the group editor", () => {
  // The test intercepts /api/sync, which a service worker would answer first.
  test.use({ serviceWorkers: "block" });

  test("Review entries brings back a day the feed has unmounted", async ({ page, request, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 60);
    const base = dayNine(0);
    await seedServer(server.url, {
      entries: [
        { description: "LWW group", startedAt: base, stoppedAt: base + HOUR },
        { description: "LWW group", startedAt: base + 2 * HOUR, stoppedAt: base + 3 * HOUR },
      ],
    });
    await page.goto(server.url + "/#/");
    // Another device wins one of the two rows, so the result offers Review entries.
    await page.route("**/api/sync", async (route) => {
      const body = JSON.parse(route.request().postData() ?? "{}") as { changes?: { time_entries?: Array<Record<string, unknown>> } };
      const pushedRows = body.changes?.time_entries ?? [];
      if (pushedRows.some((entry) => entry.description === "Local attempt")) {
        const remoteWinner = { ...pushedRows[0], description: "Remote winner", updated_at: Number(pushedRows[0]?.updated_at) + 1 };
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
    await expect(result.getByRole("status")).toContainText("1 changed elsewhere");

    // Far enough down that today's card is no longer mounted.
    await scrollUntilVisible(page, "Day 40 task 0");
    expect(await mountedDays(page)).not.toContain(isoDay(base));
    await result.getByRole("button", { name: "Review entries" }).click();
    const focused = page.locator(".feed .item .desc:focus");
    await expect(focused).toHaveCount(1);
    await expect(focused).toBeInViewport();
    expect(await pageErrors(page)).toEqual([]);
  });
});
