// The day ruler beside the Timer feed on wide screens: one row per calendar day
// from today back to the oldest recorded day, each a place to jump the feed to.
//
// Pure module, like feed.ts: the model and every offset in it are arithmetic -
// rows and headers have fixed heights - so nothing here touches the DOM and it
// stays testable under vitest's node environment. The component that draws it is
// lib/components/DayRuler.svelte; design/components/day-ruler.html is the spec.
import type { FeedDay } from "./feed";
import { entryDurationMs } from "./format";
import type { TimeOffKind } from "./types";

/** One calendar day, in px - the --dr-pitch of the component. */
export const RULER_PITCH = 16;
/** A month header, and a past year's row above its months - --dr-head. */
export const RULER_HEAD = 24;

export type RulerBand = TimeOffKind | "weekend";
/** Where a click lands: a day of the feed, or the top of the page. */
export type RulerTarget = string | "top";

export interface RulerRow {
  iso: string;
  year: number;
  /** 0-11. */
  month: number;
  date: number;
  /** 0 is Sunday, as Date.getDay. */
  weekday: number;
  /** The day header's own figure: every finished entry's duration, summed. */
  trackedMs: number;
  count: number;
  off: TimeOffKind | null;
  /** Time off first, then the weekend - the Reports chart's precedence. */
  band: RulerBand | null;
  today: boolean;
  /** In a year other than the current one: a year row sticks above its month. */
  past: boolean;
  index: number;
  /** Offset from the top of the ruler's track. */
  top: number;
  target: RulerTarget;
}

export interface RulerMonth {
  /** YYYY-MM. */
  key: string;
  year: number;
  month: number;
  past: boolean;
  top: number;
  height: number;
  rows: RulerRow[];
  /** Whether any day of it has entries. */
  recorded: boolean;
  target: RulerTarget;
  index: number;
}

export interface RulerYear {
  year: number;
  past: boolean;
  top: number;
  months: RulerMonth[];
}

export interface RulerModel {
  rows: RulerRow[];
  years: RulerYear[];
  months: RulerMonth[];
  byISO: Map<string, RulerRow>;
  monthByKey: Map<string, RulerMonth>;
  /** The track's height. */
  height: number;
  /** Where the spine ends: the oldest day's tick. */
  spineEnd: number;
}

/**
 * A place in the feed as FeedScroller reports it: inside a day's card, in the gap
 * after it, or above the feed altogether (iso null). `next` is the next older day
 * of the feed - where a gap ends - or, above the feed, its first day.
 */
export interface FeedSpot {
  iso: string | null;
  /** 0-1 across the card, the gap, or the space above the feed. */
  fraction: number;
  gap: boolean;
  next: string | null;
}

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

function isoOf(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** Noon, so a DST change never moves the date. */
export function dateOfISO(iso: string): Date {
  const [year, month, day] = iso.split("-").map(Number);
  return new Date(year!, month! - 1, day!, 12);
}

// The local day before, at noon. A day a zone skipped - Samoa went from 29 to 31
// December 2011 - does not exist there, and stepping back into it lands on the
// same day again.
function dayBefore(date: Date): Date {
  const iso = isoOf(date);
  for (let back = 1; ; back += 1) {
    const previous = new Date(date.getFullYear(), date.getMonth(), date.getDate() - back, 12);
    if (isoOf(previous) < iso) return previous;
  }
}

function sameRow(left: RulerRow, right: RulerRow): boolean {
  for (const key of Object.keys(left) as (keyof RulerRow)[]) {
    if (left[key] !== right[key]) return false;
  }
  return true;
}

// buildRuler lays out every calendar day from today (or a newer recorded day, if
// the clock went back) down to the oldest day with a finished entry. A row that
// came out the same as in the previous model is that model's own object, so a
// sync that changes one day re-renders one row, not years of them.
export function buildRuler(
  days: readonly FeedDay[],
  off: ReadonlyMap<string, TimeOffKind>,
  todayISO: string,
  previous?: RulerModel,
): RulerModel {
  const empty: RulerModel = {
    rows: [],
    years: [],
    months: [],
    byISO: new Map(),
    monthByKey: new Map(),
    height: 0,
    spineEnd: 0,
  };
  const oldest = days.at(-1)?.iso;
  if (oldest === undefined) return empty;
  const recorded = new Map(days.map((day) => [day.iso, day]));
  const currentYear = Number(todayISO.slice(0, 4));
  const newestISO = days[0]!.iso > todayISO ? days[0]!.iso : todayISO;

  const rows: RulerRow[] = [];
  for (let cursor = dateOfISO(newestISO), iso = newestISO; iso >= oldest; cursor = dayBefore(cursor), iso = isoOf(cursor)) {
    const day = recorded.get(iso);
    const weekday = cursor.getDay();
    const kind = off.get(iso) ?? null;
    rows.push({
      iso,
      year: cursor.getFullYear(),
      month: cursor.getMonth(),
      date: cursor.getDate(),
      weekday,
      // Every group's total, summed: the entries' durations.
      trackedMs: day === undefined ? 0 : day.entries.reduce((sum, entry) => sum + entryDurationMs(entry, 0), 0),
      count: day?.entries.length ?? 0,
      off: kind,
      band: kind ?? (weekday === 0 || weekday === 6 ? "weekend" : null),
      today: iso === todayISO,
      past: cursor.getFullYear() !== currentYear,
      index: rows.length,
      top: 0,
      target: "top",
    });
  }

  // Where a click lands: the day itself, else the nearest older day with entries
  // - where the day would sit in a newest-first feed. Today, and anything newer
  // than the newest recorded day before it, is the top of the page: the start
  // form and the running timers. One pass from the oldest row up.
  const newestRecorded = days.find((day) => day.iso !== todayISO)?.iso ?? null;
  let older = oldest;
  for (let index = rows.length - 1; index >= 0; index -= 1) {
    const row = rows[index]!;
    if (recorded.has(row.iso)) older = row.iso;
    row.target = row.today || newestRecorded === null || row.iso > newestRecorded ? "top" : older;
  }

  const years: RulerYear[] = [];
  const months: RulerMonth[] = [];
  let offset = 0;
  let year: RulerYear | null = null;
  let month: RulerMonth | null = null;
  for (let index = 0; index < rows.length; index += 1) {
    let row = rows[index]!;
    if (year === null || year.year !== row.year) {
      year = { year: row.year, past: row.past, top: offset, months: [] };
      years.push(year);
      if (year.past) offset += RULER_HEAD;
      month = null;
    }
    const key = `${row.year}-${pad(row.month + 1)}`;
    if (month === null || month.key !== key) {
      month = {
        key,
        year: row.year,
        month: row.month,
        past: row.past,
        top: offset,
        height: RULER_HEAD,
        rows: [],
        recorded: false,
        target: "top",
        index: months.length,
      };
      year.months.push(month);
      months.push(month);
      offset += RULER_HEAD;
    }
    row.top = offset;
    offset += RULER_PITCH;
    const before = previous?.byISO.get(row.iso);
    if (before !== undefined && sameRow(before, row)) row = rows[index] = before;
    month.rows.push(row);
    month.height += RULER_PITCH;
  }
  // A month header lands on its newest day with entries, or the top of the page
  // when that is today, as today's row does; a month without one, where the
  // month would be in the feed.
  for (const each of months) {
    const newest = each.rows.find((row) => recorded.has(row.iso));
    each.recorded = newest !== undefined;
    each.target = newest === undefined ? each.rows.at(-1)!.target : newest.today ? "top" : newest.iso;
  }
  return {
    rows,
    years,
    months,
    byISO: new Map(rows.map((row) => [row.iso, row])),
    monthByKey: new Map(months.map((each) => [each.key, each])),
    height: offset,
    spineEnd: rows.at(-1)!.top + RULER_PITCH / 2,
  };
}

// rulerOffset maps a place in the feed onto the ruler's track, continuously: a
// fraction of a day's card is that fraction of its row, the gap after it spans
// the empty rows (and any header) down to the next recorded day, and the space
// above the feed spans today's row down to the feed's first day. So the thumb
// glides with the page and crosses a weekend as the feed crosses the gap
// between Friday and Monday.
export function rulerOffset(model: RulerModel, spot: FeedSpot): number {
  const first = model.rows[0]?.top ?? 0;
  const topOf = (iso: string | null, fallback: number) => (iso === null ? fallback : (model.byISO.get(iso)?.top ?? fallback));
  if (spot.iso === null) return first + (topOf(spot.next, first) - first) * spot.fraction;
  const row = model.byISO.get(spot.iso);
  if (row === undefined) return first;
  if (!spot.gap) return row.top + RULER_PITCH * spot.fraction;
  const end = row.top + RULER_PITCH;
  return end + (topOf(spot.next, end) - end) * spot.fraction;
}

/** The first row whose box reaches below the offset. */
export function rowFrom(model: RulerModel, offset: number): number {
  const rows = model.rows;
  let low = 0;
  let high = rows.length - 1;
  while (low < high) {
    const middle = (low + high) >> 1;
    if (rows[middle]!.top + RULER_PITCH > offset) high = middle;
    else low = middle + 1;
  }
  return low;
}

/** The last row whose box starts above the offset. */
export function rowUntil(model: RulerModel, offset: number): number {
  const rows = model.rows;
  let low = 0;
  let high = rows.length - 1;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (rows[middle]!.top < offset) low = middle;
    else high = middle - 1;
  }
  return low;
}

// stepMonth finds the same date a number of months newer (positive) or older
// (negative), clamped to the month's length and to the ruler's ends.
export function stepMonth(model: RulerModel, index: number, delta: number): number {
  const row = model.rows[index];
  if (row === undefined) return index;
  const date = new Date(row.year, row.month + delta, 1, 12);
  const length = new Date(date.getFullYear(), date.getMonth() + 1, 0, 12).getDate();
  date.setDate(Math.min(row.date, length));
  const found = model.byISO.get(isoOf(date));
  if (found !== undefined) return found.index;
  return delta > 0 ? 0 : model.rows.length - 1;
}
