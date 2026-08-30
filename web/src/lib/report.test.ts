import { describe, expect, it } from "vitest";
import {
  apportion,
  buildCSV,
  groupKeysOf,
  listDays,
  NO_PROJECT_KEY,
  roundMinutes,
  splitOverlapMinutes,
  summariseReport,
  toReportEntries,
  UNTAGGED_KEY,
  visibleReportProjects,
  type GroupBy,
  type ReportEntry,
} from "./report";
import type { Project, TimeEntry, TimeOffKind } from "./types";

function makeEntry(overrides: Partial<ReportEntry> = {}): ReportEntry {
  return {
    id: "id-" + Math.abs(overrides.startedAt ?? 0) + (overrides.description ?? ""),
    date: "2026-07-01",
    projectID: null,
    description: "work",
    tags: [],
    startedAt: Date.parse("2026-07-01T09:00"),
    minutes: 60,
    ...overrides,
  };
}

function makeProject(id: string, archived = false): Project {
  return {
    id,
    name: id,
    color: "#2563eb",
    archived,
    created_at: 1,
    updated_at: 1,
    deleted_at: null,
  };
}

// Deterministic PRNG so the generated-set identity test never flakes.
function mulberry32(seed: number): () => number {
  let state = seed;
  return () => {
    state |= 0;
    state = (state + 0x6d2b79f5) | 0;
    let mixed = Math.imul(state ^ (state >>> 15), 1 | state);
    mixed = (mixed + Math.imul(mixed ^ (mixed >>> 7), 61 | mixed)) ^ mixed;
    return ((mixed ^ (mixed >>> 14)) >>> 0) / 4294967296;
  };
}

describe("toReportEntries", () => {
  it("skips running entries and resolves missing tags to an empty array", () => {
    const base = {
      project_id: null,
      description: "x",
      created_at: 1,
      updated_at: 1,
      deleted_at: null,
    };
    const rows: TimeEntry[] = [
      { ...base, id: "a", started_at: 1_000_000, stopped_at: 1_060_000 },
      { ...base, id: "running", started_at: 1_000_000, stopped_at: null },
      { ...base, id: "b", started_at: 1_000_000, stopped_at: 1_120_000, tags: ["review"] },
    ];
    const report = toReportEntries(rows);
    expect(report.map((entry) => entry.id)).toEqual(["a", "b"]);
    expect(report[0]!.tags).toEqual([]);
    expect(report[1]!.tags).toEqual(["review"]);
  });
});

describe("visibleReportProjects", () => {
  it("always includes active projects and only includes archived projects present in the range", () => {
    const active = makeProject("active");
    const archivedInRange = makeProject("archived-in-range", true);
    const archivedOutsideRange = makeProject("archived-outside-range", true);

    expect(
      visibleReportProjects(
        [active, archivedInRange, archivedOutsideRange],
        [makeEntry({ projectID: archivedInRange.id })],
      ),
    ).toEqual([active, archivedInRange]);
  });
});

describe("groupKeysOf", () => {
  it("returns exactly one key for project, day and description", () => {
    const entry = makeEntry({ projectID: "p1", description: "desc" });
    expect(groupKeysOf(entry, "project")).toEqual(["p1"]);
    expect(groupKeysOf(makeEntry({ projectID: null }), "project")).toEqual([NO_PROJECT_KEY]);
    expect(groupKeysOf(entry, "day")).toEqual(["2026-07-01"]);
    expect(groupKeysOf(entry, "description")).toEqual(["desc"]);
  });

  it("returns every tag for tag grouping and the untagged bucket for none", () => {
    expect(groupKeysOf(makeEntry({ tags: ["a", "b", "c"] }), "tag")).toEqual(["a", "b", "c"]);
    expect(groupKeysOf(makeEntry({ tags: [] }), "tag")).toEqual([UNTAGGED_KEY]);
  });

  it("groups descriptions by their normalised spelling, like the timer list", () => {
    expect(groupKeysOf(makeEntry({ description: "API  Work " }), "description")).toEqual(
      groupKeysOf(makeEntry({ description: "api work" }), "description"),
    );
  });
});

describe("apportion", () => {
  it("makes displayed values sum exactly to the rounded total", () => {
    const values = [100.4, 100.4, 100.4];
    const result = apportion(values, 301.2);
    expect(result.reduce((sum, value) => sum + value, 0)).toBe(301);
  });

  it("makes three equal groups sum to exactly 100 percent", () => {
    const result = apportion([50, 50, 50], 100);
    expect(result.reduce((sum, value) => sum + value, 0)).toBe(100);
    // Largest-remainder: 33+33+34, never 33+33+33.
    expect(result.filter((value) => value === 34)).toHaveLength(1);
  });

  it("hands the missing units to the largest fractions", () => {
    // Raw shares: 45.45, 45.45, 9.09 -> floors 45,45,9 (99); the stable sort
    // gives the leftover unit to the first of the tied .45 fractions.
    expect(apportion([50, 50, 10], 100)).toEqual([46, 45, 9]);
  });

  it("handles empty and zero-total inputs", () => {
    expect(apportion([], 100)).toEqual([]);
    expect(apportion([0, 0], 100)).toEqual([0, 0]);
  });

  it("sums exactly on randomised inputs", () => {
    const random = mulberry32(42);
    for (let round = 0; round < 200; round++) {
      const values = Array.from({ length: 1 + Math.floor(random() * 12) }, () => random() * 500);
      const total = values.reduce((sum, value) => sum + value, 0);
      const result = apportion(values, total);
      expect(result.reduce((sum, value) => sum + value, 0)).toBe(Math.round(total));
      const pcts = apportion(values, 100);
      expect(pcts.reduce((sum, value) => sum + value, 0)).toBe(100);
    }
  });
});

describe("sum of groups is independent of the grouping", () => {
  const GROUPINGS: GroupBy[] = ["project", "tag", "day", "description"];

  function groupTotal(entries: ReportEntry[], groupBy: GroupBy, billable: (entry: ReportEntry) => number): number {
    const groups = new Map<string, number>();
    for (const entry of entries) {
      const keys = groupKeysOf(entry, groupBy);
      for (const key of keys) {
        groups.set(key, (groups.get(key) ?? 0) + billable(entry) / keys.length);
      }
    }
    return [...groups.values()].reduce((sum, value) => sum + value, 0);
  }

  function generateEntries(seed: number): ReportEntry[] {
    const random = mulberry32(seed);
    const tagPool = ["analysis", "development", "meeting", "review", "other"];
    return Array.from({ length: 80 }, (unused, index) => {
      const tagCount = [0, 0, 0, 1, 1, 1, 1, 2, 3, 3][Math.floor(random() * 10)]!;
      const tags = [...tagPool].sort(() => random() - 0.5).slice(0, tagCount).sort();
      const day = 1 + Math.floor(random() * 5);
      const startedAt = Date.parse(`2026-07-0${day}T06:00`) + Math.floor(random() * 8) * 1_800_000;
      return makeEntry({
        id: `gen-${index}`,
        date: `2026-07-0${day}`,
        projectID: random() < 0.3 ? null : `p${Math.floor(random() * 3)}`,
        description: `task ${Math.floor(random() * 6)}`,
        tags,
        startedAt,
        minutes: 5 + Math.floor(random() * 180),
      });
    });
  }

  it("holds for raw minutes, including entries with 0, 1 and 3 tags", () => {
    const entries = generateEntries(1);
    const expected = entries.reduce((sum, entry) => sum + entry.minutes, 0);
    for (const groupBy of GROUPINGS) {
      expect(groupTotal(entries, groupBy, (entry) => entry.minutes)).toBeCloseTo(expected, 6);
    }
  });

  it("holds for rounded minutes", () => {
    const entries = generateEntries(2);
    const billable = (entry: ReportEntry) => roundMinutes(entry.minutes, 15);
    const expected = entries.reduce((sum, entry) => sum + billable(entry), 0);
    for (const groupBy of GROUPINGS) {
      expect(groupTotal(entries, groupBy, billable)).toBeCloseTo(expected, 6);
    }
  });

  it("holds for overlap-adjusted shares", () => {
    const entries = generateEntries(3);
    const shares = splitOverlapMinutes(entries);
    const billable = (entry: ReportEntry) => shares.get(entry.id) ?? entry.minutes;
    const expected = entries.reduce((sum, entry) => sum + billable(entry), 0);
    for (const groupBy of GROUPINGS) {
      expect(groupTotal(entries, groupBy, billable)).toBeCloseTo(expected, 6);
    }
  });
});

describe("buildCSV", () => {
  it("emits one row per entry with a Tags column and never a share", () => {
    const entries = [
      makeEntry({ id: "1", description: "two tags", tags: ["a", "b"], minutes: 60 }),
      makeEntry({ id: "2", description: "no tags", tags: [], minutes: 30, startedAt: Date.parse("2026-07-01T12:00") }),
    ];
    const csv = buildCSV(entries, () => "Proj", 0);
    const lines = csv.split("\n");
    expect(lines[0]).toBe("Date,Project,Description,Tags,Start,Duration (min)");
    expect(lines).toHaveLength(3);
    // The two-tag entry carries its full 60 minutes: a CSV row is an entry, not a group share.
    expect(lines[1]).toContain('"a;b"');
    expect(lines[1]).toContain(",60");
    expect(lines[2]).toContain('"(untagged)"');
    expect(lines[2]).toContain(",30");
  });
});

describe("toReportEntries with agent pauses", () => {
  const base: TimeEntry = {
    id: "paused",
    project_id: null,
    description: "agent work",
    tags: [],
    started_at: Date.parse("2026-07-01T09:00"),
    stopped_at: Date.parse("2026-07-01T12:00"),
    created_at: 0,
    updated_at: 0,
    deleted_at: null,
  };

  it("reports billable minutes, not the whole interval", () => {
    const [entry] = toReportEntries([{ ...base, paused_ms: 60 * 60_000 }]);
    expect(entry!.minutes).toBe(120);
  });

  it("leaves an entry without the field exactly as before", () => {
    const [entry] = toReportEntries([base]);
    expect(entry!.minutes).toBe(180);
  });

  it("models a paused entry as a shorter interval for overlaps-once", () => {
    // The compressed interval is what keeps ordinary entries' numbers identical:
    // a partner entry starting after the billable end no longer overlaps.
    const paused = toReportEntries([{ ...base, paused_ms: 2 * 60 * 60_000 }]);
    const partner = toReportEntries([
      { ...base, id: "partner", started_at: Date.parse("2026-07-01T10:30"), stopped_at: Date.parse("2026-07-01T11:30") },
    ]);
    const shares = splitOverlapMinutes([...paused, ...partner]);
    expect(shares.get("paused")).toBe(60);
    expect(shares.get("partner")).toBe(60);
  });
});

describe("buildCSV", () => {
  const projectName = (projectID: string | null) => (projectID === "p1" ? "Acme, Ltd" : "No project");

  it("quotes a project name containing a comma", () => {
    // Written raw it produces a seventh field and shifts every later column right,
    // which no CSV reader complains about - the file just parses into garbage.
    const csv = buildCSV([makeEntry({ projectID: "p1" })], projectName, 0);
    const row = csv.split("\n")[1]!;
    expect(row).toContain('"Acme, Ltd"');
    expect(splitCSVRow(row)).toHaveLength(6);
  });

  it("keeps every row at six fields whatever the text holds", () => {
    const csv = buildCSV(
      [makeEntry({ projectID: "p1", description: 'a "quoted", comma', tags: ["x,y"] })],
      projectName,
      0,
    );
    for (const row of csv.split("\n")) expect(splitCSVRow(row)).toHaveLength(6);
  });
});

// Minimal RFC 4180 field splitter, enough to count fields in a generated row.
function splitCSVRow(row: string): string[] {
  const fields: string[] = [];
  let current = "";
  let quoted = false;
  for (let index = 0; index < row.length; index++) {
    const character = row[index]!;
    if (quoted) {
      if (character === '"' && row[index + 1] === '"') {
        current += '"';
        index++;
      } else if (character === '"') {
        quoted = false;
      } else {
        current += character;
      }
    } else if (character === '"') {
      quoted = true;
    } else if (character === ",") {
      fields.push(current);
      current = "";
    } else {
      current += character;
    }
  }
  fields.push(current);
  return fields;
}

describe("listDays", () => {
  it("covers the whole range, so averages divide by the days they were summed over", () => {
    // Truncating the list while the entries keep the full range gave a numerator
    // spanning years over a denominator spanning months - 34 hours a day, unflagged.
    const days = listDays("2015-01-01", "2026-08-10");
    expect(days).toHaveLength(4240);
    expect(days.at(-1)).toBe("2026-08-10");
  });
});

describe("summariseReport", () => {
  const noOff = new Map<string, TimeOffKind>();
  const raw = (entry: ReportEntry) => entry.minutes;

  it("suppresses the tag section when nothing shown is tagged", () => {
    const summary = summariseReport([makeEntry()], ["2026-07-01"], noOff, raw);
    expect(summary.byTag).toEqual([]);
  });

  it("gates the tag section on the entries it is given, not on a wider range", () => {
    // The screen and the print sheet gated this differently, so the same filter
    // produced a By tag card on one and no section on the other.
    const tagged = makeEntry({ id: "tagged", tags: ["dev"] });
    expect(summariseReport([tagged], ["2026-07-01"], noOff, raw).byTag).toHaveLength(1);
    expect(summariseReport([makeEntry({ id: "plain" })], ["2026-07-01"], noOff, raw).byTag).toEqual([]);
  });

  it("splits a multi-tag entry so the tag totals match the project totals", () => {
    const summary = summariseReport([makeEntry({ tags: ["a", "b"] })], ["2026-07-01"], noOff, raw);
    const tagTotal = summary.byTag.reduce((sum, [, minutes]) => sum + minutes, 0);
    expect(tagTotal).toBeCloseTo(summary.totalMinutes);
  });

  it("sorts the untagged bucket last however large it is", () => {
    const summary = summariseReport(
      [makeEntry({ id: "big" }), makeEntry({ id: "big2" }), makeEntry({ id: "small", tags: ["dev"], minutes: 5 })],
      ["2026-07-01"],
      noOff,
      raw,
    );
    expect(summary.byTag.at(-1)![0]).toBe(UNTAGGED_KEY);
  });
});
