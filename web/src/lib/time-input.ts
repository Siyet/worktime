// Time-of-day text parsing for the entry editor. Pure functions, no DOM: the
// editor's fields are free-text mono inputs (input[type=time] follows the
// browser locale, refuses "930" and cannot express an end date), so parsing
// and normalisation live here where they can be unit-tested.

export interface TimeOfDay {
  hour: number;
  minute: number;
}

// Accepts "9", "930", "0930", "9:30", "9.30" - always 24h. Anything else is null.
export function parseTimeText(text: string): TimeOfDay | null {
  const trimmed = text.trim();
  let hour: number;
  let minute: number;
  const separated = /^(\d{1,2})[:.](\d{2})$/.exec(trimmed);
  if (separated) {
    hour = Number(separated[1]);
    minute = Number(separated[2]);
  } else if (/^\d{1,2}$/.test(trimmed)) {
    hour = Number(trimmed);
    minute = 0;
  } else if (/^\d{3,4}$/.test(trimmed)) {
    // "930" -> 9:30, "0930" -> 09:30: minutes are always the last two digits.
    hour = Number(trimmed.slice(0, -2));
    minute = Number(trimmed.slice(-2));
  } else {
    return null;
  }
  if (hour > 23 || minute > 59) return null;
  return { hour, minute };
}

export function formatTimeOfDay(time: TimeOfDay): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${pad(time.hour)}:${pad(time.minute)}`;
}

// Nudge a parseable value by deltaMinutes, wrapping within the day. Returns
// null when the text does not parse - the caller leaves the field untouched.
export function nudgeTimeText(text: string, deltaMinutes: number): string | null {
  const time = parseTimeText(text);
  if (time === null) return null;
  const total = (((time.hour * 60 + time.minute + deltaMinutes) % 1440) + 1440) % 1440;
  return formatTimeOfDay({ hour: Math.floor(total / 60), minute: total % 60 });
}

// Composes a local timestamp from a YYYY-MM-DD date, a time of day and a day
// offset. new Date(year, month, day+offset, ...) is DST-correct: a value that
// falls in a spring-forward gap rolls forward, and the caller writes the rolled
// value back into the field so the stored time is the one on screen.
export function composeTimestamp(dateISO: string, time: TimeOfDay, dayOffset = 0): number {
  const [year, month, day] = dateISO.split("-").map(Number);
  return new Date(year!, month! - 1, day! + dayOffset, time.hour, time.minute).getTime();
}

export function timeOfDayFromMs(ms: number): TimeOfDay {
  const date = new Date(ms);
  return { hour: date.getHours(), minute: date.getMinutes() };
}

// Shifts a YYYY-MM-DD date by whole days, staying in local time.
export function shiftDateISO(dateISO: string, deltaDays: number): string {
  const [year, month, day] = dateISO.split("-").map(Number);
  const shifted = new Date(year!, month! - 1, day! + deltaDays);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${shifted.getFullYear()}-${pad(shifted.getMonth() + 1)}-${pad(shifted.getDate())}`;
}

// Whole calendar days between two YYYY-MM-DD dates (b - a). Noon anchors dodge
// DST: every local day contains a noon, so the division is always exact enough
// to round.
export function calendarDayDiff(fromISO: string, toISO: string): number {
  const from = Date.parse(fromISO + "T12:00");
  const to = Date.parse(toISO + "T12:00");
  return Math.round((to - from) / 86_400_000);
}
