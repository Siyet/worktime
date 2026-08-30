import { describe, expect, it, vi } from "vitest";
import type { DirtyMarker, LocalMutationReceipt } from "./db";
import { classifyPushedRow, MutationProgressTracker } from "./mutation-progress";
import type { TimeEntry } from "./types";

function row(id: string, updatedAt: number, overrides: Partial<TimeEntry> = {}): TimeEntry {
  return {
    id,
    project_id: null,
    description: "group",
    tags: ["review"],
    started_at: 10,
    stopped_at: 20,
    created_at: 10,
    updated_at: updatedAt,
    deleted_at: null,
    ...overrides,
  };
}

function receipt(...rows: TimeEntry[]): LocalMutationReceipt {
  return {
    rows,
    markers: rows.map((entry) => ({ table: "time_entries", id: entry.id, updated_at: entry.updated_at })),
  };
}

describe("MutationProgressTracker", () => {
  it("keeps a settlement that arrives before the UI observes the receipt", () => {
    const tracker = new MutationProgressTracker();
    const operation = tracker.track(receipt(row("a", 1)));
    tracker.settle(operation.markers[0]!, "accepted");

    const onchange = vi.fn();
    const cleanup = tracker.observe(operation, onchange);

    expect(onchange.mock.lastCall?.[0]).toMatchObject({ total: 1, accepted: 1, pending: 0, rejected: 0, conflict: 0 });
    cleanup();
    expect(tracker.activeObservers()).toBe(0);
  });

  it("settles v1 without attributing that response to a newer local v2", () => {
    const tracker = new MutationProgressTracker();
    const v1 = tracker.track(receipt(row("same", 1)));
    const v2 = tracker.track(receipt(row("same", 2)));
    tracker.settle(v1.markers[0]!, "accepted");

    const v1Changes = vi.fn();
    const v2Changes = vi.fn();
    const cleanupV1 = tracker.observe(v1, v1Changes);
    const cleanupV2 = tracker.observe(v2, v2Changes);

    expect(v1Changes.mock.lastCall?.[0]).toMatchObject({ total: 1, accepted: 1, pending: 0, rejected: 0, conflict: 0 });
    expect(v2Changes.mock.lastCall?.[0]).toMatchObject({ total: 1, accepted: 0, pending: 1, rejected: 0, conflict: 0 });
    cleanupV1();
    cleanupV2();
  });

  it("reports mixed accepted, conflict and isolated-400 outcomes by exact marker", () => {
    const tracker = new MutationProgressTracker();
    const operation = tracker.track(receipt(row("accepted", 1), row("conflict", 1), row("rejected", 1)));
    tracker.settle(operation.markers[0]!, "accepted");
    tracker.settle(operation.markers[1]!, "conflict");
    tracker.settle(operation.markers[2]!, "rejected");

    const onchange = vi.fn();
    const cleanup = tracker.observe(operation, onchange);
    expect(onchange.mock.lastCall?.[0]).toMatchObject({ total: 3, accepted: 1, pending: 0, rejected: 1, conflict: 1 });
    cleanup();
  });

  it("keeps an observed offline operation alive past the unobserved TTL", () => {
    vi.useFakeTimers();
    try {
      const tracker = new MutationProgressTracker();
      const operation = tracker.track(receipt(row("offline", 1)));
      const onchange = vi.fn();
      const cleanup = tracker.observe(operation, onchange);

      vi.advanceTimersByTime(11 * 60_000);
      tracker.settle(operation.markers[0]!, "accepted");

      expect(onchange.mock.lastCall?.[0]).toMatchObject({ accepted: 1, pending: 0 });
      expect(tracker.activeObservers()).toBe(1);
      cleanup();
      expect(tracker.activeObservers()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("classifyPushedRow", () => {
  const marker: DirtyMarker = { table: "time_entries", id: "agent", updated_at: 7 };

  it("ignores server-owned sequence/default fields on an accepted agent row", () => {
    const expected = row("agent", 7, { agent_session_id: undefined, server_seq: undefined });
    const returned = row("agent", 7, { agent_session_id: null, server_seq: 99 });
    expect(classifyPushedRow(expected, marker, returned)).toBe("accepted");
  });

  it("classifies an echoed LWW winner as a conflict", () => {
    expect(classifyPushedRow(row("agent", 7), marker, row("agent", 8, { description: "remote" }))).toBe("conflict");
    expect(classifyPushedRow(row("agent", 7), marker, undefined)).toBe("conflict");
  });
});
