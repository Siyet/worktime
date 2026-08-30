import { rejectionKey, sameStoredRow, type DirtyMarker, type LocalMutationReceipt } from "./db";
import type { SyncedRow } from "./types";

export type MutationMemberStatus = "pending" | "accepted" | "rejected" | "conflict";

export interface MutationProgress {
  total: number;
  pending: number;
  accepted: number;
  rejected: number;
  conflict: number;
  members: Array<{ marker: DirtyMarker; status: MutationMemberStatus }>;
}

interface Observer {
  members: Map<string, { marker: DirtyMarker; status: MutationMemberStatus }>;
  onchange?: (progress: MutationProgress) => void;
  timeout?: ReturnType<typeof setTimeout>;
}

export interface TrackedLocalMutationReceipt extends LocalMutationReceipt {
  trackingID: number;
}

export function summarizeMutationMembers(
  members: Iterable<{ marker: DirtyMarker; status: MutationMemberStatus }>,
): MutationProgress {
  const progress: MutationProgress = { total: 0, pending: 0, accepted: 0, rejected: 0, conflict: 0, members: [] };
  for (const member of members) {
    progress.total++;
    progress[member.status]++;
    progress.members.push(member);
  }
  return progress;
}

// The tracker is intentionally transient. It reports the exact versions sent while
// this page is alive; after a reload the global pending/rejected UI remains the honest
// source of truth instead of reconstructing an outcome from newer IndexedDB rows.
export class MutationProgressTracker {
  private nextID = 1;
  private readonly observers = new Map<number, Observer>();
  private readonly operationsByMarker = new Map<string, Set<number>>();

  track(receipt: LocalMutationReceipt): TrackedLocalMutationReceipt {
    const id = this.nextID++;
    const members = new Map(
      receipt.markers.map((marker) => [rejectionKey(marker), { marker, status: "pending" as const }]),
    );
    const timeout = setTimeout(() => this.dispose(id), 10 * 60_000);
    this.observers.set(id, { members, timeout });
    for (const key of members.keys()) {
      const operations = this.operationsByMarker.get(key) ?? new Set<number>();
      operations.add(id);
      this.operationsByMarker.set(key, operations);
    }
    return { ...receipt, trackingID: id };
  }

  observe(receipt: TrackedLocalMutationReceipt, onchange: (progress: MutationProgress) => void): () => void {
    const observer = this.observers.get(receipt.trackingID);
    if (!observer) {
      onchange(summarizeMutationMembers([]));
      return () => {};
    }
    clearTimeout(observer.timeout);
    observer.timeout = undefined;
    observer.onchange = onchange;
    onchange(summarizeMutationMembers(observer.members.values()));
    return () => this.dispose(receipt.trackingID);
  }

  settle(marker: DirtyMarker, status: Exclude<MutationMemberStatus, "pending">): void {
    const key = rejectionKey(marker);
    for (const id of this.operationsByMarker.get(key) ?? []) {
      const observer = this.observers.get(id);
      if (!observer) continue;
      const member = observer.members.get(key);
      if (member?.status !== "pending") continue;
      member.status = status;
      observer.onchange?.(summarizeMutationMembers(observer.members.values()));
    }
  }

  dispose(id: number): void {
    const observer = this.observers.get(id);
    if (!observer) return;
    clearTimeout(observer.timeout);
    for (const key of observer.members.keys()) {
      const operations = this.operationsByMarker.get(key);
      operations?.delete(id);
      if (operations?.size === 0) this.operationsByMarker.delete(key);
    }
    this.observers.delete(id);
  }

  activeObservers(): number {
    return this.observers.size;
  }
}

export function classifyPushedRow(expected: SyncedRow, marker: DirtyMarker, returned: SyncedRow | undefined): "accepted" | "conflict" {
  if (returned === undefined || returned.updated_at !== marker.updated_at) return "conflict";
  return sameStoredRow(expected, returned) ? "accepted" : "conflict";
}

export const mutationProgressTracker = new MutationProgressTracker();

export function trackLocalMutation(receipt: LocalMutationReceipt): TrackedLocalMutationReceipt {
  return mutationProgressTracker.track(receipt);
}

export function disposeTrackedLocalMutation(receipt: TrackedLocalMutationReceipt): void {
  mutationProgressTracker.dispose(receipt.trackingID);
}

export function observeLocalMutation(
  receipt: TrackedLocalMutationReceipt,
  onchange: (progress: MutationProgress) => void,
): () => void {
  return mutationProgressTracker.observe(receipt, onchange);
}
