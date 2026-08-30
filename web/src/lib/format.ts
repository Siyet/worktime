import { formattingLocale, hourCycle, prefs } from "./settings.svelte";
import type { TimeEntry } from "./types";

// The one place that knows what an entry's duration is. Running entries are
// measured against `now`, which is what makes offline timers work at all.
// paused_ms is idle time inside the interval that must not be billed; entries
// written before it existed simply have none. It is clamped to the span, so a
// hand-edited boundary can never produce a negative duration.
export function entryDurationMs(entry: TimeEntry, now: number): number {
  const span = Math.max(0, (entry.stopped_at ?? now) - entry.started_at);
  return Math.max(0, span - Math.min(entry.paused_ms ?? 0, span));
}

// Two spellings of the same task must not read as two tasks. Same argument as
// normaliseTag in state/app.svelte.ts: descriptions are values, not ids.
export function descriptionKey(description: string): string {
  return description.trim().replace(/\s+/g, " ").toLowerCase();
}

// The short form of an agent session id, matching what the server puts into an
// entry name before the task is known (AgentSessionTag in internal/store).
export function sessionTag(sessionID: string): string {
  return sessionID.replaceAll("-", "").slice(0, 8).toLowerCase();
}

// The technical suffix identifies the producing agent session, not the user's
// work. Lists show the source label; the full identifier remains in the editor.
export function displayEntryDescription(entry: Pick<TimeEntry, "description" | "agent_session_id">): string {
  if (entry.agent_session_id) {
    const suffix = ` #${sessionTag(entry.agent_session_id)}`;
    if (entry.description.toLowerCase().endsWith(suffix)) {
      return entry.description.slice(0, -suffix.length).trim();
    }
  }
  return entry.description;
}

export function formatDuration(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${hours}:${pad(minutes)}:${pad(seconds)}`;
}

export function formatDurationShort(ms: number): string {
  const totalMinutes = Math.round(ms / 60000);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours === 0) return `${minutes}m`;
  return `${hours}h ${String(minutes).padStart(2, "0")}m`;
}

export function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString(formattingLocale(), {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: hourCycle(),
  });
}

export function formatDay(ms: number): string {
  return new Date(ms).toLocaleDateString(formattingLocale(), { weekday: "short", month: "short", day: "numeric" });
}

// Full numeric date, honoring the date-format preference.
export function formatDate(ms: number): string {
  const date = new Date(ms);
  return formatDateParts(date.getFullYear(), date.getMonth() + 1, date.getDate(), ms);
}

// Same, for YYYY-MM-DD strings (time off ranges) without a timezone round-trip.
export function formatDateISO(dayISO: string): string {
  const [year, month, day] = dayISO.split("-").map(Number);
  return formatDateParts(year!, month!, day!, new Date(dayISO + "T12:00").getTime());
}

function formatDateParts(year: number, month: number, day: number, ms: number): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  switch (prefs.dateFormat) {
    case "dmy":
      return `${pad(day)}.${pad(month)}.${year}`;
    case "mdy":
      return `${pad(month)}/${pad(day)}/${year}`;
    case "ymd":
      return `${year}-${pad(month)}-${pad(day)}`;
    default:
      return new Date(ms).toLocaleDateString(formattingLocale());
  }
}

// localDateISO renders a timestamp as YYYY-MM-DD in the user's local timezone.
export function localDateISO(ms: number): string {
  const date = new Date(ms);
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${date.getFullYear()}-${month}-${day}`;
}
