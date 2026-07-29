import { formattingLocale, hourCycle, prefs } from "./settings.svelte";

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
