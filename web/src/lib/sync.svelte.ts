// Background sync engine: pushes dirty rows and pulls remote changes in a single
// POST /api/sync call. Local database stays usable offline; when connectivity
// returns, everything reconciles via last-write-wins on updated_at.
import {
  clearDirtyMarker,
  getCursor,
  getMeta,
  getRow,
  listDirtyMarkers,
  mergeServerRows,
  setCursor,
  setMeta,
  type DirtyMarker,
} from "./db";
import type { SyncChanges, SyncedRow, SyncResponse } from "./types";

export type SyncStatus = "idle" | "syncing" | "synced" | "offline" | "error" | "unauthenticated";

// The server caps a batch at 10000 rows and validates the whole batch before opening
// its transaction, so pushes are chunked well under the cap. A tag rename or merge in
// Settings rewrites every affected entry and is the operation that reaches this size.
const maxPushRows = 2000;
const rejectedMetaKey = "rejected";

export const syncState = $state({
  status: "idle" as SyncStatus,
  pendingCount: 0,
  rejectedCount: 0,
  lastSyncedAt: 0,
});

// The MERGED event tells app state to reload from IndexedDB after a pull.
export const SYNC_MERGED_EVENT = "worktime:sync-merged";

let syncInFlight = false;
let syncQueued = false;
let debounceHandle: ReturnType<typeof setTimeout> | undefined;

export function startSyncEngine(): void {
  window.addEventListener("online", () => void syncNow());
  window.addEventListener("offline", () => {
    syncState.status = "offline";
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") void syncNow();
  });
  setInterval(() => void syncNow(), 30_000);
  void syncNow();
}

// requestSync schedules a sync shortly after a local change, coalescing bursts.
export function requestSync(): void {
  void refreshPendingCount();
  clearTimeout(debounceHandle);
  debounceHandle = setTimeout(() => void syncNow(), 1_500);
}

async function refreshPendingCount(): Promise<void> {
  syncState.pendingCount = (await listDirtyMarkers()).length;
}

export async function syncNow(): Promise<void> {
  if (syncInFlight) {
    syncQueued = true;
    return;
  }
  syncInFlight = true;
  try {
    do {
      syncQueued = false;
      await syncOnce();
    } while (syncQueued);
  } finally {
    syncInFlight = false;
  }
}

interface PendingRow {
  marker: DirtyMarker;
  row: SyncedRow;
}

function changesFrom(pending: PendingRow[]): SyncChanges {
  const changes: SyncChanges = {};
  for (const item of pending) {
    const bucket = (changes[item.marker.table] ??= [] as never[]) as SyncedRow[];
    bucket.push(item.row);
  }
  return changes;
}

async function postSync(changes: SyncChanges): Promise<{ status: number; payload?: SyncResponse }> {
  const response = await fetch("/api/sync", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ since: await getCursor(), changes }),
  });
  if (!response.ok) return { status: response.status };
  return { status: response.status, payload: (await response.json()) as SyncResponse };
}

async function applyPayload(payload: SyncResponse): Promise<boolean> {
  const merged = await mergeServerRows(payload.changes);
  await setCursor(payload.seq);
  return merged;
}

// pushChunk sends one batch and, on a 400, isolates the row the server refuses.
// Validation runs before the server opens its transaction, so a rejected batch wrote
// nothing and retrying row by row is safe. Without this a single malformed row blocks
// every pending change on the device forever and surfaces only as "sync error".
async function pushChunk(
  pending: PendingRow[],
  rejected: Set<string>,
): Promise<{ merged: boolean; fatalStatus?: number }> {
  const result = await postSync(changesFrom(pending));
  if (result.payload) {
    const merged = await applyPayload(result.payload);
    for (const item of pending) {
      await clearDirtyMarker(item.marker);
    }
    return { merged };
  }
  if (result.status !== 400 || pending.length === 0) {
    return { merged: false, fatalStatus: result.status };
  }

  const onlyRow = pending.length === 1 ? pending[0] : undefined;
  if (onlyRow) {
    rejected.add(`${onlyRow.marker.table}:${onlyRow.marker.id}`);
    await clearDirtyMarker(onlyRow.marker);
    return { merged: false };
  }

  let merged = false;
  for (const item of pending) {
    const single = await postSync(changesFrom([item]));
    if (single.payload) {
      merged = (await applyPayload(single.payload)) || merged;
      await clearDirtyMarker(item.marker);
      continue;
    }
    if (single.status !== 400) return { merged, fatalStatus: single.status };
    rejected.add(`${item.marker.table}:${item.marker.id}`);
    await clearDirtyMarker(item.marker);
  }
  return { merged };
}

async function syncOnce(): Promise<void> {
  if (!navigator.onLine) {
    syncState.status = "offline";
    return;
  }
  syncState.status = "syncing";

  try {
    const rejected = new Set((await getMeta<string[]>(rejectedMetaKey)) ?? []);
    const rejectedBefore = rejected.size;

    const pending: PendingRow[] = [];
    for (const marker of await listDirtyMarkers()) {
      if (rejected.has(`${marker.table}:${marker.id}`)) continue;
      const row = await getRow(marker.table, marker.id);
      if (!row) continue;
      pending.push({ marker, row });
    }

    const chunks: PendingRow[][] = [];
    for (let index = 0; index < pending.length; index += maxPushRows) {
      chunks.push(pending.slice(index, index + maxPushRows));
    }
    // With nothing to push we still make one request, because that is also the pull.
    if (chunks.length === 0) chunks.push([]);

    let merged = false;
    for (const chunk of chunks) {
      const result = await pushChunk(chunk, rejected);
      merged = result.merged || merged;
      if (result.fatalStatus !== undefined) {
        if (result.fatalStatus === 401) {
          syncState.status = "unauthenticated";
          return;
        }
        throw new Error(`sync failed: ${result.fatalStatus}`);
      }
    }

    if (rejected.size !== rejectedBefore) {
      await setMeta(rejectedMetaKey, [...rejected]);
    }
    syncState.rejectedCount = rejected.size;
    await refreshPendingCount();

    syncState.status = "synced";
    syncState.lastSyncedAt = Date.now();
    if (merged) {
      window.dispatchEvent(new CustomEvent(SYNC_MERGED_EVENT));
    }
  } catch (error) {
    console.error("sync error", error);
    syncState.status = navigator.onLine ? "error" : "offline";
  }
}
