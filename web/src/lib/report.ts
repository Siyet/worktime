// Pure report computations shared by the Reports page and the printable report.
// All durations are whole minutes; days are local YYYY-MM-DD strings.
import { localDateISO } from "./format";
import { formattingLocale } from "./settings.svelte";
import type { TimeEntry, TimeOff, TimeOffKind } from "./types";

export interface ReportEntry {
  id: string;
  date: string;
  projectID: string | null;
  description: string;
  startedAt: number;
  minutes: number;
}

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
      startedAt: entry.started_at,
      minutes: Math.round((entry.stopped_at - entry.started_at) / 60000),
    });
  }
  return result;
}

// Inclusive list of local days between two YYYY-MM-DD dates. Noon anchors dodge DST edges.
export function listDays(fromISO: string, toISO: string): string[] {
  if (!fromISO || !toISO || toISO < fromISO) return [];
  const days: string[] = [];
  const cursor = new Date(fromISO + "T12:00");
  const end = new Date(toISO + "T12:00");
  while (cursor <= end) {
    days.push(localDateISO(cursor.getTime()));
    cursor.setDate(cursor.getDate() + 1);
    if (days.length > 1000) break;
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

export function formatDayISO(dayISO: string): string {
  return new Date(dayISO + "T12:00").toLocaleDateString(formattingLocale(), {
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

// CSV of the given entries: Date, Project, Description, Start (24h), Duration (min).
// minutesFor lets the caller substitute overlap-adjusted durations for raw ones.
export function buildCSV(
  entries: ReportEntry[],
  projectName: (projectID: string | null) => string,
  roundingStep: number,
  minutesFor: (entry: ReportEntry) => number = (entry) => entry.minutes,
): string {
  const lines = [["Date", "Project", "Description", "Start", "Duration (min)"].join(",")];
  const sorted = [...entries].sort((left, right) => left.startedAt - right.startedAt);
  for (const entry of sorted) {
    const started = new Date(entry.startedAt);
    const start = `${String(started.getHours()).padStart(2, "0")}:${String(started.getMinutes()).padStart(2, "0")}`;
    const description = `"${entry.description.replaceAll('"', '""')}"`;
    const minutes = Math.round(roundMinutes(minutesFor(entry), roundingStep));
    lines.push([entry.date, projectName(entry.projectID), description, start, minutes].join(","));
  }
  return lines.join("\n");
}
