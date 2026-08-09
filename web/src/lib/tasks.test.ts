import { describe, expect, it } from "vitest";
import { groupDayEntries, taskKey, taskSuggestions, wallClockMs } from "./tasks";
import type { TimeEntry } from "./types";

const HOUR = 3_600_000;
const NOW = Date.parse("2026-07-01T18:00");

let counter = 0;

function entry(overrides: Partial<TimeEntry> = {}): TimeEntry {
  const startedAt = overrides.started_at ?? NOW - 4 * HOUR;
  counter += 1;
  return {
    id: `entry-${counter}`,
    project_id: null,
    description: "Write e2e tests",
    tags: [],
    started_at: startedAt,
    stopped_at: startedAt + HOUR,
    created_at: startedAt,
    updated_at: startedAt,
    deleted_at: null,
    ...overrides,
  };
}

describe("groupDayEntries", () => {
  it("collapses repeats of the same task and sums their time", () => {
    const groups = groupDayEntries(
      [
        entry({ started_at: NOW - 6 * HOUR }),
        entry({ started_at: NOW - 4 * HOUR, description: "write  E2E   tests" }),
        entry({ started_at: NOW - 2 * HOUR }),
      ],
      NOW,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]!.entries).toHaveLength(3);
    expect(groups[0]!.totalMs).toBe(3 * HOUR);
    // The display form comes from the newest member, not from the key.
    expect(groups[0]!.description).toBe("Write e2e tests");
    expect(groups[0]!.firstStartedAt).toBe(NOW - 6 * HOUR);
    expect(groups[0]!.lastStoppedAt).toBe(NOW - HOUR);
  });

  it("keeps different projects and different tags apart", () => {
    const groups = groupDayEntries(
      [
        entry({ project_id: "p1" }),
        entry({ project_id: "p2" }),
        entry({ project_id: "p1", tags: ["review"] }),
      ],
      NOW,
    );
    expect(groups).toHaveLength(3);
  });

  it("ignores tag order", () => {
    const groups = groupDayEntries([entry({ tags: ["a", "b"] }), entry({ tags: ["b", "a"] })], NOW);
    expect(groups).toHaveLength(1);
  });

  it("never groups entries without a description", () => {
    const groups = groupDayEntries([entry({ description: "" }), entry({ description: "  " })], NOW);
    expect(groups).toHaveLength(2);
  });

  it("orders finished groups by their last end and running ones first", () => {
    const groups = groupDayEntries(
      [
        entry({ description: "older", started_at: NOW - 8 * HOUR }),
        entry({ description: "newer", started_at: NOW - 3 * HOUR }),
        entry({ description: "running", started_at: NOW - 10 * HOUR, stopped_at: null }),
      ],
      NOW,
    );
    expect(groups.map((group) => group.description)).toEqual(["running", "newer", "older"]);
  });

  it("sums to the same total as the individual entries", () => {
    const entries = [
      entry({ started_at: NOW - 6 * HOUR }),
      entry({ started_at: NOW - 5 * HOUR, description: "other" }),
      entry({ started_at: NOW - 2 * HOUR }),
      entry({ started_at: NOW - HOUR, stopped_at: null }),
    ];
    const dayTotal = entries.reduce((sum, item) => sum + ((item.stopped_at ?? NOW) - item.started_at), 0);
    const groups = groupDayEntries(entries, NOW);
    expect(groups.reduce((sum, group) => sum + group.totalMs, 0)).toBe(dayTotal);
    // Wall clock is never larger than the sum, and never smaller than the
    // widest single group.
    const wall = wallClockMs(entries, NOW);
    expect(wall).toBeLessThanOrEqual(dayTotal);
    expect(wall).toBeGreaterThanOrEqual(Math.max(...groups.map((group) => group.wallMs)));
  });
});

describe("wallClockMs", () => {
  it("counts an hour worked twice in parallel once", () => {
    const entries = [entry({ started_at: NOW - 3 * HOUR }), entry({ started_at: NOW - 3 * HOUR })];
    expect(entries.reduce((sum, item) => sum + (item.stopped_at! - item.started_at), 0)).toBe(2 * HOUR);
    expect(wallClockMs(entries, NOW)).toBe(HOUR);
  });

  it("adds up sequential entries", () => {
    expect(wallClockMs([entry({ started_at: NOW - 4 * HOUR }), entry({ started_at: NOW - 2 * HOUR })], NOW)).toBe(
      2 * HOUR,
    );
  });

  it("does not double count a nested interval", () => {
    const outer = entry({ started_at: NOW - 4 * HOUR, stopped_at: NOW - HOUR });
    const inner = entry({ started_at: NOW - 3 * HOUR, stopped_at: NOW - 2 * HOUR });
    expect(wallClockMs([outer, inner], NOW)).toBe(3 * HOUR);
  });

  it("measures a running entry up to now", () => {
    expect(wallClockMs([entry({ started_at: NOW - 2 * HOUR, stopped_at: null })], NOW)).toBe(2 * HOUR);
  });
});

describe("taskKey", () => {
  it("is stable across spellings and tag order", () => {
    expect(taskKey(" API  work ", "p1", ["b", "a"])).toBe(taskKey("api work", "p1", ["a", "b"]));
  });
});

describe("taskSuggestions", () => {
  const source = [
    entry({ description: "Write e2e tests", started_at: NOW - 2 * HOUR, project_id: "p1", tags: ["review"] }),
    entry({ description: "Write docs", started_at: NOW - 3 * HOUR }),
    entry({ description: "Rewrite the sync engine", started_at: NOW - 4 * HOUR }),
    entry({ description: "Ancient work", started_at: NOW - 30 * 24 * HOUR }),
  ];

  it("suggests nothing for an empty query", () => {
    expect(taskSuggestions(source, "", NOW)).toEqual([]);
    expect(taskSuggestions(source, "   ", NOW)).toEqual([]);
  });

  it("ranks prefix matches above substring matches", () => {
    const found = taskSuggestions(source, "write", NOW).map((suggestion) => suggestion.description);
    expect(found.slice(0, 2)).toEqual(["Write e2e tests", "Write docs"]);
    expect(found).toContain("Rewrite the sync engine");
  });

  it("carries the project and tags of the newest use", () => {
    const [first] = taskSuggestions(source, "write e2e", NOW);
    expect(first).toEqual({ description: "Write e2e tests", projectID: "p1", tags: ["review"] });
  });

  it("deduplicates spellings and keeps the newest one", () => {
    const entries = [
      entry({ description: "api work", started_at: NOW - 5 * HOUR }),
      entry({ description: "API Work", started_at: NOW - HOUR }),
    ];
    const found = taskSuggestions(entries, "api", NOW);
    expect(found).toHaveLength(1);
    expect(found[0]!.description).toBe("API Work");
  });

  it("does not suggest what is already typed in full", () => {
    expect(taskSuggestions(source, "Write docs", NOW)).toEqual([]);
  });

  it("ignores entries older than the window unless they are still running", () => {
    expect(taskSuggestions(source, "ancient", NOW)).toEqual([]);
    const stillRunning = entry({ description: "Ancient run", started_at: NOW - 30 * 24 * HOUR, stopped_at: null });
    expect(taskSuggestions([stillRunning], "ancient", NOW)).toHaveLength(1);
  });

  it("returns at most eight suggestions", () => {
    const many = Array.from({ length: 12 }, (_, index) =>
      entry({ description: `Task number ${index}`, started_at: NOW - index * 60_000 }),
    );
    expect(taskSuggestions(many, "task", NOW)).toHaveLength(8);
  });
});
