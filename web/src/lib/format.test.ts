import { describe, expect, it } from "vitest";
import { descriptionKey, displayEntryDescription, entryDurationMs, sessionTag } from "./format";
import type { TimeEntry } from "./types";

function makeEntry(overrides: Partial<TimeEntry> = {}): TimeEntry {
  return {
    id: "entry",
    project_id: null,
    description: "work",
    tags: [],
    started_at: 1_000_000,
    stopped_at: 1_060_000,
    created_at: 1_000_000,
    updated_at: 1_000_000,
    deleted_at: null,
    ...overrides,
  };
}

describe("descriptionKey", () => {
  it("collapses the spellings the grouping must treat as one task", () => {
    expect(descriptionKey("  API   work ")).toBe("api work");
    expect(descriptionKey("API work")).toBe(descriptionKey("api  WORK"));
  });

  it("keeps different tasks apart", () => {
    expect(descriptionKey("API work")).not.toBe(descriptionKey("API works"));
  });

  it("maps an empty description to an empty key", () => {
    expect(descriptionKey("   ")).toBe("");
  });
});

describe("displayEntryDescription", () => {
  it("keeps technical session identifiers out of entry lists", () => {
    const sessionID = "01a03f80-1234-5678-9abc-def012345678";
    expect(displayEntryDescription({ description: "Codex #01a03f80", agent_session_id: sessionID })).toBe("Codex");
    expect(displayEntryDescription({ description: "WT-1 Real task", agent_session_id: sessionID })).toBe("WT-1 Real task");
  });

  it("does not strip text from ordinary entries", () => {
    expect(displayEntryDescription({ description: "Codex #01a03f80", agent_session_id: null })).toBe("Codex #01a03f80");
  });
});

describe("entryDurationMs", () => {
  it("measures a finished entry by its own boundaries", () => {
    expect(entryDurationMs(makeEntry(), 9_999_999)).toBe(60_000);
  });

  it("measures a running entry against now", () => {
    expect(entryDurationMs(makeEntry({ stopped_at: null }), 1_030_000)).toBe(30_000);
  });

  it("never goes negative on a hand-edited boundary", () => {
    expect(entryDurationMs(makeEntry({ stopped_at: 999_000 }), 0)).toBe(0);
  });
});

describe("sessionTag", () => {
  it("matches the eight hex characters the server puts into entry names", () => {
    expect(sessionTag("AB12CD34-1111-2222-3333-444444444444")).toBe("ab12cd34");
  });
});
