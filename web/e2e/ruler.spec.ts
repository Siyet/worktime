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

// A wheel scroll is animated in some engines - WebKit on Linux among them - and
// goes on for a while after the position first passes a threshold. What is
// measured against the ruler waits for it to come to rest.
async function rulerAtRest(page: Page): Promise<void> {
  const scroller = page.locator(".dr-scroll");
  let last: number | null = null;
  await expect
    .poll(
      async () => {
        const now = await scroller.evaluate((element) => element.scrollTop);
        const still = now === last;
        last = now;
        return still;
      },
      { intervals: [150] },
    )
    .toBe(true);
}

function ruler(page: Page) {
  return page.getByRole("navigation", { name: "Days" });
}

function row(page: Page, offsetDays: number) {
  return page.locator(`.dr-day[data-iso="${isoDay(dayNine(-offsetDays))}"]`);
}

// `timeout` for a first sync big enough to take a while on a CI runner.
async function open(page: Page, serverURL: string, timeout?: number): Promise<void> {
  await page.goto(serverURL + "/#/");
  await expect(page.locator(".feed .item").first()).toBeVisible({ timeout });
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

function current(page: Page) {
  return page.locator(".dr-day[aria-current='location']");
}

// Whether something is drawn over the focused element's centre.
async function focusCovered(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const focus = document.activeElement as HTMLElement;
    const rect = focus.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    return hit === null || !focus.contains(hit);
  });
}

// The tooltip's title for a day in en-US: the year only outside the current one.
function tipTitle(iso: string): string {
  const date = new Date(iso + "T12:00");
  return date.toLocaleDateString("en-US", {
    weekday: "long",
    month: "long",
    day: "numeric",
    ...(date.getFullYear() === new Date().getFullYear() ? {} : { year: "numeric" }),
  });
}

test.describe("day ruler", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("appears only on a wide window, and only once there is a day before today", async ({ page, server }) => {
    await trackErrors(page);
    // Today's, whatever the time of day: an hour back would be yesterday before 1 am.
    const now = Date.now();
    const midnight = new Date(now).setHours(0, 0, 0, 0);
    await seedServer(server.url, {
      entries: [{ description: "Only today", startedAt: Math.max(midnight, now - HOUR), stoppedAt: now }],
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
    // Up to 89rem it fits without the times, which stay in the tooltip.
    const time = ruler(page).locator(".dr-dur").first();
    for (const width of [1380, 1408]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(ruler(page)).toBeVisible();
      await expect(time).toBeHidden();
    }
    for (const width of [1424, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(time).toBeVisible();
    }
    // It sits in the margin, clear of the cards, at every width it is shown at.
    for (const width of [1380, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      const cards = (await page.locator(".feed .card").first().boundingBox())!;
      const nav = (await ruler(page).boundingBox())!;
      expect(nav.x).toBeGreaterThanOrEqual(cards.x + cards.width + 8);
      expect(nav.x + nav.width).toBeLessThanOrEqual(width);
    }
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
    expect(await tick(plain)).toBe(12);
    expect(await tick(monday)).toBe(20);
    expect(await tick(first)).toBe(28);
    // Every tick ends flush with the right edge of the window - at the page's
    // scrollbar gutter, where the platform reserves one.
    const edge = (offset: number) =>
      row(page, offset).evaluate((button) => button.getBoundingClientRect().right - parseFloat(getComputedStyle(button, "::before").right));
    const windowEdge = await page.evaluate(() => {
      const probe = document.createElement("div");
      probe.style.cssText = "position: fixed; right: 0; width: 0; height: 0";
      document.body.append(probe);
      const right = probe.getBoundingClientRect().right;
      probe.remove();
      return right;
    });
    expect(windowEdge).toBeGreaterThan(page.viewportSize()!.width - 20);
    for (const offset of [plain, monday, first]) expect(await edge(offset)).toBe(windowEdge);

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
    await expect(page.locator(".dr-day.today")).toHaveAccessibleName(/20m, 1 entry$/);
    await expect(row(page, SICK)).toHaveAccessibleName(/sick leave/);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a day with work in parallel shows the header's clock time, the figure before the slash", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    // Inside the day's first entry: 40 more minutes tracked, not one more on the clock.
    const weekday = recordedFrom(1);
    await seedServer(server.url, {
      entries: [{ description: "In parallel", startedAt: dayNine(-weekday) + 10 * 60_000, stoppedAt: dayNine(-weekday) + 50 * 60_000 }],
    });
    await open(page, server.url);
    const header = page.locator(`.feed > .day[data-key="${isoDay(dayNine(-weekday))}"] .card > .row`);
    await expect(header.locator(".wall")).toHaveText("1h 30m");
    await expect(header.locator(".tracked")).toHaveText("2h 10m");
    await expect(row(page, weekday).locator(".dr-dur")).toHaveText("1h 30m");
    // The tooltip and the name add the header's second figure.
    await expect(row(page, weekday)).toHaveAccessibleName(/1h 30m, 2h 10m tracked - work that ran in parallel is counted once, 3 entries/);
    await row(page, weekday).hover();
    await expect(page.locator(".dr-tip .ts")).toHaveText(["1h 30m · 3 entries", "2h 10m tracked - work that ran in parallel is counted once"]);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("rows keep the model's height whatever the browser's font size", async ({ page, server, browserName }) => {
    test.skip(browserName !== "chromium", "the default font size is set through the Chromium protocol");
    await trackErrors(page);
    await seedHistory(server.url);
    // A reader's larger default font: rem-sized rows would outgrow the model's
    // offsets, and following the page would lose the day at the reading line.
    const session = await page.context().newCDPSession(page);
    await session.send("Page.setFontSizes", { fontSizes: { standard: 20, fixed: 16 } });
    // 85rem is 1700px at that size.
    await page.setViewportSize({ width: 1920, height: 900 });
    await open(page, server.url);
    expect(await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize))).toBe(20);
    const height = (selector: string) => page.locator(selector).first().evaluate((element) => element.getBoundingClientRect().height);
    expect(await height(".dr-li")).toBe(20);
    expect(await height(".dr-mhead")).toBe(28);
    // A whole month, header and rows: the model's height for it. The first month
    // is the current one, which never has a year row above it.
    const month = page.locator(".dr-month").first();
    const rows = await month.locator(".dr-li").count();
    expect(await month.evaluate((element) => element.getBoundingClientRect().height)).toBe(28 + rows * 20);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a row under the pointer comes up to full strength: its tick grows, its labels brighten", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    // A day with entries, whose date once stood out bright at rest.
    const target = row(page, recordedFrom(20));
    const tick = () =>
      target.evaluate((button) => {
        const style = getComputedStyle(button, "::before");
        return { width: parseFloat(style.width), opacity: Number(style.opacity) };
      });
    const labels = () =>
      target.evaluate((button) =>
        [".dr-wd", ".dr-n", ".dr-dur"].map((selector) => getComputedStyle(button.querySelector(selector)!).color),
      );
    const text = await page.evaluate(() => {
      const probe = document.createElement("span");
      probe.style.color = "var(--text)";
      document.body.append(probe);
      const color = getComputedStyle(probe).color;
      probe.remove();
      return color;
    });
    const rest = await tick();
    expect(rest.opacity).toBeLessThan(1);
    // At rest the weekday, the date and the time are one muted colour.
    const muted = await labels();
    expect(new Set(muted).size).toBe(1);
    expect(muted[0]).not.toBe(text);
    await target.hover();
    // Past the transition: the tick settles 10px longer at full opacity, and
    // the labels in the text colour.
    await expect.poll(tick).toEqual({ width: rest.width + 10, opacity: 1 });
    await expect.poll(labels).toEqual([text, text, text]);
    await pointAtFeed(page);
    await expect.poll(tick).toEqual(rest);
    await expect.poll(labels).toEqual(muted);
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
    // The ruler says the reader is at the landed day, not at the one above it.
    await expect(current(page)).toHaveAttribute("data-iso", isoDay(dayNine(-target)));
    // Focus went where the click landed, as an in-page link takes it: the keys
    // scroll the page, never the ruler, and Tab goes on from the day.
    expect(await day.evaluate((element) => element === document.activeElement)).toBe(true);
    const scrollY = () => page.evaluate(() => window.scrollY);
    const rulerTop = () => page.locator(".dr-scroll").evaluate((element) => element.scrollTop);
    const ruled = await rulerTop();
    let before = await scrollY();
    await page.keyboard.press("ArrowDown");
    await expect.poll(scrollY).toBeGreaterThan(before);
    before = await scrollY();
    await page.keyboard.press("PageDown");
    await expect.poll(scrollY).toBeGreaterThan(before + 100);
    before = await scrollY();
    await page.keyboard.press("End");
    await expect.poll(scrollY).toBeGreaterThan(before + 100);
    expect(await rulerTop()).toBe(ruled);
    await row(page, target).click();
    await expect(day).toHaveClass(/landed/);
    await page.keyboard.press("Tab");
    expect(await day.evaluate((element) => element !== document.activeElement && element.contains(document.activeElement))).toBe(true);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the first jump far back after loading lands on the day and stays there", async ({ page, server }) => {
    test.slow();
    await trackErrors(page);
    // Two and a half years of days two to nine entries tall. Measuring the day a
    // jump mounts moves the estimate every unmeasured day above it stands in for,
    // hundreds of them: the window must stay on the day through that, and
    // through the days above it being measured afterwards.
    const entries = [];
    for (let offset = 1; offset <= 900; offset++) {
      if (weekend(-offset)) continue;
      const count = 2 + ((offset * 7) % 8);
      for (let index = 0; index < count; index++) {
        const start = dayNine(-offset) + index * 45 * 60_000;
        entries.push({ description: `Day ${offset} task ${index}`, startedAt: start, stoppedAt: start + 40 * 60_000 });
      }
    }
    await seedServer(server.url, { entries, timeOff: [] });
    await open(page, server.url, 30_000);
    const target = nearest(400, false);
    const iso = isoDay(dayNine(-target));
    await row(page, target).click();
    const day = page.locator(`.feed > .day[data-key="${iso}"]`);
    await expect(day).toHaveClass(/landed/);
    await settle(page);
    let landed = await landing(page, target);
    expect(Math.abs(landed.day - landed.line)).toBeLessThanOrEqual(1.5);
    await expect(current(page)).toHaveAttribute("data-iso", iso);
    // The days above are measured while the reader is idle; the day stays put.
    await page.waitForTimeout(3_000);
    await settle(page);
    await expect(day).toBeVisible();
    landed = await landing(page, target);
    expect(Math.abs(landed.day - landed.line)).toBeLessThanOrEqual(1.5);
    await expect(current(page)).toHaveAttribute("data-iso", iso);

    // The reader scrolls on right after another far jump: whatever row the
    // scroll stopped at stays at the line while the days above are measured.
    const atLine = () =>
      page.evaluate(() => {
        const line = document.querySelector(".pinbar.stuck")?.getBoundingClientRect().bottom ?? 0;
        const rows = [...document.querySelectorAll<HTMLElement>(".feed > .day [data-anchor]")];
        const row = rows.find((each) => each.getBoundingClientRect().bottom > line)!;
        return { text: row.textContent, offset: row.getBoundingClientRect().top - line };
      });
    // The same row, however far it moved - null once it is not mounted.
    const offsetOf = (text: string | null) =>
      page.evaluate((text) => {
        const line = document.querySelector(".pinbar.stuck")?.getBoundingClientRect().bottom ?? 0;
        const rows = [...document.querySelectorAll<HTMLElement>(".feed > .day [data-anchor]")];
        const row = rows.find((each) => each.textContent === text);
        return row === undefined ? null : row.getBoundingClientRect().top - line;
      }, text);
    await row(page, nearest(600, false)).click();
    await pointAtFeed(page);
    await page.mouse.wheel(0, 700);
    await page.waitForTimeout(300);
    await settle(page);
    const scrolled = await atLine();
    await page.waitForTimeout(3_000);
    await settle(page);
    const later = await offsetOf(scrolled.text);
    expect(later, `row "${scrolled.text}" at ${scrolled.offset} is gone`).not.toBeNull();
    expect(Math.abs(later! - scrolled.offset), `row "${scrolled.text}" moved from ${scrolled.offset} to ${later}`).toBeLessThanOrEqual(1.5);

    // Home and End while the days above a far jump are still being measured
    // go straight to the ends of the page - End to the end it had then.
    await row(page, nearest(750, false)).click();
    await page.waitForTimeout(700);
    await page.keyboard.press("Home");
    await expect.poll(() => page.evaluate(() => window.scrollY), { timeout: 1_500 }).toBe(0);
    await row(page, nearest(500, false)).click();
    await page.waitForTimeout(700);
    // Read straight after the scroller's own handler, before any frame corrects
    // the page for the days above being measured.
    await page.evaluate(() => {
      const record = (event: KeyboardEvent) => {
        if (event.key !== "End") return;
        (window as unknown as { endGap: number }).endGap = document.documentElement.scrollHeight - window.innerHeight - window.scrollY;
        window.removeEventListener("keydown", record);
      };
      window.addEventListener("keydown", record);
    });
    await page.keyboard.press("End");
    expect(await page.evaluate(() => (window as unknown as { endGap: number }).endGap)).toBeLessThanOrEqual(2);
    // More history loads below, and the days above are measured - the page's
    // scroll position moves with the corrections, but what the reader sees
    // does not: End does not chase the growing bottom.
    await settle(page);
    const ended = await atLine();
    await page.waitForTimeout(1_000);
    await settle(page);
    const after = await offsetOf(ended.text);
    expect(after, `row "${ended.text}" at ${ended.offset} is gone`).not.toBeNull();
    expect(Math.abs(after! - ended.offset), `row "${ended.text}" moved from ${ended.offset} to ${after}`).toBeLessThanOrEqual(1.5);

    // The oldest day: the page ends before it reaches the line, and it stays put.
    const oldest = page.locator(".dr-day").last();
    const oldestISO = (await oldest.getAttribute("data-iso"))!;
    await oldest.click();
    const bottomDay = page.locator(`.feed > .day[data-key="${oldestISO}"]`);
    await expect(bottomDay).toHaveClass(/landed/);
    await settle(page);
    const first = (await bottomDay.boundingBox())!.y;
    await page.waitForTimeout(3_000);
    await settle(page);
    expect(Math.abs((await bottomDay.boundingBox())!.y - first)).toBeLessThanOrEqual(1.5);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("with no timer running, the keys after a mouse jump scroll the page and Tab goes on from where it landed", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    const scrollY = () => page.evaluate(() => window.scrollY);
    const rulerTop = () => page.locator(".dr-scroll").evaluate((element) => element.scrollTop);
    const target = recordedFrom(40);
    await row(page, target).click();
    const day = page.locator(`.feed > .day[data-key="${isoDay(dayNine(-target))}"]`);
    await expect(day).toHaveClass(/landed/);
    // The pointer stays on the ruler, so the ruler does not follow the page:
    // anything that moves it now is a key that went to it. WebKit can spend the
    // first arrow after a mouse click without scrolling anything; the second
    // must move the page.
    const press = async (key: string) => {
      const before = await scrollY();
      for (let attempt = 0; attempt < 2 && (await scrollY()) === before; attempt++) {
        await page.keyboard.press(key);
        await page.waitForTimeout(300);
      }
      expect(await scrollY(), key).toBeGreaterThan(before);
    };
    const ruled = await rulerTop();
    for (const key of ["ArrowDown", "Space", "End"]) await press(key);
    expect(await rulerTop()).toBe(ruled);
    // End scrolls smoothly; the reader waits for the page to stop.
    await expect.poll(async () => {
      const before = await scrollY();
      await settle(page);
      return (await scrollY()) - before;
    }).toBe(0);

    // Today is the top: the arrows scroll the page from there, and Tab goes on
    // into the start form.
    await page.locator(".dr-day.today").click();
    await expect.poll(scrollY).toBe(0);
    const top = await rulerTop();
    await press("ArrowDown");
    expect(await rulerTop()).toBe(top);
    await page.keyboard.press("Tab");
    await expect(page.getByRole("combobox", { name: "Description" })).toBeFocused();
    expect(await pageErrors(page)).toEqual([]);
  });

  test("an empty day lands where it would be, today and a month header go where they say", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url, 2);
    await open(page, server.url);
    // Something typed in the start form, focus still there.
    const description = page.getByRole("combobox", { name: "Description" });
    await description.fill("half a thought");
    // A weekend day has no card: it lands on the next older day with entries.
    const sunday = [...Array(14).keys()].map((index) => index + 10).find((offset) => new Date(dayNine(-offset)).getDay() === 0)!;
    await row(page, sunday).click();
    const friday = recordedFrom(sunday);
    await expect(page.locator(`.feed > .day[data-key="${isoDay(dayNine(-friday))}"]`)).toHaveClass(/landed/);
    await settle(page);
    const landed = await landing(page, friday);
    expect(Math.abs(landed.day - landed.line)).toBeLessThanOrEqual(1.5);
    await expect(current(page)).toHaveAttribute("data-iso", isoDay(dayNine(-friday)));
    // The field let go: the next Space scrolls the page instead of typing.
    const landedAt = await page.evaluate(() => window.scrollY);
    await page.keyboard.press("Space");
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(landedAt + 100);
    await expect(description).toHaveValue("half a thought");

    // Today is the top of the page: the start form and the running timers. The
    // Space step may still be under way; the jump cuts it short for good.
    await page.locator(".dr-day.today").click();
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => window.scrollY)).toBe(0);

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
      const lit = page.locator(".dr-day[data-vis]");
      const first = (await lit.first().boundingBox())!;
      const last = (await lit.last().boundingBox())!;
      const frame = (await scroller.boundingBox())!;
      return first.y >= frame.y && last.y + last.height <= frame.y + frame.height;
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
    // The page scroll brought the lit ticks into the ruler's view.
    expect(await inView()).toBe(true);

    // The reader takes the ruler back to today: the chip says the lit ticks are below.
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

  test("the wheel over the ruler scrolls the ruler, never the page, and the chip brings the lit ticks back", async ({ page, server }) => {
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
    // The lit ticks are up at today: the chip points up and scrolls the ruler back.
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
    // Far from the lit ticks, and no chip over the rows focus moves to.
    await expect(page.locator(".dr-return")).toHaveCount(0);
    await page.keyboard.press("PageUp");
    expect(await focusCovered(page)).toBe(false);
    await page.keyboard.press("Shift+PageUp");
    expect(await focusCovered(page)).toBe(false);
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
    await settle(page);
    await expect(current(page)).toHaveAttribute("data-iso", isoDay(dayNine(-target)));
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
    const full = tipTitle(isoDay(dayNine(-weekday)));
    await expect(tip).toContainText(full);
    await expect(tip).toContainText("1h 30m");
    await expect(tip).toContainText("2 entries");
    // An empty day says where a click would take it.
    const saturday = nearest(3, true);
    await row(page, saturday).hover();
    await expect(tip).toContainText("No entries");
    await expect(tip).toContainText("jumps to");
    // Leaving and coming straight back keeps it.
    await row(page, weekday).hover();
    await expect(tip).toContainText(full);
    await pointAtFeed(page);
    await row(page, weekday).hover();
    await page.waitForTimeout(400);
    await expect(tip).toContainText(full);

    // The rows scroll under the resting pointer: it describes the row now under it.
    const box = (await row(page, weekday).boundingBox())!;
    const point = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
    await page.mouse.move(point.x, point.y);
    await page.mouse.wheel(0, 320);
    await expect.poll(() => page.locator(".dr-scroll").evaluate((element) => element.scrollTop)).toBeGreaterThan(200);
    await rulerAtRest(page);
    await settle(page);
    const under = await page.evaluate(({ x, y }) => {
      const stop = document.elementFromPoint(x, y)?.closest<HTMLElement>(".dr-day, .dr-mbtn");
      return { iso: stop?.dataset.iso ?? null, month: stop?.dataset.month ?? null };
    }, point);
    if (under.iso !== null) await expect(tip.locator(".tt")).toHaveText(new RegExp(tipTitle(under.iso)));
    else if (under.month !== null) {
      const [year, month] = under.month.split("-").map(Number);
      const name = new Date(year!, month! - 1, 1).toLocaleDateString("en-US", { month: "long", year: "numeric" });
      await expect(tip.locator(".tt")).toHaveText(name);
    }

    // Leaving the ruler hides it.
    await pointAtFeed(page);
    await expect(tip).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the keyboard's tooltip stays on the focused day with the pointer resting on the ruler", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    await page.getByRole("combobox", { name: "Description" }).focus();
    for (let step = 0; step < 60; step++) {
      if (await page.evaluate(() => document.activeElement?.closest("nav.dr") !== null)) break;
      await page.keyboard.press("Tab");
    }
    const box = (await page.locator(".dr-scroll").boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.waitForTimeout(400);
    const title = page.locator(".dr-tip .tt");
    for (let step = 0; step < 3; step++) {
      await page.keyboard.press("PageDown");
      await settle(page);
      const focused = await page.evaluate(() => (document.activeElement as HTMLElement).dataset.iso!);
      await expect(title).toHaveText(tipTitle(focused));
    }
    expect(await pageErrors(page)).toEqual([]);
  });

  test("the chip never covers a month or a year header scrolling in", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    // Late January, with a screenful of January days: the history reaches back
    // into a past year, and the lit ticks stay up in January.
    const year = new Date().getFullYear() + 1;
    const january = [];
    for (let date = 4; date <= 19; date++) {
      const day = new Date(year, 0, date, 9).getTime();
      if ([0, 6].includes(new Date(day).getDay())) continue;
      // Four rows a day: a screenful even with a platform's smaller fonts.
      for (let index = 0; index < 4; index++) {
        const start = day + index * HOUR;
        january.push({ description: `January ${date} task ${index}`, startedAt: start, stoppedAt: start + 30 * 60_000 });
      }
    }
    await seedServer(server.url, { entries: january });
    await page.clock.setFixedTime(new Date(year, 0, 20, 12));
    await open(page, server.url);
    // The feed at the top, the ruler scrolled so that a month's last row sits
    // half under the stuck header: the chip shows, and the next month's header
    // (and a past year's row) must stay in view above it.
    // A month's block: a past year's first month comes under its year's row.
    const scrollTo = (key: string, heads: number) =>
      page.evaluate(
        ({ key, heads }) => {
          const section = document.querySelector(`.dr-mbtn[data-month="${key}"]`)!.closest(".dr-month")!;
          const year = section.parentElement!;
          const block = year.classList.contains("past") && section === year.querySelector(".dr-month") ? year : section;
          const scroller = document.querySelector<HTMLElement>(".dr-scroll")!;
          const origin = scroller.getBoundingClientRect().top;
          scroller.scrollTop = block.getBoundingClientRect().top - origin + scroller.scrollTop - 16 - heads + 8;
        },
        { key, heads },
      );
    // The block's headers in view whose centres something else is drawn over.
    const hidden = (key: string) =>
      page.evaluate((key) => {
        const section = document.querySelector(`.dr-mbtn[data-month="${key}"]`)!.closest(".dr-month")!;
        const year = section.parentElement!;
        const block = year.classList.contains("past") && section === year.querySelector(".dr-month") ? year : section;
        const origin = document.querySelector<HTMLElement>(".dr-scroll")!.getBoundingClientRect().top;
        return [...block.querySelectorAll<HTMLElement>(":scope > .dr-yhead, .dr-mhead")]
          .slice(0, 2)
          .filter((head) => {
            const rect = head.getBoundingClientRect();
            if (rect.bottom <= origin + 24) return false;
            const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
            return hit === null || !head.contains(hit);
          })
          .map((head) => head.textContent!.trim());
      }, key);
    // A past year's month sticks under its year's row: two headers.
    for (const [key, heads] of [[`${year - 1}-12`, 24], [`${year - 1}-11`, 48]] as const) {
      await scrollTo(key, heads);
      // The chip comes with the ruler's scroll event, which some engines send a
      // few frames late.
      await expect(page.locator(".dr-return"), key).toBeVisible();
      await settle(page);
      expect(await hidden(key), key).toEqual([]);
    }
    expect(await pageErrors(page)).toEqual([]);
  });

  test("a ruler the reader scrolled away stays there through a sync, until the page scrolls", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    await open(page, server.url);
    const scroller = page.locator(".dr-scroll");
    const scrollTop = () => scroller.evaluate((element) => element.scrollTop);
    const box = (await scroller.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    for (let step = 0; step < 6; step++) await page.mouse.wheel(0, 400);
    await expect.poll(scrollTop).toBeGreaterThan(1500);
    await rulerAtRest(page);
    await pointAtFeed(page);
    await settle(page);
    const explored = await scrollTop();
    // Still where the wheel took it: nothing snapped it back before the baseline.
    expect(explored).toBeGreaterThan(1500);

    // A sync changes an old day and rebuilds the rows; the ruler stays put.
    const saturday = nearest(100, true);
    await seedServer(server.url, {
      entries: [{ description: "Old weekend fix", startedAt: dayNine(-saturday), stoppedAt: dayNine(-saturday) + 45 * 60_000 }],
    });
    await triggerSync(page);
    await expect(row(page, saturday)).toHaveClass(/rec/);
    await settle(page);
    expect(await scrollTop()).toBe(explored);

    // The next page scroll brings the lit ticks back into view.
    await page.evaluate(() => window.scrollBy(0, 200));
    await settle(page);
    expect(await scrollTop()).toBeLessThan(explored);
    await expect(page.locator(".dr-return")).toHaveCount(0);
    expect(await pageErrors(page)).toEqual([]);
  });

  test("midnight adds a row at the top and the rows under the reader stay put", async ({ page, server }) => {
    await trackErrors(page);
    await seedHistory(server.url);
    // New Year's Eve: the next day is a new month and a new year as well.
    const year = new Date().getFullYear();
    await page.clock.setFixedTime(new Date(year, 11, 31, 23, 59, 50));
    await open(page, server.url);
    await expect(page.locator(".dr-day").first()).toHaveAttribute("data-iso", `${year}-12-31`);
    const scroller = page.locator(".dr-scroll");
    const box = (await scroller.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.wheel(0, 1200);
    await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(800);
    await rulerAtRest(page);
    await settle(page);
    const watched = row(page, recordedFrom(40));
    const before = (await watched.boundingBox())!.y;

    await page.clock.setFixedTime(new Date(year + 1, 0, 1, 0, 0, 5));
    await expect(page.locator(".dr-day").first()).toHaveAttribute("data-iso", `${year + 1}-01-01`);
    await expect(page.locator(".dr-day").first()).toHaveClass(/today/);
    await expect(page.locator(`.dr-mbtn[data-month="${year + 1}-01"]`)).toHaveCount(1);
    await expect(page.locator(".dr-yhead", { hasText: String(year) })).toHaveCount(1);
    await settle(page);
    expect(Math.abs((await watched.boundingBox())!.y - before)).toBeLessThanOrEqual(0.5);
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
