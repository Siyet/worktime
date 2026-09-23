import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import {
  dayOffsets,
  estimateDayHeight,
  FEED_CHUNK_DAYS,
  FEED_DEFAULT_DAY_HEIGHT,
  feedDays,
  planFeedWindow,
  resolveWindow,
  windowKeys,
  type FeedDay,
  type FeedWindow,
} from "./feed";
import type { TimeEntry } from "./types";

const HOUR = 3_600_000;

let counter = 0;

function entry(startedAt: number, overrides: Partial<TimeEntry> = {}): TimeEntry {
  counter += 1;
  return {
    id: `entry-${counter}`,
    project_id: null,
    description: "work",
    tags: [],
    started_at: startedAt,
    stopped_at: startedAt + HOUR,
    created_at: startedAt,
    updated_at: startedAt,
    deleted_at: null,
    ...overrides,
  };
}

// Local wall-clock time, so the test reads the same in every timezone.
function at(isoLocal: string): number {
  return new Date(isoLocal).getTime();
}

function day(iso: string): FeedDay {
  return { iso, entries: [] };
}

// Days newest first, like feedDays returns them: 2026-07-30 back to 2026-07-01.
const MONTH = Array.from({ length: 30 }, (_, index) => day(`2026-07-${String(30 - index).padStart(2, "0")}`));

describe("feedDays", () => {
  it("buckets finished entries by local day, newest day and newest entry first", () => {
    const days = feedDays([
      entry(at("2026-07-01T09:00")),
      entry(at("2026-07-02T15:00")),
      entry(at("2026-07-01T14:00")),
      entry(at("2026-07-02T08:00")),
    ]);
    expect(days.map((feedDay) => feedDay.iso)).toEqual(["2026-07-02", "2026-07-01"]);
    expect(days[0]!.entries.map((item) => item.started_at)).toEqual([at("2026-07-02T15:00"), at("2026-07-02T08:00")]);
    expect(days[1]!.entries.map((item) => item.started_at)).toEqual([at("2026-07-01T14:00"), at("2026-07-01T09:00")]);
  });

  it("leaves running entries to the running card", () => {
    const days = feedDays([entry(at("2026-07-01T09:00"), { stopped_at: null }), entry(at("2026-07-01T10:00"))]);
    expect(days).toHaveLength(1);
    expect(days[0]!.entries).toHaveLength(1);
  });

  it("puts entries that interleave days back into their own day", () => {
    // The same-day shortcut must not glue an entry onto the day it happens to follow.
    const days = feedDays([
      entry(at("2026-07-01T23:59")),
      entry(at("2026-07-02T00:00")),
      entry(at("2026-07-01T00:00")),
      entry(at("2026-07-02T23:59")),
    ]);
    expect(days.map((feedDay) => [feedDay.iso, feedDay.entries.length])).toEqual([
      ["2026-07-02", 2],
      ["2026-07-01", 2],
    ]);
  });

  it("returns nothing for no finished entries", () => {
    expect(feedDays([])).toEqual([]);
  });

  describe("across daylight saving changes", () => {
    beforeAll(() => {
      vi.stubEnv("TZ", "Europe/Berlin");
    });
    afterAll(() => {
      vi.unstubAllEnvs();
    });

    it("ends a 23-hour day at the next midnight, not 24 hours later", () => {
      // Guards the test itself: without the zone switch there is no DST day here.
      expect(new Date(at("2026-03-30T00:30")).getTimezoneOffset()).toBe(-120);
      // Clocks go forward on 2026-03-29; +24h from its midnight is 01:00 on the 30th.
      const days = feedDays([
        entry(at("2026-03-29T00:30")),
        entry(at("2026-03-29T23:30")),
        entry(at("2026-03-30T00:30")),
      ]);
      expect(days.map((feedDay) => [feedDay.iso, feedDay.entries.length])).toEqual([
        ["2026-03-30", 1],
        ["2026-03-29", 2],
      ]);
    });

    it("keeps the last hour of a 25-hour day on that day", () => {
      // Clocks go back on 2026-10-25; +24h from its midnight is 23:00 the same day.
      const days = feedDays([entry(at("2026-10-25T00:30")), entry(at("2026-10-25T23:30"))]);
      expect(days.map((feedDay) => [feedDay.iso, feedDay.entries.length])).toEqual([["2026-10-25", 2]]);
    });
  });
});

describe("dayOffsets", () => {
  it("stands in the mean of the measured days for the unmeasured ones", () => {
    expect(estimateDayHeight([100, null, 300])).toBe(200);
    expect(dayOffsets([100, null, 300])).toEqual([0, 100, 300, 600]);
  });

  it("falls back to a default before anything has been measured", () => {
    expect(estimateDayHeight([null, null])).toBe(FEED_DEFAULT_DAY_HEIGHT);
  });
});

describe("planFeedWindow", () => {
  // Days of 100px each, in an 800px viewport.
  const VIEWPORT = 800;
  const heights = (count: number) => Array.from({ length: count }, () => 100);

  function plan(window: FeedWindow, loadedHeights: (number | null)[], viewTop: number, total = 1000): FeedWindow {
    return planFeedWindow({ window, total, heights: loadedHeights, viewTop, viewBottom: viewTop + VIEWPORT });
  }

  it("starts with the first chunk", () => {
    expect(plan({ first: 0, last: -1, loaded: 0 }, [], 0)).toEqual({ first: 0, last: FEED_CHUNK_DAYS - 1, loaded: FEED_CHUNK_DAYS });
    expect(plan({ first: 0, last: -1, loaded: 0 }, [], 0, 3)).toEqual({ first: 0, last: 2, loaded: 3 });
  });

  it("loads the next chunk as the bottom comes within reach, and mounts it", () => {
    // 15 days = 1500px; the viewport bottom at 800 leaves 700, under 1.5 viewports.
    expect(plan({ first: 0, last: 14, loaded: 15 }, heights(15), 0)).toEqual({
      first: 0,
      last: 15 + FEED_CHUNK_DAYS - 1,
      loaded: 15 + FEED_CHUNK_DAYS,
    });
  });

  it("does not load while the bottom is still far away", () => {
    expect(plan({ first: 0, last: 29, loaded: 30 }, heights(30), 0).loaded).toBe(30);
  });

  it("never advances past a frontier that has not been measured yet", () => {
    // Advancing on a guessed bottom would load the whole history in a burst.
    const unmeasured = [...heights(9), null];
    expect(plan({ first: 0, last: 9, loaded: 10 }, unmeasured, 0).loaded).toBe(10);
  });

  it("stops at the end of the history", () => {
    expect(plan({ first: 0, last: 9, loaded: 10 }, heights(10), 0, 10).loaded).toBe(10);
    expect(plan({ first: 0, last: 9, loaded: 10 }, heights(10), 0, 12).loaded).toBe(12);
  });

  it("keeps days mounted until they are further than the far range", () => {
    // Viewport over days 20-27; near reaches up to day 8, far up to day 0.
    expect(plan({ first: 0, last: 29, loaded: 30 }, heights(30), 2000).first).toBe(0);
    // Scrolled on to days 30-37: far starts at day 6, so days 0-5 go.
    expect(plan({ first: 0, last: 49, loaded: 50 }, heights(50), 3000).first).toBe(6);
  });

  it("mounts days coming back within the near range", () => {
    // Mounted 20-49, reader scrolls up to day 18: near reaches up to day 6.
    expect(plan({ first: 20, last: 49, loaded: 50 }, heights(50), 1800).first).toBe(6);
  });

  it("unmounts below only beyond the far range as well", () => {
    // Mounted 0-49, reader at the top: far reaches down to day 32.
    expect(plan({ first: 0, last: 49, loaded: 50 }, heights(50), 0).last).toBe(32);
    // Mounted 0-25: still inside far, nothing to mount or unmount.
    expect(plan({ first: 0, last: 25, loaded: 50 }, heights(50), 0).last).toBe(25);
  });

  it("starts over from the near range after a jump away from the mounted days", () => {
    // Mounted 0-10, then a scrollbar drag to day 200.
    expect(plan({ first: 0, last: 10, loaded: 300 }, heights(300), 20_000)).toEqual({ first: 188, last: 220, loaded: 300 });
  });

  it("clamps at the top while the form is still on screen", () => {
    expect(plan({ first: 0, last: 9, loaded: 30 }, heights(30), -400).first).toBe(0);
  });

  it("clamps at the bottom past the end of the history", () => {
    const window = plan({ first: 0, last: 9, loaded: 10 }, heights(10), 5000, 10);
    expect(window.last).toBe(9);
    expect(window.first).toBeLessThanOrEqual(window.last);
  });
});

describe("resolveWindow and windowKeys", () => {
  it("resolves the initial keys to the first chunk", () => {
    expect(resolveWindow(MONTH, { first: null, last: null, frontier: null })).toEqual({
      first: 0,
      last: FEED_CHUNK_DAYS - 1,
      loaded: FEED_CHUNK_DAYS,
    });
    expect(resolveWindow([], { first: null, last: null, frontier: null })).toEqual({ first: 0, last: -1, loaded: 0 });
  });

  it("round-trips a window through its keys", () => {
    const window = { first: 3, last: 12, loaded: 20 };
    expect(windowKeys(MONTH, window)).toEqual({ first: "2026-07-27", last: "2026-07-18", frontier: "2026-07-11" });
    expect(resolveWindow(MONTH, windowKeys(MONTH, window))).toEqual(window);
  });

  it("keeps a window that starts at the newest day open to a newer one", () => {
    const keys = windowKeys(MONTH, { first: 0, last: 9, loaded: 10 });
    expect(keys.first).toBeNull();
    const withToday = [day("2026-07-31"), ...MONTH];
    expect(resolveWindow(withToday, keys)).toEqual({ first: 0, last: 10, loaded: 11 });
  });

  it("keeps the same days mounted when a day appears above them", () => {
    const keys = windowKeys(MONTH, { first: 5, last: 9, loaded: 15 });
    const withToday = [day("2026-07-31"), ...MONTH];
    const window = resolveWindow(withToday, keys);
    expect(window).toEqual({ first: 6, last: 10, loaded: 16 });
    expect(withToday.slice(window.first, window.last + 1).map((feedDay) => feedDay.iso)).toEqual(
      MONTH.slice(5, 10).map((feedDay) => feedDay.iso),
    );
  });

  it("resolves deleted edge days to their neighbours on the inside", () => {
    const keys = windowKeys(MONTH, { first: 5, last: 9, loaded: 15 });
    const withoutEdges = MONTH.filter((feedDay) => feedDay.iso !== keys.first && feedDay.iso !== keys.last);
    const window = resolveWindow(withoutEdges, keys);
    expect(withoutEdges.slice(window.first, window.last + 1).map((feedDay) => feedDay.iso)).toEqual(
      MONTH.slice(6, 9).map((feedDay) => feedDay.iso),
    );
    expect(window.loaded).toBe(13);
  });

  it("mounts nothing when both edge days of a two-day window are deleted", () => {
    const keys = windowKeys(MONTH, { first: 5, last: 6, loaded: 15 });
    const withoutBoth = MONTH.filter((feedDay) => feedDay.iso !== keys.first && feedDay.iso !== keys.last);
    const window = resolveWindow(withoutBoth, keys);
    expect(window.first).toBeGreaterThan(window.last);
  });

  it("loads nothing when every loaded day is gone, so the next plan starts over", () => {
    const keys = windowKeys(MONTH, { first: 0, last: 4, loaded: 5 });
    const older = MONTH.slice(5);
    expect(resolveWindow(older, keys).loaded).toBe(0);
  });
});
