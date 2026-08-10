// Pure report computations shared by the Reports page and the printable report.
// All durations are whole minutes; days are local YYYY-MM-DD strings.
import { descriptionKey, entryDurationMs, localDateISO } from "./format";
import { formattingLocale } from "./settings.svelte";
import type { TimeEntry, TimeOff, TimeOffKind } from "./types";

export interface ReportEntry {
  id: string;
  date: string;
  projectID: string | null;
  description: string;
  // Always a concrete array: the optional field on TimeEntry is resolved here
  // once, so nothing downstream ever handles undefined.
  tags: string[];
  startedAt: number;
  minutes: number;
}

export const NO_PROJECT_KEY = "none";

// The bucket for entries with no tags. User tags are normalised to lowercase
// and the server rejects a leading underscore, so no collision is possible.
export const UNTAGGED_KEY = "__untagged";

export type GroupBy = "project" | "tag" | "day" | "description";

// Finished entries only; running timers never enter reports.
export function toReportEntries(entries: TimeEntry[]): ReportEntry[] {
  const result: ReportEntry[] = [];
  for (const entry of entries) {
    if (entry.stopped_at === null) continue;
    result.push({
      id: entry.id,
      date: localDateISO(entry.started_at),
      projectID: entry.project_id,
      description: entry.description,
      tags: entry.tags ?? [],
      startedAt: entry.started_at,
      // Billable minutes, so a paused agent entry is not reported as the whole
      // interval. ReportEntry deliberately carries no paused_ms of its own:
      // splitOverlapMinutes then models such an entry as [start, start+minutes],
      // which leaves every ordinary entry's numbers untouched.
      minutes: Math.round(entryDurationMs(entry, entry.stopped_at) / 60000),
    });
  }
  return result;
}

// The group keys an entry belongs to. Exactly one key for project/day/
// description; for tag grouping an entry lands in every one of its tags, or in
// the untagged bucket. Every entry has at least one key under every grouping,
// and an entry's weight inside one of its groups is billable / keys.length, so
// Σ groups = Σ entries regardless of the grouping.
// Descriptions group by their normalised form, the same key the Timer page
// groups by; the label has to be taken from an entry, not from the key, or
// "API work" would be displayed as "api work".
export function groupKeysOf(entry: ReportEntry, groupBy: GroupBy): string[] {
  if (groupBy === "project") return [entry.projectID ?? NO_PROJECT_KEY];
  if (groupBy === "day") return [entry.date];
  if (groupBy === "description") return [descriptionKey(entry.description)];
  return entry.tags.length > 0 ? entry.tags : [UNTAGGED_KEY];
}

// Largest-remainder allocation: integer parts of `values` scaled to sum to
// exactly Math.round(targetTotal). Display rounds each group independently, so
// without this the printed Итого row and a 17%+17%+17% percentage column would
// visibly fail the arithmetic they invite.
export function apportion(values: number[], targetTotal: number): number[] {
  const total = values.reduce((sum, value) => sum + value, 0);
  const target = Math.round(targetTotal);
  if (total <= 0 || values.length === 0) return values.map(() => 0);
  const raw = values.map((value) => (value / total) * target);
  const result = raw.map(Math.floor);
  let remainder = target - result.reduce((sum, value) => sum + value, 0);
  const byFraction = raw
    .map((value, index) => ({ fraction: value - Math.floor(value), index }))
    .sort((left, right) => right.fraction - left.fraction);
  // Hand out the missing units to the largest fractions; float drift can in
  // principle leave a negative remainder, taken back from the smallest ones.
  for (let position = 0; remainder > 0 && position < byFraction.length; position++, remainder--) {
    const index = byFraction[position]!.index;
    result[index] = result[index]! + 1;
  }
  for (let position = byFraction.length - 1; remainder < 0 && position >= 0; position--, remainder++) {
    const index = byFraction[position]!.index;
    result[index] = result[index]! - 1;
  }
  return result;
}

// A range this long is a date-input accident, not a report anyone reads day by day.
// The cap exists so a stray year like 0202 cannot hang the page building a million
// strings; it is far past any range the UI offers.
export const maxReportDays = 20_000;

// A daily bar chart stops being readable long before it stops being drawable, and a
// date input types through intermediate years ("0002", "0020", "0202") on the way to a
// real one - each of which asks for tens of thousands of SVG nodes. The aggregates
// still cover the whole range; only the drawing is dropped. Both report surfaces share
// the threshold so the screen and the printed sheet make the same call.
export const maxChartDays = 400;

// Inclusive list of local days between two YYYY-MM-DD dates. Noon anchors dodge DST edges.
//
// The list has to cover the whole range: callers divide totals by its length and scan
// it for the peak day, while the entries themselves are filtered by the raw range. A
// list stopping short would leave a report whose numerator spans years and whose
// denominator spans months - an average of thirty-four hours a day, with nothing on
// screen to say why.
export function listDays(fromISO: string, toISO: string): string[] {
  if (!fromISO || !toISO || toISO < fromISO) return [];
  const days: string[] = [];
  const cursor = new Date(fromISO + "T12:00");
  const end = new Date(toISO + "T12:00");
  while (cursor <= end) {
    days.push(localDateISO(cursor.getTime()));
    cursor.setDate(cursor.getDate() + 1);
    if (days.length >= maxReportDays) break;
  }
  return days;
}

// Per-day time-off kind. Sick leave wins over overlapping vacation/dayoff ranges
// (it changes how the day is drawn and counted).
export function expandTimeOff(timeOff: TimeOff[]): Map<string, TimeOffKind> {
  const byDay = new Map<string, TimeOffKind>();
  for (const range of timeOff) {
    for (const day of listDays(range.date_from, range.date_to)) {
      if (byDay.get(day) === "sick") continue;
      byDay.set(day, range.kind);
    }
  }
  return byDay;
}

export function isWeekend(dayISO: string): boolean {
  const weekday = new Date(dayISO + "T12:00").getDay();
  return weekday === 0 || weekday === 6;
}

// Rounds a single entry to the nearest step with a one-step minimum (0 = off).
export function roundMinutes(minutes: number, step: number): number {
  if (!step) return minutes;
  return Math.max(step, Math.round(minutes / step) * step);
}

// Splits wall-clock time equally between simultaneously running entries, so
// overlapping work is counted once in totals: an hour spent on two concurrent
// tasks contributes one hour overall (30 minutes to each). Returns fractional
// minutes per entry id; entries that never overlap keep their full duration.
export function splitOverlapMinutes(entries: ReportEntry[]): Map<string, number> {
  interface Edge {
    time: number;
    delta: 1 | -1;
    id: string;
  }
  const edges: Edge[] = [];
  const shares = new Map<string, number>();
  for (const entry of entries) {
    shares.set(entry.id, 0);
    const end = entry.startedAt + entry.minutes * 60_000;
    if (end <= entry.startedAt) continue;
    edges.push({ time: entry.startedAt, delta: 1, id: entry.id });
    edges.push({ time: end, delta: -1, id: entry.id });
  }
  edges.sort((left, right) => left.time - right.time);

  const active = new Set<string>();
  let previousTime = 0;
  for (const edge of edges) {
    if (active.size > 0 && edge.time > previousTime) {
      const sliceMs = (edge.time - previousTime) / active.size;
      for (const id of active) shares.set(id, shares.get(id)! + sliceMs);
    }
    if (edge.delta === 1) {
      active.add(edge.id);
    } else {
      active.delete(edge.id);
    }
    previousTime = edge.time;
  }

  const minutes = new Map<string, number>();
  for (const [id, ms] of shares) minutes.set(id, ms / 60_000);
  return minutes;
}

// dateRangeEntries narrows to a local date range. The timestamp pre-filter is what
// keeps the conversion off the whole history: only the survivors get a Date built for
// them, and the generous one-day margin covers the offset between a local day and the
// UTC timestamp behind it without having to reason about the sign.
export function dateRangeEntries(entries: TimeEntry[], fromISO: string, toISO: string): ReportEntry[] {
  if (!fromISO || !toISO || toISO < fromISO) return [];
  const day = 86_400_000;
  const fromMs = Date.parse(fromISO + "T00:00:00Z") - day;
  const toMs = Date.parse(toISO + "T00:00:00Z") + 2 * day;
  const candidates = entries.filter(
    (entry) => entry.stopped_at !== null && entry.started_at >= fromMs && entry.started_at <= toMs,
  );
  return toReportEntries(candidates).filter((entry) => entry.date >= fromISO && entry.date <= toISO);
}

export interface ReportSummary {
  totalMinutes: number;
  workDays: string[];
  avgMinutes: number;
  minutesByDay: Map<string, number>;
  peakDay: string | null;
  weekendMinutes: number;
  offCounts: Record<TimeOffKind, number>;
  byProject: [string, number][];
  byTag: [string, number][];
}

// summariseReport is the whole aggregation both report surfaces render. It lived twice
// - once in the Reports page and once in the print sheet - and the copies had already
// drifted: the two gated the By tag section on different entry sets, so one filter
// produced a tag section on screen and none on the printed sheet.
//
// entries are already filtered; days and off describe the range, and minutesOf supplies
// either raw or overlap-adjusted minutes.
export function summariseReport(
  entries: ReportEntry[],
  days: string[],
  off: Map<string, TimeOffKind>,
  minutesOf: (entry: ReportEntry) => number,
): ReportSummary {
  const totalMinutes = entries.reduce((sum, entry) => sum + minutesOf(entry), 0);
  const workDays = days.filter((day) => !isWeekend(day) && !off.has(day));

  const minutesByDay = new Map<string, number>();
  for (const entry of entries) {
    minutesByDay.set(entry.date, (minutesByDay.get(entry.date) ?? 0) + minutesOf(entry));
  }

  let peakDay: string | null = null;
  for (const day of days) {
    if ((minutesByDay.get(day) ?? 0) > (peakDay ? minutesByDay.get(peakDay)! : 0)) peakDay = day;
  }

  const weekendMinutes = entries
    .filter((entry) => isWeekend(entry.date))
    .reduce((sum, entry) => sum + minutesOf(entry), 0);

  const offCounts: Record<TimeOffKind, number> = { vacation: 0, sick: 0, dayoff: 0 };
  for (const day of days) {
    const kind = off.get(day);
    if (kind) offCounts[kind]++;
  }

  const projectTotals = new Map<string, number>();
  for (const entry of entries) {
    const key = entry.projectID ?? NO_PROJECT_KEY;
    projectTotals.set(key, (projectTotals.get(key) ?? 0) + minutesOf(entry));
  }

  return {
    totalMinutes,
    workDays,
    avgMinutes: workDays.length ? totalMinutes / workDays.length : 0,
    minutesByDay,
    peakDay,
    weekendMinutes,
    offCounts,
    byProject: [...projectTotals.entries()].sort((left, right) => right[1] - left[1]),
    byTag: totalsByTag(entries, minutesOf),
  };
}

// Split shares: an entry with k tags contributes 1/k of its minutes to each, so
// Σ By tag = Σ By project = the total, and the untagged bucket keeps every untagged
// entry inside it. The section is suppressed when nothing shown is tagged - a history
// with no tags must not grow a card holding a single "untagged, 100%" row. Untagged
// sorts last regardless of size: it is a residue, not a tag.
function totalsByTag(entries: ReportEntry[], minutesOf: (entry: ReportEntry) => number): [string, number][] {
  if (!entries.some((entry) => entry.tags.length > 0)) return [];
  const totals = new Map<string, number>();
  for (const entry of entries) {
    const keys = entry.tags.length > 0 ? entry.tags : [UNTAGGED_KEY];
    for (const key of keys) {
      totals.set(key, (totals.get(key) ?? 0) + minutesOf(entry) / keys.length);
    }
  }
  const rows = [...totals.entries()].sort((left, right) => right[1] - left[1]);
  const untaggedIndex = rows.findIndex(([key]) => key === UNTAGGED_KEY);
  if (untaggedIndex >= 0) rows.push(...rows.splice(untaggedIndex, 1));
  return rows;
}

export function formatDayISO(dayISO: string): string {
  return new Date(dayISO + "T12:00").toLocaleDateString(formattingLocale(), {
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

// CSV of the given entries: Date, Project, Description, Tags, Start (24h),
// Duration (min). minutesFor lets the caller substitute overlap-adjusted
// durations for raw ones; it is billable-per-entry - a CSV row is an entry,
// not a group, so tag shares never appear here and with rounding off
// Σ Duration equals the report total exactly.
export function buildCSV(
  entries: ReportEntry[],
  projectName: (projectID: string | null) => string,
  roundingStep: number,
  minutesFor: (entry: ReportEntry) => number = (entry) => entry.minutes,
): string {
  const lines = [["Date", "Project", "Description", "Tags", "Start", "Duration (min)"].join(",")];
  const sorted = [...entries].sort((left, right) => left.startedAt - right.startedAt);
  for (const entry of sorted) {
    const started = new Date(entry.startedAt);
    const start = `${String(started.getHours()).padStart(2, "0")}:${String(started.getMinutes()).padStart(2, "0")}`;
    const minutes = Math.round(roundMinutes(minutesFor(entry), roundingStep));
    lines.push(
      [
        entry.date,
        csvCell(projectName(entry.projectID)),
        csvCell(entry.description),
        csvCell(entry.tags.length > 0 ? entry.tags.join(";") : "(untagged)"),
        start,
        minutes,
      ].join(","),
    );
  }
  return lines.join("\n");
}

// Every free-text cell goes through this. A project named "Acme, Ltd" written raw
// produces a seventh field and shifts Description, Tags, Start and Duration one column
// right for every row of that project - silently, since nothing in a CSV reader
// objects.
function csvCell(value: string): string {
  return `"${value.replaceAll('"', '""')}"`;
}
