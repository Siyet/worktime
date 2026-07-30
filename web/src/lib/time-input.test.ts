import { describe, expect, it } from "vitest";
import {
  calendarDayDiff,
  composeTimestamp,
  formatTimeOfDay,
  nudgeTimeText,
  parseTimeText,
  shiftDateISO,
  timeOfDayFromMs,
} from "./time-input";

describe("parseTimeText", () => {
  it("accepts the documented forms", () => {
    expect(parseTimeText("9")).toEqual({ hour: 9, minute: 0 });
    expect(parseTimeText("930")).toEqual({ hour: 9, minute: 30 });
    expect(parseTimeText("0930")).toEqual({ hour: 9, minute: 30 });
    expect(parseTimeText("9:30")).toEqual({ hour: 9, minute: 30 });
    expect(parseTimeText("9.30")).toEqual({ hour: 9, minute: 30 });
    expect(parseTimeText(" 23:59 ")).toEqual({ hour: 23, minute: 59 });
    expect(parseTimeText("0")).toEqual({ hour: 0, minute: 0 });
  });

  it("rejects out-of-range and malformed input", () => {
    expect(parseTimeText("24:00")).toBeNull();
    expect(parseTimeText("12:60")).toBeNull();
    expect(parseTimeText("2460")).toBeNull();
    expect(parseTimeText("9:3")).toBeNull();
    expect(parseTimeText("abc")).toBeNull();
    expect(parseTimeText("")).toBeNull();
    expect(parseTimeText("12345")).toBeNull();
    expect(parseTimeText("-100")).toBeNull();
  });
});

describe("formatTimeOfDay", () => {
  it("pads to HH:MM", () => {
    expect(formatTimeOfDay({ hour: 9, minute: 5 })).toBe("09:05");
    expect(formatTimeOfDay({ hour: 0, minute: 0 })).toBe("00:00");
  });
});

describe("nudgeTimeText", () => {
  it("nudges by minutes and wraps within the day", () => {
    expect(nudgeTimeText("09:30", 1)).toBe("09:31");
    expect(nudgeTimeText("09:30", -15)).toBe("09:15");
    expect(nudgeTimeText("23:59", 1)).toBe("00:00");
    expect(nudgeTimeText("00:00", -1)).toBe("23:59");
    expect(nudgeTimeText("930", 1)).toBe("09:31");
  });

  it("returns null for unparseable text", () => {
    expect(nudgeTimeText("nope", 1)).toBeNull();
  });
});

describe("timestamps", () => {
  it("round-trips through compose and timeOfDayFromMs", () => {
    const ms = composeTimestamp("2026-07-15", { hour: 23, minute: 40 });
    expect(timeOfDayFromMs(ms)).toEqual({ hour: 23, minute: 40 });
  });

  it("applies the end-day offset across month boundaries", () => {
    const sameDay = composeTimestamp("2026-07-31", { hour: 23, minute: 40 });
    const nextDay = composeTimestamp("2026-07-31", { hour: 0, minute: 20 }, 1);
    expect(nextDay - sameDay).toBe(40 * 60_000);
  });
});

describe("dates", () => {
  it("shifts across month and year boundaries", () => {
    expect(shiftDateISO("2026-07-31", 1)).toBe("2026-08-01");
    expect(shiftDateISO("2026-01-01", -1)).toBe("2025-12-31");
  });

  it("counts calendar days", () => {
    expect(calendarDayDiff("2026-07-31", "2026-08-01")).toBe(1);
    expect(calendarDayDiff("2026-07-31", "2026-07-31")).toBe(0);
    expect(calendarDayDiff("2026-08-01", "2026-07-31")).toBe(-1);
  });
});
