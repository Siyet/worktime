// Task grouping and description suggestions for the Timer page.
//
// Pure module on purpose: it imports nothing from lib/state, which reaches for
// window and setInterval at module level, so these functions stay testable under
// vitest's node environment. Tags are read inline as `entry.tags ?? []` for the
// same reason report.ts does it - the field is absent on older rows.
import { descriptionKey, entryDurationMs, localDateISO } from "./format";
import type { TimeEntry } from "./types";

export interface TaskGroup {
  /** Internal identity; never rendered into a DOM id (it contains separators). */
  key: string;
  /** Display form, taken from the newest member. */
  description: string;
  projectID: string | null;
  tags: string[];
  /** Members, newest first. */
  entries: TimeEntry[];
  /** Sum of member durations - what the day total is built from. */
  totalMs: number;
  /** Union of member intervals: work done in parallel is counted once. */
  wallMs: number;
  firstStartedAt: number;
  /** Null while any member is still running. */
  lastStoppedAt: number | null;
}

export interface TaskSuggestion {
  description: string;
  projectID: string | null;
  tags: string[];
}

/** How far back suggestions look: exactly what the Timer page itself shows. */
export const SUGGESTION_WINDOW_DAYS = 7;
const MAX_SUGGESTIONS = 8;

// suggestionWindowStart is the single definition of that window's lower edge: midnight
// of the day SUGGESTION_WINDOW_DAYS-1 back. Every surface derives it from here so the
// feed and the suggestions that claim to mirror it cannot drift apart.
//
// It cuts at a day boundary rather than a rolling 168 hours because the feed groups its
// results into calendar day cards - a rolling cut leaves the oldest card holding only
// what happened after the current time of day. It also changes once a day rather than
// once a second, which keeps the ticker out of every scan over the entry table.
// Days are stepped through the Date constructor rather than by subtracting fixed
// milliseconds: a DST day is 23 or 25 hours long, so a fixed subtraction lands the
// window edge at 01:00 or at 23:00 the previous day for a week after each change -
// which silently drops an hour of entries out of the feed, or grows an eighth card.
export function suggestionWindowStart(nowMs: number): number {
  const today = new Date(localDateISO(nowMs) + "T00:00");
  return new Date(today.getFullYear(), today.getMonth(), today.getDate() - (SUGGESTION_WINDOW_DAYS - 1)).getTime();
}

// A NUL byte cannot occur in a description, a UUID or a tag, so the parts of
// the key can never run into each other.
const KEY_SEPARATOR = "\u0000";

// Grouping by description alone would be wrong twice over: the row shows one
// project dot, and tags split a duration across report groups.
export function taskKey(description: string, projectID: string | null, tags: string[]): string {
  return [descriptionKey(description), projectID ?? "", [...tags].sort().join(",")].join(KEY_SEPARATOR);
}

// groupDayEntries collapses repeats of the same task into one group. Entries
// without a description are never grouped: an empty description says nothing
// about what the work was, so two of them are not the same task.
//
// `now` is only read for entries that are still running, to measure them up to it. A
// caller passing only finished entries can leave it out; passing anything else there -
// a window bound, say - would silently give every running row a duration of zero.
export function groupDayEntries(entries: TimeEntry[], now = 0): TaskGroup[] {
  const groups = new Map<string, TaskGroup>();
  for (const entry of entries) {
    const tags = entry.tags ?? [];
    const key =
      entry.description.trim() === ""
        ? `${KEY_SEPARATOR}entry${KEY_SEPARATOR}${entry.id}`
        : taskKey(entry.description, entry.project_id, tags);
    const existing = groups.get(key);
    if (existing === undefined) {
      groups.set(key, {
        key,
        description: entry.description,
        projectID: entry.project_id,
        tags,
        entries: [entry],
        totalMs: entryDurationMs(entry, now),
        wallMs: 0,
        firstStartedAt: entry.started_at,
        lastStoppedAt: entry.stopped_at,
      });
      continue;
    }
    existing.entries.push(entry);
    existing.totalMs += entryDurationMs(entry, now);
    existing.firstStartedAt = Math.min(existing.firstStartedAt, entry.started_at);
    if (existing.lastStoppedAt !== null) {
      existing.lastStoppedAt = entry.stopped_at === null ? null : Math.max(existing.lastStoppedAt, entry.stopped_at);
    }
  }

  const result = [...groups.values()];
  for (const group of result) {
    group.entries.sort((left, right) => right.started_at - left.started_at);
    group.description = group.entries[0]!.description;
    group.wallMs = wallClockMs(group.entries, now);
  }
  // Running groups first by start, finished ones by when they last ended: the
  // list reads as "what happened most recently", which is why it is not simply
  // sorted by start like the flat list was.
  result.sort((left, right) => {
    if (left.lastStoppedAt === null && right.lastStoppedAt === null) {
      return right.firstStartedAt - left.firstStartedAt;
    }
    if (left.lastStoppedAt === null) return -1;
    if (right.lastStoppedAt === null) return 1;
    return right.lastStoppedAt - left.lastStoppedAt;
  });
  return result;
}

// Union length of the entries' billable intervals. A day where two agents worked
// the same hour is 1h of wall clock and 2h of tracked time; the UI shows both.
// The interval used is [started_at, started_at + duration], the same compressed
// shape splitOverlapMinutes uses in reports, so the two never disagree.
export function wallClockMs(entries: TimeEntry[], now: number): number {
  const intervals = entries
    .map((entry) => ({ start: entry.started_at, end: entry.started_at + entryDurationMs(entry, now) }))
    .filter((interval) => interval.end > interval.start)
    .sort((left, right) => left.start - right.start);
  let total = 0;
  let openStart = 0;
  let openEnd = -1;
  for (const interval of intervals) {
    if (openEnd < interval.start) {
      if (openEnd > openStart) total += openEnd - openStart;
      openStart = interval.start;
      openEnd = interval.end;
    } else if (interval.end > openEnd) {
      openEnd = interval.end;
    }
  }
  if (openEnd > openStart) total += openEnd - openStart;
  return total;
}

// taskSuggestions offers what the page already shows: everything since cutoff plus
// anything still running. The instance has more than ten thousand entries, and a
// suggestion from a year ago helps nobody.
//
// The caller passes the cutoff rather than "now" so that the suggestions and the feed
// they claim to mirror cannot drift apart, and so that neither has to be recomputed
// every second just to keep a window edge fresh.
export function taskSuggestions(entries: TimeEntry[], query: string, cutoff: number): TaskSuggestion[] {
  const needle = descriptionKey(query);
  if (needle === "") return [];

  interface Candidate {
    suggestion: TaskSuggestion;
    lastUsedAt: number;
    count: number;
  }
  const byKey = new Map<string, Candidate>();
  for (const entry of entries) {
    if (entry.description.trim() === "") continue;
    if (entry.stopped_at !== null && entry.started_at < cutoff) continue;
    const key = descriptionKey(entry.description);
    const suggestion: TaskSuggestion = {
      description: entry.description,
      projectID: entry.project_id,
      tags: entry.tags ?? [],
    };
    const seen = byKey.get(key);
    if (seen === undefined) {
      byKey.set(key, { suggestion, lastUsedAt: entry.started_at, count: 1 });
      continue;
    }
    seen.count += 1;
    // The newest spelling wins, along with its project and tags: reusing a task
    // should reproduce the way it was tracked last, not the first time ever.
    if (entry.started_at > seen.lastUsedAt) {
      seen.lastUsedAt = entry.started_at;
      seen.suggestion = suggestion;
    }
  }

  return [...byKey.entries()]
    .filter(([key]) => key !== needle && key.includes(needle))
    .sort((left, right) => {
      const rank = Number(!left[0].startsWith(needle)) - Number(!right[0].startsWith(needle));
      if (rank !== 0) return rank;
      return right[1].lastUsedAt - left[1].lastUsedAt || right[1].count - left[1].count;
    })
    .slice(0, MAX_SUGGESTIONS)
    .map(([, candidate]) => candidate.suggestion);
}

