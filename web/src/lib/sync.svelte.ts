// Background sync engine: pushes dirty rows and pulls remote changes in a single
// POST /api/sync call. Local database stays usable offline; when connectivity
// returns, everything reconciles via last-write-wins on updated_at.
import {
  clearDirtyMarker,
  getCursor,
  getRow,
  listDirtyMarkers,
  saveServerRow,
  setCursor,
  TABLES,
} from "./db";
import type { SyncChanges, SyncedRow, SyncResponse, TableName } from "./types";

export type SyncStatus = "idle" | "syncing" | "synced" | "offline" | "error" | "unauthenticated";

export const syncState = $state({
  status: "idle" as SyncStatus,
  pendingCount: 0,
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

async function syncOnce(): Promise<void> {
  if (!navigator.onLine) {
    syncState.status = "offline";
    return;
  }
  syncState.status = "syncing";

  try {
    const markers = await listDirtyMarkers();
    const changes: SyncChanges = {};
    for (const marker of markers) {
      const row = await getRow(marker.table, marker.id);
      if (!row) continue;
      const bucket = (changes[marker.table] ??= [] as never[]) as SyncedRow[];
      bucket.push(row);
    }

    const response = await fetch("/api/sync", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ since: await getCursor(), changes }),
    });
    if (response.status === 401) {
      syncState.status = "unauthenticated";
      return;
    }
    if (!response.ok) {
      throw new Error(`sync failed: ${response.status}`);
    }

    const payload: SyncResponse = await response.json();
    let merged = false;
    for (const table of TABLES) {
      for (const incoming of payload.changes[table] ?? []) {
        const local = await getRow(table, incoming.id);
        // Keep local edits that are newer than what the server returned; they are
        // still dirty and will win on the next push.
        if (!local || incoming.updated_at >= local.updated_at) {
          await saveServerRow(table, incoming);
          merged = true;
        }
      }
    }
    await setCursor(payload.seq);
    for (const marker of markers) {
      await clearDirtyMarker(marker);
    }
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
