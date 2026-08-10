// Background sync engine: pushes dirty rows and pulls remote changes in a single
// POST /api/sync call. Local database stays usable offline; when connectivity
// returns, everything reconciles via last-write-wins on updated_at.
import {
  clearDirtyMarkers,
  getCursor,
  getMeta,
  listDirtyMarkers,
  mergeServerRows,
  readRows,
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

// A rejected row is quarantined by id *and* version, so the next edit of that row is
// retried rather than skipped forever. Keying by id alone made a single 400 - an
// over-long description, a tag the server reserves - strand the row on this device
// permanently, with the header still reading "synced".
function rejectionKey(marker: DirtyMarker): string {
  return `${marker.table}:${marker.id}@${marker.updated_at}`;
}

// rememberRejection keeps at most one quarantined version per row, so the set is
// bounded by the number of rows the server has ever refused rather than by the number
// of times the user retried them.
function rememberRejection(rejected: Set<string>, marker: DirtyMarker): void {
  forgetRejections(rejected, [marker]);
  rejected.add(rejectionKey(marker));
}

// forgetRejections clears a row's quarantine once any version of it reaches the server.
// Without this the set only ever grows: the row is synced, but the Settings banner goes
// on reporting a refusal that no longer exists, and the entry stays in IndexedDB for
// the life of the device.
function forgetRejections(rejected: Set<string>, markers: DirtyMarker[]): void {
  for (const marker of markers) {
    const prefix = `${marker.table}:${marker.id}@`;
    for (const key of rejected) {
      if (key.startsWith(prefix)) rejected.delete(key);
    }
  }
}

// quarantineFingerprint is compared against itself to decide whether the set needs
// writing back to IndexedDB. Sorted, so an unchanged set is recognised whatever order
// the entries were added in, and JSON-encoded rather than joined on a separator, which
// avoids inventing a delimiter that cannot occur in a key.
function quarantineFingerprint(rejected: Set<string>): string {
  return JSON.stringify([...rejected].sort());
}

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
    // A connection that stalls rather than resets - a captive portal, a suspended
    // radio - leaves the promise pending forever, and syncInFlight is only released in
    // the finally of syncNow. Without a deadline that one request stops the tab from
    // ever syncing again, with the header stuck on "syncing" and no error anywhere.
    //
    // The deadline is generous because the first sync of a device is one response
    // carrying the whole history, and it either completes or starts over - there is no
    // partial progress to keep. A tight timeout would leave a phone on a slow link
    // unable to bootstrap at all, retrying the same megabytes every 30 seconds.
    signal: AbortSignal.timeout(5 * 60_000),
  });
  if (!response.ok) return { status: response.status };
  return { status: response.status, payload: (await response.json()) as SyncResponse };
}

async function applyPayload(payload: SyncResponse, pushed: PendingRow[] = []): Promise<boolean> {
  const justPushed = new Set(pushed.map((item) => `${item.marker.table}:${item.marker.id}`));
  const merged = await mergeServerRows(payload.changes, justPushed);
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
    const merged = await applyPayload(result.payload, pending);
    const markers = pending.map((item) => item.marker);
    forgetRejections(rejected, markers);
    await clearDirtyMarkers(markers);
    return { merged };
  }
  if (result.status !== 400 || pending.length === 0) {
    return { merged: false, fatalStatus: result.status };
  }

  const onlyRow = pending.length === 1 ? pending[0] : undefined;
  if (onlyRow) {
    rememberRejection(rejected, onlyRow.marker);
    await clearDirtyMarkers([onlyRow.marker]);
    return { merged: false };
  }

  let merged = false;
  const settled: DirtyMarker[] = [];
  for (const item of pending) {
    const single = await postSync(changesFrom([item]));
    if (single.payload) {
      merged = (await applyPayload(single.payload, [item])) || merged;
      forgetRejections(rejected, [item.marker]);
      settled.push(item.marker);
      continue;
    }
    if (single.status !== 400) {
      await clearDirtyMarkers(settled);
      return { merged, fatalStatus: single.status };
    }
    rememberRejection(rejected, item.marker);
    settled.push(item.marker);
  }
  await clearDirtyMarkers(settled);
  return { merged };
}

async function syncOnce(): Promise<void> {
  if (!navigator.onLine) {
    syncState.status = "offline";
    return;
  }
  syncState.status = "syncing";

  const rejected = new Set((await getMeta<string[]>(rejectedMetaKey)) ?? []);
  const rejectedBefore = quarantineFingerprint(rejected);
  // The quarantine is persisted whatever happens below, including the early return on
  // 401 and the throw on a fatal status. pushChunk clears the dirty marker of every row
  // it quarantines, so losing the set on the way out would leave those rows in neither
  // the push queue nor the warning - gone from the server's copy with nothing on screen
  // to say so.
  const persistQuarantine = async () => {
    if (quarantineFingerprint(rejected) !== rejectedBefore) {
      await setMeta(rejectedMetaKey, [...rejected]);
    }
    syncState.rejectedCount = rejected.size;
  };

  try {
    const markers = (await listDirtyMarkers()).filter((marker) => !rejected.has(rejectionKey(marker)));
    const rows = await readRows(markers);
    const pending: PendingRow[] = [];
    for (const marker of markers) {
      const row = rows.get(`${marker.table}:${marker.id}`);
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

    await refreshPendingCount();

    syncState.status = "synced";
    syncState.lastSyncedAt = Date.now();
    if (merged) {
      window.dispatchEvent(new CustomEvent(SYNC_MERGED_EVENT));
    }
  } catch (error) {
    console.error("sync error", error);
    syncState.status = navigator.onLine ? "error" : "offline";
  } finally {
    await persistQuarantine();
  }
}
