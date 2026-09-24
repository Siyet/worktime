import { expect, seedServer, test, triggerSync } from "./fixtures";
import type { Page } from "@playwright/test";

// The day ruler beside the Timer feed (design/components/day-ruler.html): wide
// windows with a mouse only, every calendar day a row, a click jumps the feed.

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

function weekend(offsetDays: number): boolean {
  const weekday = new Date(dayNine(offsetDays)).getDay();
  return weekday === 0 || weekday === 6;
}

/** The newest offset at or before `from` days ago that falls on the weekend, or on a weekday. */
function nearest(from: number, onWeekend: boolean): number {
  let offset = from;
  while (weekend(-offset) !== onWeekend) offset += 1;
  return offset;
}

const HISTORY_DAYS = 150;
const VACATION = [30, 34];
const SICK = nearest(12, false);

/** Whether the seeded history has entries on the day `offset` days ago. */
function recorded(offset: number): boolean {
  if (offset === 0) return true;
  if (offset < 0 || offset > HISTORY_DAYS || weekend(-offset) || offset === SICK) return false;
  return offset < VACATION[0]! || offset > VACATION[1]!;
}

/** The oldest day with entries: where the ruler ends. */
const OLDEST = (() => {
  let offset = HISTORY_DAYS;
  while (!recorded(offset)) offset -= 1;
  return offset;
})();

/** The newest day with entries at or before `from` days ago. */
function recordedFrom(from: number): number {
  let offset = from;
  while (!recorded(offset)) offset += 1;
  return offset;
}

// Two entries on every weekday back to HISTORY_DAYS ago, none on weekends, a
// vacation week and a sick day without entries, and one entry today.
async function seedHistory(serverURL: string, running = 0): Promise<void> {
  const entries = [];
  for (let offset = 1; offset <= HISTORY_DAYS; offset++) {
    if (!recorded(offset)) continue;
    entries.push({ description: `Day ${offset} early`, startedAt: dayNine(-offset), stoppedAt: dayNine(-offset) + 55 * 60_000 });
    entries.push({ description: `Day ${offset} late`, startedAt: dayNine(-offset) + 2 * HOUR, stoppedAt: dayNine(-offset) + 2 * HOUR + 35 * 60_000 });
  }
  entries.push({ description: "Today early", startedAt: dayNine(0) - 8 * HOUR, stoppedAt: dayNine(0) - 8 * HOUR + 20 * 60_000 });
  const now = Date.now();
  for (let index = 0; index < running; index++) {
    entries.push({ description: `Running ${index}`, startedAt: now - (index + 1) * 60_000, stoppedAt: null });
  }
  await seedServer(serverURL, {
    entries,
    timeOff: [
      { kind: "vacation", dateFrom: isoDay(dayNine(-VACATION[1]!)), dateTo: isoDay(dayNine(-VACATION[0]!)) },
      { kind: "sick", dateFrom: isoDay(dayNine(-SICK)), dateTo: isoDay(dayNine(-SICK)) },
    ],
  });
}

async function trackErrors(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const errors: string[] = [];
    (window as unknown as { rulerErrors: string[] }).rulerErrors = errors;
    window.addEventListener("error", (event) => errors.push(String(event.message)));
  });
}

async function pageErrors(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { rulerErrors: string[] }).rulerErrors);
}

async function settle(page: Page): Promise<void> {
  for (let frame = 0; frame < 4; frame++) {
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => setTimeout(resolve, 0))));
  }
}

function ruler(page: Page) {
  return page.getByRole("navigation", { name: "Days" });
}

function row(page: Page, offsetDays: number) {
  return page.locator(`.dr-day[data-iso="${isoDay(dayNine(-offsetDays))}"]`);
}

async function open(page: Page, serverURL: string): Promise<void> {
  await page.goto(serverURL + "/#/");
  await expect(page.locator(".feed .item").first()).toBeVisible();
  await expect(ruler(page)).toBeVisible();
  await settle(page);
}

// Where a day's card is, against the strip's bottom edge once it is stuck.
async function landing(page: Page, offsetDays: number): Promise<{ day: number; line: number }> {
  return page.evaluate((iso) => {
    const day = document.querySelector(`.feed > .day[data-key="${iso}"]`)!;
    const strip = document.querySelector(".pinbar.stuck");
    return { day: day.getBoundingClientRect().top, line: strip === null ? 0 : strip.getBoundingClientRect().bottom };
  }, isoDay(dayNine(-offsetDays)));
}

// The pointer somewhere over the feed, off the ruler.
async function pointAtFeed(page: Page): Promise<void> {
  await page.mouse.move(400, 500);
}

test.describe("day ruler", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("appears only on a wide window, and only once there is a day before today", async ({ page, server }) => {
    await trackErrors(page);
    await seedServer(server.url, {
      entries: [{ description: "Only today", startedAt: Date.now() - HOUR, stoppedAt: Date.now() - HOUR / 2 }],
    });
    await page.goto(server.url + "/#/");
    await expect(page.locator(".feed .item")).toHaveCount(1);
    // Nothing before today: nothing to navigate to.
    await expect(ruler(page)).toHaveCount(0);
    await seedServer(server.url, {
      entries: [{ description: "Yesterday", startedAt: dayNine(-1), stoppedAt: dayNine(-1) + HOUR }],
    });
    await triggerSync(page);
    await expect(ruler(page)).toBeVisible();
    // Below 85rem there is no room for it beside the column: not rendered at all.
    await page.setViewportSize({ width: 1300, height: 900 });
    await expect(ruler(page)).toHaveCount(0);
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(ruler(page)).toBeVisible();
    // It sits in the margin, clear of the cards.
    const cards = (await page.locator(".feed .card").first().boundingBox())!;
    const nav = (await ruler(page).boundingBox())!;
    expect(nav.x).toBeGreaterThanOrEqual(cards.x + cards.width + 8);
    expect(nav.x + nav.width).toBeLessThanOrEqual(1440);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("has a row for every calendar day, ticks by the calendar and bands like the Reports chart", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    await expect(page.locator(".dr-day")).toHaveCount(OLDEST + 1);
    await expect(page.locator(".dr-day").first()).toHaveAttribute("data-iso", isoDay(Date.now()));
    await expect(page.locator(".dr-day").first()).toHaveClass(/today/);
    await expect(page.locator(".dr-day").last()).toHaveAttribute("data-iso", isoDay(dayNine(-OLDEST)));

    const tick = (offset: number) =>
      row(page, offset).evaluate((button) => parseFloat(getComputedStyle(button, "::before").width));
    const monday = [...Array(15).keys()].map((index) => index + 1).find((offset) => {
      const date = new Date(dayNine(-offset));
      return date.getDay() === 1 && date.getDate() !== 1;
    })!;
    const plain = [...Array(8).keys()].map((index) => index + 1).find((offset) => {
      const date = new Date(dayNine(-offset));
      return date.getDay() !== 1 && date.getDate() !== 1;
    })!;
    const first = [...Array(40).keys()].map((index) => index + 1).find((offset) => new Date(dayNine(-offset)).getDate() === 1)!;
    expect(await tick(plain)).toBe(6);
    expect(await tick(monday)).toBe(11);
    expect(await tick(first)).toBe(17);

    const band = (offset: number) => row(page, offset).locator("xpath=..").getAttribute("data-band");
    expect(await band(nearest(1, true))).toBe("weekend");
    expect(await band(recordedFrom(1))).toBeNull();
    // Time off over a weekend is time off, as in the chart.
    for (let offset = VACATION[0]!; offset <= VACATION[1]!; offset++) expect(await band(offset)).toBe("vacation");
    expect(await band(SICK)).toBe("sick");

    // A day with entries carries the day header's own figure; an empty one none.
    const weekday = recordedFrom(1);
    const header = page.locator(`.feed > .day[data-key="${isoDay(dayNine(-weekday))}"] .card > .row .tracked`);
    await expect(header).toHaveText("1h 30m");
    await expect(row(page, weekday).locator(".dr-dur")).toHaveText("1h 30m");
    await expect(row(page, nearest(1, true)).locator(".dr-dur")).toHaveCount(0);
    await expect(row(page, weekday)).toHaveAccessibleName(/1h 30m, 2 entries/);
    await expect(row(page, SICK)).toHaveAccessibleName(/sick leave/);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a click jumps the feed to a day that is not mounted and lands it under the strip", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    const target = recordedFrom(50);
    await expect(page.locator(`.feed > .day[data-key="${isoDay(dayNine(-target))}"]`)).toHaveCount(0);
    await row(page, target).click();
    const day = page.locator(`.feed > .day[data-key="${isoDay(dayNine(-target))}"]`);
    await expect(day).toBeVisible();
    await expect(day).toHaveClass(/landed/);
    await settle(page);
    const landed = await landing(page, target);
    expect(landed.line).toBeGreaterThan(0);
    expect(Math.abs(landed.day - landed.line)).toBeLessThanOrEqual(1.5);
    // The ring fades and goes.
    await expect(day).not.toHaveClass(/landed/, { timeout: 3_000 });
    // The day stays put while the days around it are measured.
    await page.waitForTimeout(500);
    const later = await landing(page, target);
    expect(Math.abs(later.day - later.line)).toBeLessThanOrEqual(1.5);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("an empty day lands where it would be, today and a month header go where they say", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    // A weekend day has no card: it lands on the next older day with entries.
    const sunday = [...Array(14).keys()].map((index) => index + 10).find((offset) => new Date(dayNine(-offset)).getDay() === 0)!;
    await row(page, sunday).click();
    const friday = recordedFrom(sunday);
    await expect(page.locator(`.feed > .day[data-key="${isoDay(dayNine(-friday))}"]`)).toHaveClass(/landed/);
    await settle(page);
    const landed = await landing(page, friday);
    expect(Math.abs(landed.day - landed.line)).toBeLessThanOrEqual(1.5);

    // Today is the top of the page: the start form and the running timers.
    await page.locator(".dr-day.today").click();
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);

    // A month header: that month's newest day with entries.
    const lastMonth = new Date(new Date().getFullYear(), new Date().getMonth() - 1, 1);
    const key = `${lastMonth.getFullYear()}-${String(lastMonth.getMonth() + 1).padStart(2, "0")}`;
    const monthEnd = new Date(lastMonth.getFullYear(), lastMonth.getMonth() + 1, 0, 9).getTime();
    const newest = recordedFrom(Math.round((dayNine(0) - monthEnd) / DAY));
    await page.locator(`.dr-mbtn[data-month="${key}"]`).click();
    await expect(page.locator(`.feed > .day[data-key="${isoDay(dayNine(-newest))}"]`)).toHaveClass(/landed/);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("marks what the feed shows and follows the page, but not while the pointer is on it", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    const scroller = page.locator(".dr-scroll");
    const scrollTop = () => scroller.evaluate((element) => element.scrollTop);
    const inView = async () => {
      const box = (await page.locator(".dr-thumb").boundingBox())!;
      const frame = (await scroller.boundingBox())!;
      return box.y >= frame.y && box.y + box.height <= frame.y + frame.height;
    };

    // Deep in the feed - a jump, then the reader scrolling on from there.
    const deep = recordedFrom(110);
    await row(page, deep).click();
    await expect(page.locator(`.feed > .day[data-key="${isoDay(dayNine(-deep))}"]`)).toHaveClass(/landed/);
    await pointAtFeed(page);
    await page.evaluate(() => window.scrollBy(0, 300));
    await settle(page);

    // The marked rows are the days on screen.
    const { shown, atLine } = await page.evaluate(() => {
      const line = document.querySelector(".pinbar.stuck")?.getBoundingClientRect().bottom ?? 0;
      const days = [...document.querySelectorAll<HTMLElement>(".feed > .day")];
      // The day whose wrapper - card and the gap below it - holds the reading line.
      const holding = days.find((day) => {
        const rect = day.getBoundingClientRect();
        return rect.top <= line && rect.bottom > line;
      });
      return {
        atLine: holding?.dataset.key ?? null,
        shown: days
          .filter((day) => {
            const card = day.querySelector(".card")!.getBoundingClientRect();
            return card.bottom > line + 1 && card.top < window.innerHeight - 1;
          })
          .map((day) => day.dataset.key!),
      };
    });
    const marked = await page.locator(".dr-day[data-vis]").evaluateAll((rows) => rows.map((each) => (each as HTMLElement).dataset.iso!));
    expect(shown.length).toBeGreaterThan(0);
    for (const iso of shown) expect(marked).toContain(iso);
    // The day at the reading line is where the reader is.
    await expect(page.locator(".dr-day[aria-current='location']")).toHaveAttribute("data-iso", atLine!);
    // The page scroll brought the thumb into the ruler's view.
    expect(await inView()).toBe(true);

    // The reader takes the ruler back to today: the chip says the thumb is below.
    const frame = (await scroller.boundingBox())!;
    await page.mouse.move(frame.x + frame.width / 2, frame.y + 200);
    await page.mouse.wheel(0, -20_000);
    await expect.poll(scrollTop).toBe(0);
    const chip = page.locator(".dr-return");
    await expect(chip).toBeVisible();
    await expect(chip).toContainText("▼");
    // While the pointer is on the ruler, the page scrolling does not move it.
    await page.evaluate(() => window.scrollBy(0, 400));
    await settle(page);
    expect(await scrollTop()).toBe(0);

    // Off the ruler, the next page scroll brings it back.
    await pointAtFeed(page);
    await page.evaluate(() => window.scrollBy(0, -200));
    await settle(page);
    expect(await inView()).toBe(true);
    await expect(chip).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the wheel over the ruler scrolls the ruler, never the page, and the chip brings the thumb back", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    const scroller = page.locator(".dr-scroll");
    const box = (await scroller.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    const before = await page.evaluate(() => window.scrollY);
    for (let step = 0; step < 6; step++) await page.mouse.wheel(0, 400);
    await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(1000);
    await settle(page);
    expect(await page.evaluate(() => window.scrollY)).toBe(before);
    // The thumb is up at today: the chip points up and scrolls the ruler back.
    const chip = page.locator(".dr-return");
    await expect(chip).toBeVisible();
    await expect(chip).toContainText("▲");
    await chip.click();
    await expect(chip).toHaveCount(0);
    expect(await page.evaluate(() => window.scrollY)).toBe(before);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the keyboard walks the ruler without moving the page, and Tab after a jump enters the landed day", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    // Tab reaches the ruler once, on the day the reader is at.
    await page.getByRole("combobox", { name: "Description" }).focus();
    for (let step = 0; step < 60; step++) {
      if (await page.evaluate(() => document.activeElement?.closest("nav.dr") !== null)) break;
      await page.keyboard.press("Tab");
    }
    const focused = () => page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.iso ?? null);
    expect(await focused()).toBe(await page.locator(".dr-day[aria-current='location']").getAttribute("data-iso"));
    const scrollY = await page.evaluate(() => window.scrollY);

    await page.keyboard.press("End");
    expect(await focused()).toBe(isoDay(dayNine(-OLDEST)));
    await page.keyboard.press("Home");
    expect(await focused()).toBe(isoDay(Date.now()));
    // On the 1st, the next stop down is the previous month's header.
    await page.keyboard.press("ArrowDown");
    if ((await focused()) === null) await page.keyboard.press("ArrowDown");
    expect(await focused()).toBe(isoDay(dayNine(-1)));
    // PageDown: the same date a month older.
    const monthAgo = new Date(dayNine(-1));
    const length = new Date(monthAgo.getFullYear(), monthAgo.getMonth(), 0).getDate();
    monthAgo.setDate(1);
    monthAgo.setMonth(monthAgo.getMonth() - 1);
    monthAgo.setDate(Math.min(new Date(dayNine(-1)).getDate(), length));
    await page.keyboard.press("PageDown");
    expect(await focused()).toBe(isoDay(monthAgo.getTime()));
    // None of it scrolled the page.
    expect(await page.evaluate(() => window.scrollY)).toBe(scrollY);
    await expect(page.locator(".dr-tip")).toBeVisible();

    // Enter jumps; focus stays on the ruler, and Tab moves into the landed day.
    const target = recordedFrom(Math.round((dayNine(0) - monthAgo.getTime()) / DAY));
    for (let step = 0; step < 20 && (await focused()) !== isoDay(dayNine(-target)); step++) await page.keyboard.press("ArrowDown");
    expect(await focused()).toBe(isoDay(dayNine(-target)));
    await page.keyboard.press("Enter");
    const day = page.locator(`.feed > .day[data-key="${isoDay(dayNine(-target))}"]`);
    await expect(day).toHaveClass(/landed/);
    expect(await focused()).toBe(isoDay(dayNine(-target)));
    await page.keyboard.press("Tab");
    expect(await day.evaluate((element) => element.contains(document.activeElement))).toBe(true);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("hovering a day shows its full date and what it holds", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    const weekday = recordedFrom(1);
    await row(page, weekday).hover();
    const tip = page.locator(".dr-tip");
    await expect(tip).toBeVisible();
    const date = new Date(dayNine(-weekday));
    const full = date.toLocaleDateString("en-US", {
      weekday: "long",
      month: "long",
      day: "numeric",
      ...(date.getFullYear() === new Date().getFullYear() ? {} : { year: "numeric" }),
    });
    await expect(tip).toContainText(full);
    await expect(tip).toContainText("1h 30m");
    await expect(tip).toContainText("2 entries");
    // An empty day says where a click would take it.
    const saturday = nearest(3, true);
    await row(page, saturday).hover();
    await expect(tip).toContainText("No entries");
    await expect(tip).toContainText("jumps to");
    // Leaving the ruler hides it.
    await pointAtFeed(page);
    await expect(tip).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a sync that adds entries to an empty day updates its row and keeps the marks", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    const saturday = nearest(3, true);
    await expect(row(page, saturday)).not.toHaveClass(/rec/);
    const current = await page.locator(".dr-day[aria-current='location']").getAttribute("data-iso");
    await seedServer(server.url, {
      entries: [{ description: "Weekend fix", startedAt: dayNine(-saturday), stoppedAt: dayNine(-saturday) + 45 * 60_000 }],
    });
    await triggerSync(page);
    await expect(row(page, saturday)).toHaveClass(/rec/);
    await expect(row(page, saturday).locator(".dr-dur")).toHaveText("45m");
    await expect(page.locator(".dr-day[aria-current='location']")).toHaveCount(1);
    await expect(page.locator(".dr-day[aria-current='location']")).toHaveAttribute("data-iso", current!);
    expect(await pageErrors(page)).toEqual([]);
  });
});

test.describe("day ruler under a thumb", () => {
  test.use({ viewport: { width: 1440, height: 900 }, hasTouch: true });

  test("is not rendered without a mouse, however wide the screen", async ({ page, server }) => {
    await seedHistory(server.url);
    await page.goto(server.url + "/#/");
    await expect(page.locator(".feed .item").first()).toBeVisible();
    await settle(page);
    await expect(ruler(page)).toHaveCount(0);
  });
});
