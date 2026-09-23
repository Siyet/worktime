// The Timer page's feed of finished entries, and the rules for which part of it
// is in the DOM at any moment.
//
// Pure module on purpose, like tasks.ts: nothing here touches lib/state or the
// DOM, so bucketing and windowing stay testable under vitest's node environment.
// The DOM side - measuring, scrolling, anchoring - lives in feed-scroll.svelte.ts.
import { localDateISO } from "./format";
import type { TimeEntry } from "./types";

export interface FeedDay {
  /** Local calendar day, YYYY-MM-DD; the day's identity in the window. */
  iso: string;
  /** Newest first. */
  entries: TimeEntry[];
}

// feedDays buckets every finished entry into its local day, newest day first.
//
// The instance holds more than ten thousand entries and this reruns on every
// start, stop and sync merge, so it makes one pass instead of sorting them all:
// only the day keys and each day's own handful of entries get sorted. Entries
// arrive roughly in creation order, so most of them fall on the same day as the
// one before and skip the Date allocation behind localDateISO.
export function feedDays(entries: readonly TimeEntry[]): FeedDay[] {
  const buckets = new Map<string, TimeEntry[]>();
  let dayStart = 0;
  let dayEnd = -1;
  let bucket: TimeEntry[] = [];
  for (const entry of entries) {
    if (entry.stopped_at === null) continue;
    const startedAt = entry.started_at;
    if (startedAt < dayStart || startedAt >= dayEnd) {
      const dayISO = localDateISO(startedAt);
      const midnight = new Date(dayISO + "T00:00");
      dayStart = midnight.getTime();
      // Through the Date constructor, not +24h: a DST day is 23 or 25 hours long.
      dayEnd = new Date(midnight.getFullYear(), midnight.getMonth(), midnight.getDate() + 1).getTime();
      bucket = buckets.get(dayISO) ?? [];
      buckets.set(dayISO, bucket);
    }
    bucket.push(entry);
  }
  return [...buckets.entries()]
    .sort(([left], [right]) => (left < right ? 1 : left > right ? -1 : 0))
    .map(([iso, dayEntries]) => ({ iso, entries: dayEntries.sort((left, right) => right.started_at - left.started_at) }));
}

/** Rendered days as indices into the days array: [first, last] of [0, loaded). */
export interface FeedWindow {
  first: number;
  last: number;
  loaded: number;
}

export interface FeedPlanInput {
  window: FeedWindow;
  /** Total number of days in the feed. */
  total: number;
  /** Height of every loaded day, index-aligned; null where not measured yet. */
  heights: readonly (number | null)[];
  /** The viewport, in pixels relative to the top of the feed. */
  viewTop: number;
  viewBottom: number;
}

/** Days added to the loaded part of the feed per step. */
export const FEED_CHUNK_DAYS = 7;
/** Height assumed for a day never measured, until one has been. */
export const FEED_DEFAULT_DAY_HEIGHT = 160;
/** Mount everything within this many viewports of the visible range... */
export const FEED_NEAR_VIEWPORTS = 1.5;
/** ...and unmount only what is further than this. The gap is the hysteresis. */
export const FEED_FAR_VIEWPORTS = 3;

// estimateDayHeight stands in for a day that has not been measured: the mean of
// the ones that have been.
export function estimateDayHeight(heights: readonly (number | null)[]): number {
  let sum = 0;
  let count = 0;
  for (const height of heights) {
    if (height === null) continue;
    sum += height;
    count += 1;
  }
  return count === 0 ? FEED_DEFAULT_DAY_HEIGHT : sum / count;
}

// dayOffsets is the prefix sum of day heights: offsets[i] is where day i starts,
// offsets[heights.length] is the bottom of the loaded feed.
export function dayOffsets(heights: readonly (number | null)[]): number[] {
  const estimate = estimateDayHeight(heights);
  const offsets = [0];
  for (const height of heights) offsets.push(offsets[offsets.length - 1]! + (height ?? estimate));
  return offsets;
}

// dayAt is the index of the loaded day holding feed coordinate y, clamped into
// the loaded range.
function dayAt(offsets: readonly number[], y: number): number {
  const loaded = offsets.length - 1;
  let low = 0;
  let high = loaded - 1;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (offsets[middle]! <= y) low = middle;
    else high = middle - 1;
  }
  return Math.max(0, low);
}

// planFeedWindow decides which days are mounted and how far the feed is loaded.
//
// Everything within FEED_NEAR_VIEWPORTS of the viewport is mounted; a mounted day
// is only unmounted once it is further than FEED_FAR_VIEWPORTS, so scrolling back
// and forth across a boundary does not mount and unmount the same day every frame.
// A jump - a scrollbar drag, Home, End - lands wherever the offsets say, because
// every day above the frontier has a height, measured or estimated.
//
// The frontier only moves once the last loaded day has been measured: until then
// the bottom of the feed is a guess, and advancing on a guess would load the whole
// history in one burst of frames.
export function planFeedWindow(input: FeedPlanInput): FeedWindow {
  const { window, total, heights, viewTop, viewBottom } = input;
  const loaded = heights.length;
  if (loaded === 0) {
    const initial = Math.min(total, FEED_CHUNK_DAYS);
    return { first: 0, last: initial - 1, loaded: initial };
  }
  const viewport = Math.max(1, viewBottom - viewTop);
  const near = FEED_NEAR_VIEWPORTS * viewport;
  const far = FEED_FAR_VIEWPORTS * viewport;
  const offsets = dayOffsets(heights);
  const nearFirst = dayAt(offsets, viewTop - near);
  const nearLast = dayAt(offsets, viewBottom + near);
  const farFirst = dayAt(offsets, viewTop - far);
  const farLast = dayAt(offsets, viewBottom + far);
  let first = nearFirst;
  let last = nearLast;
  // A jump that leaves the mounted range entirely - a scrollbar drag, Home, End -
  // starts over from the near range; there is nothing mounted worth keeping.
  if (window.first <= window.last && window.last >= farFirst && window.first <= farLast) {
    const clamp = (value: number, low: number, high: number) => Math.min(Math.max(value, low), high);
    first = clamp(window.first, farFirst, nearFirst);
    last = clamp(window.last, nearLast, farLast);
  }
  let nextLoaded = loaded;
  const frontierMeasured = heights[loaded - 1] !== null;
  if (loaded < total && frontierMeasured && offsets[loaded]! - viewBottom < near) {
    nextLoaded = Math.min(total, loaded + FEED_CHUNK_DAYS);
    // The new days are mounted straight away: they cannot be measured otherwise.
    last = nextLoaded - 1;
  }
  return { first, last: Math.max(first, last), loaded: nextLoaded };
}

/** The window by day keys, so it survives days appearing and disappearing around it. */
export interface FeedWindowKeys {
  /** Newest mounted day; null while the window starts at the newest day, so a new day joins it. */
  first: string | null;
  /** Oldest mounted day. */
  last: string | null;
  /** Oldest loaded day; null until the first plan, which loads the first chunk. */
  frontier: string | null;
}

// countNewer is how many days are newer than key - or on it, when inclusive.
// Days are sorted newest first, so those days are a prefix and a binary search
// finds where it ends.
function countNewer(days: readonly FeedDay[], key: string, inclusive: boolean): number {
  let low = 0;
  let high = days.length;
  while (low < high) {
    const middle = (low + high) >> 1;
    const iso = days[middle]!.iso;
    if (iso > key || (inclusive && iso === key)) low = middle + 1;
    else high = middle;
  }
  return low;
}

// resolveWindow turns the keys back into indices against the current days. A key
// whose day has been deleted resolves to its neighbour on the inside, and a day
// that appears within the window simply becomes part of it. Both edge days deleted
// can leave first > last: nothing is mounted until the next plan, which the data
// change has already scheduled.
export function resolveWindow(days: readonly FeedDay[], keys: FeedWindowKeys): FeedWindow {
  const loaded =
    keys.frontier === null ? Math.min(days.length, FEED_CHUNK_DAYS) : countNewer(days, keys.frontier, true);
  if (loaded === 0) return { first: 0, last: -1, loaded: 0 };
  const first = keys.first === null ? 0 : countNewer(days, keys.first, false);
  const last = (keys.last === null ? loaded : Math.min(loaded, countNewer(days, keys.last, true))) - 1;
  return { first, last, loaded };
}

export function windowKeys(days: readonly FeedDay[], window: FeedWindow): FeedWindowKeys {
  return {
    first: window.first === 0 ? null : (days[window.first]?.iso ?? null),
    last: days[window.last]?.iso ?? null,
    frontier: days[window.loaded - 1]?.iso ?? null,
  };
}
