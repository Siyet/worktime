import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { feedDays } from "./feed";
import { expandTimeOff } from "./report";
import { RULER_HEAD, RULER_PITCH, buildRuler, rowFrom, rowUntil, rulerOffset, stepMonth, type RulerModel } from "./ruler";
import type { TimeEntry, TimeOff, TimeOffKind } from "./types";

const MINUTE = 60_000;

let counter = 0;

// A finished entry on a local calendar day, so the test reads the same in
// every timezone.
function entry(dayISO: string, hour: number, minutes: number): TimeEntry {
  counter += 1;
  const startedAt = new Date(`${dayISO}T${String(hour).padStart(2, "0")}:00`).getTime();
  return {
    id: `entry-${counter}`,
    project_id: null,
    description: `work ${counter}`,
    tags: [],
    started_at: startedAt,
    stopped_at: startedAt + minutes * MINUTE,
    created_at: startedAt,
    updated_at: startedAt,
    deleted_at: null,
  };
}

function timeOff(kind: TimeOffKind, from: string, to: string): TimeOff {
  counter += 1;
  return { id: `off-${counter}`, kind, date_from: from, date_to: to, note: "", created_at: 0, updated_at: 0, deleted_at: null };
}

function ruler(entries: TimeEntry[], todayISO: string, off: TimeOff[] = []): RulerModel {
  return buildRuler(feedDays(entries), expandTimeOff(off), todayISO);
}

// Thursday 2026-09-24 is today; the history starts on Friday 2026-08-28.
const TODAY = "2026-09-24";
const history = [
  entry("2026-09-24", 9, 50),
  entry("2026-09-23", 9, 120),
  entry("2026-09-23", 13, 35),
  entry("2026-09-21", 10, 60),
  entry("2026-09-18", 10, 45),
  entry("2026-09-01", 9, 30),
  entry("2026-08-28", 9, 90),
];

describe("buildRuler", () => {
  it("has a row for every calendar day from today to the oldest recorded day", () => {
    const model = ruler(history, TODAY);
    expect(model.rows[0]!.iso).toBe("2026-09-24");
    expect(model.rows.at(-1)!.iso).toBe("2026-08-28");
    // 24 days of September and 4 of August.
    expect(model.rows).toHaveLength(28);
    const dates = model.rows.map((row) => row.iso);
    expect(new Set(dates).size).toBe(dates.length);
    expect(dates).toEqual([...dates].sort().reverse());
  });

  it("marks Mondays, the 1st, today and the weekend", () => {
    const model = ruler(history, TODAY);
    const row = (iso: string) => model.byISO.get(iso)!;
    expect(row("2026-09-21").weekday).toBe(1);
    expect(row("2026-09-01").date).toBe(1);
    expect(row("2026-09-24").today).toBe(true);
    expect(row("2026-09-23").today).toBe(false);
    expect(row("2026-09-20").band).toBe("weekend");
    expect(row("2026-09-19").band).toBe("weekend");
    expect(row("2026-09-18").band).toBeNull();
  });

  it("carries the day header's tracked time and the entry count", () => {
    const model = ruler(history, TODAY);
    const row = model.byISO.get("2026-09-23")!;
    expect(row.trackedMs).toBe((120 + 35) * MINUTE);
    expect(row.count).toBe(2);
    expect(model.byISO.get("2026-09-22")!.trackedMs).toBe(0);
    expect(model.byISO.get("2026-09-22")!.count).toBe(0);
  });

  it("counts finished entries only, like today's card", () => {
    const running: TimeEntry = { ...entry(TODAY, 11, 10), stopped_at: null };
    const model = ruler([...history, running], TODAY);
    expect(model.byISO.get(TODAY)!.trackedMs).toBe(50 * MINUTE);
    expect(model.byISO.get(TODAY)!.count).toBe(1);
  });

  it("gives time off precedence over the weekend, and sick leave over other time off", () => {
    const model = ruler(history, TODAY, [
      timeOff("vacation", "2026-09-04", "2026-09-08"),
      timeOff("dayoff", "2026-09-10", "2026-09-10"),
      timeOff("sick", "2026-09-08", "2026-09-09"),
    ]);
    const band = (iso: string) => model.byISO.get(iso)!.band;
    // Saturday and Sunday inside the vacation are vacation, not weekend.
    expect(band("2026-09-05")).toBe("vacation");
    expect(band("2026-09-06")).toBe("vacation");
    expect(band("2026-09-08")).toBe("sick");
    expect(band("2026-09-09")).toBe("sick");
    expect(band("2026-09-10")).toBe("dayoff");
    expect(band("2026-09-12")).toBe("weekend");
    expect(model.byISO.get("2026-09-08")!.off).toBe("sick");
  });

  it("jumps a day with entries to itself and an empty day to the next older one", () => {
    const model = ruler(history, TODAY);
    const target = (iso: string) => model.byISO.get(iso)!.target;
    expect(target("2026-09-23")).toBe("2026-09-23");
    expect(target("2026-09-22")).toBe("2026-09-21");
    expect(target("2026-09-20")).toBe("2026-09-18");
    expect(target("2026-09-02")).toBe("2026-09-01");
    expect(target("2026-08-31")).toBe("2026-08-28");
  });

  it("jumps today to the top of the page, and so any day newer than the newest recorded one", () => {
    const model = ruler(history.filter((item) => localDay(item) !== "2026-09-23"), TODAY);
    expect(model.byISO.get(TODAY)!.target).toBe("top");
    // The 23rd and 22nd are empty and newer than the 21st, the newest day before today.
    expect(model.byISO.get("2026-09-23")!.target).toBe("top");
    expect(model.byISO.get("2026-09-22")!.target).toBe("top");
    expect(model.byISO.get("2026-09-21")!.target).toBe("2026-09-21");
  });

  it("starts at today even when today has no entries", () => {
    const model = ruler(history.filter((item) => localDay(item) !== TODAY), TODAY);
    expect(model.rows[0]!.iso).toBe(TODAY);
    expect(model.rows[0]!.count).toBe(0);
    expect(model.rows[0]!.target).toBe("top");
  });

  it("starts at a recorded day newer than today when the clock went back", () => {
    const model = ruler([...history, entry("2026-09-26", 9, 30)], TODAY);
    expect(model.rows[0]!.iso).toBe("2026-09-26");
    expect(model.byISO.get(TODAY)!.today).toBe(true);
  });

  it("is empty without a finished entry", () => {
    const model = ruler([], TODAY);
    expect(model.rows).toEqual([]);
    expect(model.months).toEqual([]);
    expect(model.height).toBe(0);
  });

  it("lays out a header per month and a pitch per day, all arithmetic", () => {
    const model = ruler(history, TODAY);
    expect(model.months.map((month) => month.key)).toEqual(["2026-09", "2026-08"]);
    const [september, august] = model.months;
    expect(september!.top).toBe(0);
    expect(september!.height).toBe(RULER_HEAD + 24 * RULER_PITCH);
    expect(model.rows[0]!.top).toBe(RULER_HEAD);
    expect(august!.top).toBe(september!.height);
    expect(model.byISO.get("2026-08-31")!.top).toBe(september!.height + RULER_HEAD);
    expect(model.height).toBe(september!.height + august!.height);
    expect(model.spineEnd).toBe(model.rows.at(-1)!.top + RULER_PITCH / 2);
  });

  it("adds a year row above the months of a past year", () => {
    const model = ruler([entry("2026-01-02", 9, 60), entry("2025-12-30", 9, 60)], "2026-01-02");
    expect(model.years.map((year) => [year.year, year.past])).toEqual([
      [2026, false],
      [2025, true],
    ]);
    const december = model.monthByKey.get("2025-12")!;
    // January: a header and two days; then the year row, then December's header.
    expect(december.top).toBe(RULER_HEAD + 2 * RULER_PITCH + RULER_HEAD);
    expect(model.byISO.get("2025-12-31")!.past).toBe(true);
    expect(model.byISO.get("2025-12-31")!.top).toBe(december.top + RULER_HEAD);
  });

  it("jumps a month header to its newest day with entries", () => {
    const model = ruler(history, TODAY);
    // Today is September's newest recorded day: the top of the page.
    expect(model.monthByKey.get("2026-09")!.target).toBe("top");
    expect(model.monthByKey.get("2026-08")!.target).toBe("2026-08-28");
    expect(model.monthByKey.get("2026-08")!.recorded).toBe(true);
  });

  it("jumps a month without entries to where the month would be in the feed", () => {
    const model = ruler([entry("2026-09-10", 9, 30), entry("2026-07-30", 9, 30)], TODAY);
    const august = model.monthByKey.get("2026-08")!;
    expect(august.recorded).toBe(false);
    expect(august.target).toBe("2026-07-30");
  });
});

describe("buildRuler against the previous model", () => {
  it("keeps the rows that did not change, so only a changed day re-renders", () => {
    const before = ruler(history, TODAY);
    const after = buildRuler(feedDays([...history, entry("2026-09-22", 11, 25)]), new Map(), TODAY, before);
    const changed = after.rows.filter((row) => row !== before.byISO.get(row.iso)).map((row) => row.iso);
    expect(changed).toEqual(["2026-09-22"]);
    expect(after.byISO.get("2026-09-22")!.trackedMs).toBe(25 * MINUTE);
    // The months hold the same objects as the rows.
    expect(after.months.flatMap((month) => month.rows)).toEqual(after.rows);
    for (const [index, row] of after.months.flatMap((month) => month.rows).entries()) expect(row).toBe(after.rows[index]);
    // Nothing changed at all: every row is the previous one.
    const again = buildRuler(feedDays(history), new Map(), TODAY, before);
    expect(again.rows.every((row) => row === before.byISO.get(row.iso))).toBe(true);
  });
});

describe("buildRuler in a zone that skipped a day", () => {
  beforeAll(() => {
    vi.stubEnv("TZ", "Pacific/Apia");
  });
  afterAll(() => {
    vi.unstubAllEnvs();
  });

  it("steps over the day Samoa never had", () => {
    // Guards the test itself: without the zone switch the 30th exists.
    expect(localDay(entry("2011-12-30", 12, 10))).toBe("2011-12-31");
    const model = ruler([entry("2012-01-01", 9, 30), entry("2011-12-29", 9, 30)], "2012-01-01");
    expect(model.rows.map((row) => row.iso)).toEqual(["2012-01-01", "2011-12-31", "2011-12-29"]);
    expect(model.rows.map((row) => row.weekday)).toEqual([0, 6, 4]);
  });
});

describe("rulerOffset", () => {
  const model = ruler(history, TODAY);
  const top = (iso: string) => model.byISO.get(iso)!.top;

  it("maps a fraction of a day's card onto its row", () => {
    expect(rulerOffset(model, { iso: "2026-09-23", fraction: 0, gap: false, next: "2026-09-21" })).toBe(top("2026-09-23"));
    expect(rulerOffset(model, { iso: "2026-09-23", fraction: 0.5, gap: false, next: "2026-09-21" })).toBe(
      top("2026-09-23") + RULER_PITCH / 2,
    );
  });

  it("spans the empty rows between two recorded days across the gap", () => {
    // The feed is newest first: after Monday the 21st's card comes the gap down
    // to Friday the 18th, which on the ruler spans the weekend's rows.
    const start = top("2026-09-21") + RULER_PITCH;
    const end = top("2026-09-18");
    expect(rulerOffset(model, { iso: "2026-09-21", fraction: 0, gap: true, next: "2026-09-18" })).toBe(start);
    expect(rulerOffset(model, { iso: "2026-09-21", fraction: 1, gap: true, next: "2026-09-18" })).toBe(end);
    expect(rulerOffset(model, { iso: "2026-09-21", fraction: 0.5, gap: true, next: "2026-09-18" })).toBe((start + end) / 2);
  });

  it("crosses a month header in a gap", () => {
    const start = top("2026-09-01") + RULER_PITCH;
    expect(rulerOffset(model, { iso: "2026-09-01", fraction: 1, gap: true, next: "2026-08-28" })).toBe(top("2026-08-28"));
    expect(top("2026-08-28") - start).toBe(RULER_HEAD + 3 * RULER_PITCH);
  });

  it("maps the space above the feed from today's row to the feed's first day", () => {
    expect(rulerOffset(model, { iso: null, fraction: 0, gap: false, next: "2026-09-23" })).toBe(top(TODAY));
    expect(rulerOffset(model, { iso: null, fraction: 1, gap: false, next: "2026-09-23" })).toBe(top("2026-09-23"));
  });

  it("ends a gap after the oldest day at the end of its row", () => {
    expect(rulerOffset(model, { iso: "2026-08-28", fraction: 0.7, gap: true, next: null })).toBe(top("2026-08-28") + RULER_PITCH);
  });
});

describe("rowFrom and rowUntil", () => {
  const model = ruler(history, TODAY);

  it("find the rows a thumb overlaps", () => {
    const first = model.rows[0]!.top;
    expect(rowFrom(model, first)).toBe(0);
    expect(rowFrom(model, first + RULER_PITCH - 1)).toBe(0);
    expect(rowFrom(model, first + RULER_PITCH)).toBe(1);
    expect(rowUntil(model, first + RULER_PITCH)).toBe(0);
    expect(rowUntil(model, first + RULER_PITCH + 1)).toBe(1);
    // Across September's end and August's header.
    const august = model.byISO.get("2026-08-31")!;
    expect(rowFrom(model, august.top - 1)).toBe(august.index);
  });
});

describe("stepMonth", () => {
  const model = ruler([entry("2026-03-31", 9, 30), entry("2025-12-15", 9, 30)], "2026-03-31");
  const index = (iso: string) => model.byISO.get(iso)!.index;

  it("moves to the same date a month older, clamped to the month's length", () => {
    expect(model.rows[stepMonth(model, index("2026-03-31"), -1)]!.iso).toBe("2026-02-28");
    expect(model.rows[stepMonth(model, index("2026-02-28"), -1)]!.iso).toBe("2026-01-28");
  });

  it("moves newer, and stops at the ruler's ends", () => {
    expect(model.rows[stepMonth(model, index("2026-01-28"), 1)]!.iso).toBe("2026-02-28");
    expect(stepMonth(model, index("2026-03-10"), 1)).toBe(0);
    expect(stepMonth(model, index("2026-01-10"), -12)).toBe(model.rows.length - 1);
  });
});

function localDay(item: TimeEntry): string {
  const date = new Date(item.started_at);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}
