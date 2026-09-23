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
    // From the keyboard: Playwright's click scrolls its target into view first,
    // which a real pointer never does, and that scroll is not what is tested here.
    await runningCard(page).getByRole("button", { name: "Stop" }).focus();
    await page.keyboard.press("Enter");
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

  test("keyboard focus walks the pinned card without moving the page", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    // A phone, and more timers than the capped card can show at once.
    await page.setViewportSize({ width: 360, height: 740 });
    await seedServer(server.url, {
      entries: [0, 1, 2, 3, 4, 5].map((index) => ({
        description: `Agent task ${index}`,
        startedAt: Date.now() - (index + 1) * 60_000,
        stoppedAt: null,
      })),
    });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(6);
    await scrollUntilVisible(page, "Day 20 task 0");
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);
    expect(await page.locator(".pinned > .card").evaluate((card) => card.scrollHeight > card.clientHeight)).toBe(true);

    // Every control in the card, from the last up - the order Shift+Tab takes
    // coming in from the feed. Focused one by one rather than tabbed through: Safari
    // only tabs to buttons with "Press Tab to highlight each item" switched on.
    const scrollY = await page.evaluate(() => window.scrollY);
    const controls = page.locator(".pinned > .card").locator("button, [tabindex='0']");
    const count = await controls.count();
    const stopCount = await runningCard(page).getByRole("button", { name: "Stop" }).count();
    let visited = 0;
    for (let index = count - 1; index >= 0; index--) {
      await controls.nth(index).evaluate((control) => (control as HTMLElement).focus());
      const focus = await page.evaluate(() => {
        const active = document.activeElement as HTMLElement;
        const card = document.querySelector(".pinned > .card")!.getBoundingClientRect();
        const rect = active.getBoundingClientRect();
        return {
          isStop: active.textContent?.trim() === "Stop",
          visible: rect.top >= card.top - 1 && rect.bottom <= card.bottom + 1,
          scrollY: window.scrollY,
        };
      });
      expect(focus.scrollY).toBe(scrollY);
      expect(focus.visible).toBe(true);
      if (focus.isStop) visited += 1;
    }
    expect(visited).toBe(stopCount);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("page keys never slide unread rows under the pinned card", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.goto(server.url + "/#/");
    await page.getByPlaceholder("What are you working on?").fill("Pinned work");
    await page.getByRole("button", { name: "Start" }).click();
    await blur(page);
    const card = (await runningCard(page).boundingBox())!;
    const height = await page.evaluate(() => window.innerHeight);
    // Whatever sat at the bottom of the screen must still be in view below the
    // card, which is stuck to the top once the page has moved.
    const visible = height - card.height;

    // From the top: this step is the one that sticks the card.
    let scrollY = await restingScrollY(page);
    expect(scrollY).toBe(0);
    await page.keyboard.press("PageDown");
    scrollY = await restingScrollY(page);
    expect(scrollY).toBeLessThanOrEqual(visible);
    expect(scrollY).toBeGreaterThan(visible / 2);
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);

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

    // Focus in a card with nothing left to scroll of its own - after Stop, after
    // picking a project - pages the page the same way.
    await runningCard(page).getByRole("button", { name: "Stop" }).focus();
    await page.keyboard.press("PageDown");
    const fromCard = (await restingScrollY(page)) - scrollY;
    expect(fromCard).toBeLessThanOrEqual(visible);
    expect(fromCard).toBeGreaterThan(visible / 2);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a focused Stop that stops its own timer hands the padding back", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await seedServer(server.url, {
      entries: [0, 1].map((index) => ({ description: `Agent task ${index}`, startedAt: Date.now() - (index + 1) * 60_000, stoppedAt: null })),
    });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(2);
    await scrollUntilVisible(page, "Day 20 task 0");
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);

    // The button goes away with its timer, and WebKit reports no focusout for it.
    await runningCard(page).getByRole("button", { name: "Stop" }).first().focus();
    await page.keyboard.press("Enter");
    await expect(runningCard(page).locator(".item")).toHaveCount(1);
    await settle(page);
    const card = (await runningCard(page).boundingBox())!;
    const padding = await page.evaluate(() => document.documentElement.style.scrollPaddingTop);
    expect(parseFloat(padding)).toBeGreaterThanOrEqual(card.y + card.height);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the project menu of a running row opens in full over a card that scrolls", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    // A phone, more timers than the capped card shows, and a long project list.
    await page.setViewportSize({ width: 360, height: 740 });
    const projects = ["Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"].map((name) => ({ id: crypto.randomUUID(), name }));
    await seedServer(server.url, {
      projects,
      entries: [0, 1, 2, 3].map((index) => ({ description: `Agent task ${index}`, startedAt: Date.now() - (index + 1) * 60_000, stoppedAt: null })),
    });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(4);
    await scrollUntilVisible(page, "Day 20 task 0");
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);
    const card = page.locator(".pinned > .card");
    expect(await card.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);

    // The last row, scrolled into view inside the card, its menu below the card's edge.
    const row = runningCard(page).locator(".item").last();
    await row.evaluate((element) => element.scrollIntoView({ block: "nearest" }));
    await row.getByRole("combobox", { name: "Edit project" }).click();
    const menu = page.getByRole("listbox", { name: "Project" });
    const last = menu.getByRole("option", { name: "Foxtrot" });
    expect((await last.boundingBox())!.y).toBeGreaterThan((await card.boundingBox())!.y + (await card.boundingBox())!.height);
    expect(await hitsItself(last)).toBe(true);
    await last.click();
    await expect(row.getByRole("combobox", { name: "Edit project" })).toHaveText("Foxtrot");

    // Scrolled out of the card's view, the trigger takes its open menu with it.
    await row.getByRole("combobox", { name: "Edit project" }).click();
    await expect(menu).toBeVisible();
    await card.evaluate((element) => element.scrollTo({ top: 0, behavior: "instant" }));
    await expect(menu).toBeHidden();
    await card.evaluate((element) => element.scrollTo({ top: element.scrollHeight, behavior: "instant" }));
    await expect(menu).toBeVisible();
    await page.keyboard.press("Escape");

    // A feed row's menu scrolls with the page as before; only the card's float.
    const feedRow = page.locator(".feed .item").filter({ hasText: "Day 20 task 0" });
    await feedRow.getByRole("combobox", { name: "Edit project" }).click();
    expect(await menu.evaluate((element) => getComputedStyle(element).position)).toBe("absolute");
    await page.keyboard.press("Escape");
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the tags menu of a running row opens in full over a card that scrolls", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.setViewportSize({ width: 1200, height: 800 });
    await seedServer(server.url, {
      entries: [0, 1, 2, 3, 4, 5, 6, 7].map((index) => ({ description: `Agent task ${index}`, startedAt: Date.now() - (index + 1) * 60_000, stoppedAt: null })),
    });
    await page.goto(server.url + "/#/");
    await expect(runningCard(page).locator(".item")).toHaveCount(8);
    await scrollUntilVisible(page, "Day 20 task 0");
    const card = page.locator(".pinned > .card");
    expect(await card.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);

    const row = runningCard(page).locator(".item").last();
    await row.evaluate((element) => element.scrollIntoView({ block: "nearest" }));
    await row.getByRole("button", { name: "Add tags" }).click();
    const menu = page.getByRole("dialog", { name: "Tags" });
    const save = menu.getByRole("button", { name: "Save" });
    expect((await save.boundingBox())!.y).toBeGreaterThan((await card.boundingBox())!.y + (await card.boundingBox())!.height);
    expect(await hitsItself(save)).toBe(true);
    await menu.getByRole("button", { name: "Cancel" }).click();
    await expect(menu).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a timer that starts while the reader is deep does not cover their row", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 40);
    await page.goto(server.url + "/#/");
    await page.addStyleTag({ content: "html { overflow-anchor: none !important; }" });
    await scrollUntilVisible(page, "Day 20 task 0");
    const probe = await probeRow(page);

    // A timer started elsewhere - another device, an agent - arrives by sync.
    await seedServer(server.url, {
      entries: [{ description: "Started elsewhere", startedAt: Date.now() - 60_000, stoppedAt: null }],
    });
    await triggerSync(page);
    await expect(page.locator(".pinned")).toHaveClass(/stuck/);
    await settle(page);
    const card = (await runningCard(page).boundingBox())!;
    // Pushed down by exactly the card, so it sits just as far below its bottom edge
    // as it sat below the top of the screen.
    expect(Math.abs((await rowY(page, probe.text)) - (probe.y + card.y + card.height))).toBeLessThanOrEqual(1);
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
    await page.addStyleTag({ content: "html { overflow-anchor: none !important; }" });
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
