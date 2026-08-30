import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { compactLegacyPausedEntry, getAllRows, listDirtyMarkers, saveLocalRows, wipeLocalData } from "./db";
import type { TimeEntry } from "./types";

const hour = 3_600_000;

function legacyEntry(overrides: Partial<TimeEntry & { paused_ms: number }> = {}) {
  return {
    id: "00000000-0000-7000-8000-000000000001",
    project_id: null,
    description: "legacy",
    tags: ["review"],
    started_at: 9 * hour,
    stopped_at: 12 * hour,
    created_at: 10,
    updated_at: 20,
    deleted_at: null,
    server_seq: 30,
    agent_session_id: "00000000-0000-7000-8000-000000000002",
    paused_ms: hour,
    ...overrides,
  };
}

describe("compactLegacyPausedEntry", () => {
  it("compacts finished and running bounds without touching sync metadata", () => {
    const finished = compactLegacyPausedEntry(legacyEntry());
    expect(finished).toMatchObject({
      id: "00000000-0000-7000-8000-000000000001",
      description: "legacy",
      tags: ["review"],
      started_at: 9 * hour,
      stopped_at: 11 * hour,
      created_at: 10,
      updated_at: 20,
      deleted_at: null,
      server_seq: 30,
      agent_session_id: "00000000-0000-7000-8000-000000000002",
    });
    expect(Object.hasOwn(finished, "paused_ms")).toBe(false);

    const running = compactLegacyPausedEntry(legacyEntry({ stopped_at: null }));
    expect(running.started_at).toBe(10 * hour);
    expect(running.stopped_at).toBeNull();
    expect(running.updated_at).toBe(20);
    expect(running.server_seq).toBe(30);
  });

  it("clamps finished rows, preserves tombstones, and cannot compact twice", () => {
    const compacted = compactLegacyPausedEntry(
      legacyEntry({ stopped_at: 10 * hour, paused_ms: 5 * hour, deleted_at: 99 }),
    );
    expect(compacted.started_at).toBe(9 * hour);
    expect(compacted.stopped_at).toBe(9 * hour);
    expect(compacted.deleted_at).toBe(99);
    expect(compactLegacyPausedEntry(compacted)).toEqual(compacted);
  });
});

describe("saveLocalRows", () => {
  beforeEach(async () => {
    await wipeLocalData();
  });

  afterEach(async () => {
    await wipeLocalData();
  });

  it("commits every row and dirty marker in one transaction", async () => {
    const first = legacyEntry({ id: "00000000-0000-7000-8000-000000000011", paused_ms: 0 });
    const second = legacyEntry({ id: "00000000-0000-7000-8000-000000000012", paused_ms: 0 });

    const receipt = await saveLocalRows("time_entries", [first, second]);

    expect(receipt.markers).toEqual([
      { table: "time_entries", id: first.id, updated_at: first.updated_at },
      { table: "time_entries", id: second.id, updated_at: second.updated_at },
    ]);
    expect(await getAllRows("time_entries")).toHaveLength(2);
    expect(await listDirtyMarkers()).toHaveLength(2);
  });

  it("aborts earlier puts when a later row cannot be cloned", async () => {
    const valid = legacyEntry({ id: "00000000-0000-7000-8000-000000000021", paused_ms: 0 });
    const invalid = {
      ...legacyEntry({ id: "00000000-0000-7000-8000-000000000022", paused_ms: 0 }),
      cannotClone: () => undefined,
    } as unknown as TimeEntry;

    await expect(saveLocalRows("time_entries", [valid, invalid])).rejects.toBeDefined();

    expect(await getAllRows("time_entries")).toEqual([]);
    expect(await listDirtyMarkers()).toEqual([]);
  });
});
