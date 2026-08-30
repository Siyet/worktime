// IndexedDB layer: the local source of truth. The UI reads and writes only this
// database; the sync engine reconciles it with the server in the background.
import { openDB, type IDBPDatabase } from "idb";
import type { SyncedRow, TableName } from "./types";

export const TABLES: TableName[] = ["projects", "time_entries", "time_off"];

export interface DirtyMarker {
  table: TableName;
  id: string;
  updated_at: number;
}

let dbPromise: Promise<IDBPDatabase> | null = null;

function db(): Promise<IDBPDatabase> {
  dbPromise ??= openDB("worktime", 1, {
    upgrade(database) {
      for (const table of TABLES) {
        database.createObjectStore(table, { keyPath: "id" });
      }
      database.createObjectStore("meta");
      // Dirty markers queue local changes for push; key is `${table}:${id}`.
      database.createObjectStore("dirty");
    },
    // Another tab is deleting the database, which only happens on logout or when a
    // different account signs in. Holding the connection would block that delete; and
    // carrying on afterwards would be worse than blocking it, because this tab's sync
    // engine keeps running on its 30-second timer and would push the previous account's
    // rows under the new account's cookie. Let go and reload into whoever is signed in
    // now - the tab that started the wipe has already stored the new user id, so this
    // does not bounce back and forth.
    blocking() {
      dbPromise = null;
      window.location.reload();
    },
  });
  return dbPromise;
}

export async function getAllRows<Row extends SyncedRow>(table: TableName): Promise<Row[]> {
  return (await db()).getAll(table);
}

export async function getRow<Row extends SyncedRow>(table: TableName, id: string): Promise<Row | undefined> {
  return (await db()).get(table, id);
}

// saveLocalRow persists a user-made change and queues it for sync.
export async function saveLocalRow(table: TableName, row: SyncedRow): Promise<void> {
  const database = await db();
  const transaction = database.transaction([table, "dirty"], "readwrite");
  await transaction.objectStore(table).put(row);
  const marker: DirtyMarker = { table, id: row.id, updated_at: row.updated_at };
  await transaction.objectStore("dirty").put(marker, `${table}:${row.id}`);
  await transaction.done;
}

// readRows fetches the rows behind a set of dirty markers in one transaction. Opening
// one per row is the anti-pattern mergeServerRows exists to avoid, and the push path
// reaches the same size: a tag rename in Settings rewrites every affected entry.
export async function readRows(markers: DirtyMarker[]): Promise<Map<string, SyncedRow>> {
  const rows = new Map<string, SyncedRow>();
  if (markers.length === 0) return rows;
  const database = await db();
  const transaction = database.transaction([...TABLES], "readonly");
  for (const marker of markers) {
    const row = (await transaction.objectStore(marker.table).get(marker.id)) as SyncedRow | undefined;
    if (row) rows.set(`${marker.table}:${marker.id}`, row);
  }
  await transaction.done;
  return rows;
}

// mergeServerRows applies a pulled batch in a single IndexedDB transaction.
// One transaction per row would take tens of seconds on a 10k-row bootstrap;
// batching brings it down to well under a second. Rows older than the local
// version (pending local edits) are kept.
export async function mergeServerRows(
  changes: Partial<Record<TableName, SyncedRow[]>>,
  justPushed: ReadonlySet<string> = new Set(),
): Promise<boolean> {
  const database = await db();
  const transaction = database.transaction([...TABLES, "dirty"], "readwrite");
  const dirty = transaction.objectStore("dirty");
  let merged = false;
  for (const table of TABLES) {
    const incoming = changes[table] ?? [];
    if (incoming.length === 0) continue;
    const store = transaction.objectStore(table);
    for (const row of incoming) {
      // A row that still holds a dirty marker has a local edit waiting to be pushed,
      // and the server copy would silently revert it.
      //
      // Rows pushed in this very request are the exception: their marker is only
      // cleared after this merge, and the response can carry server-owned fields
      // the client has no other way to learn (agent_session_id or a rename by
      // the agent flow). Skipping those loses them for good - the
      // cursor has already moved past that server_seq. The updated_at comparison
      // below still protects an edit made while the push was in flight.
      if (!justPushed.has(`${table}:${row.id}`) && (await dirty.get(`${table}:${row.id}`))) continue;
      const local = (await store.get(row.id)) as SyncedRow | undefined;
      if (!local || row.updated_at >= local.updated_at) {
        void store.put(row);
        // Every successful push is echoed back with the same updated_at, so reporting
        // a merge on equality alone would make each local edit reload the whole state
        // from disk. Only a row that actually differs is worth waking the app for.
        if (!local || !sameStoredRow(local, row)) merged = true;
      }
    }
  }
  await transaction.done;
  return merged;
}

// sameStoredRow compares two versions of a row by content. server_seq is excluded: it
// moves on every write and says nothing about what the UI would render.
function sameStoredRow(local: SyncedRow, incoming: SyncedRow): boolean {
  const fields = new Set([...Object.keys(local), ...Object.keys(incoming)]);
  fields.delete("server_seq");
  for (const field of fields) {
    const localValue = (local as unknown as Record<string, unknown>)[field];
    const incomingValue = (incoming as unknown as Record<string, unknown>)[field];
    if (Array.isArray(localValue) || Array.isArray(incomingValue)) {
      const localItems = Array.isArray(localValue) ? localValue : [];
      const incomingItems = Array.isArray(incomingValue) ? incomingValue : [];
      if (localItems.length !== incomingItems.length) return false;
      if (localItems.some((item, index) => item !== incomingItems[index])) return false;
      continue;
    }
    // A locally created row omits agent_session_id, while the server sends it as
    // null. Comparing those as different would make every new timer's own echo
    // look like a change and reload the whole state from disk - the exact cost
    // this check exists to avoid.
    if (localValue === undefined && (incomingValue === null || incomingValue === 0)) continue;
    if (localValue !== incomingValue) return false;
  }
  return true;
}

export async function listDirtyMarkers(): Promise<DirtyMarker[]> {
  return (await db()).getAll("dirty");
}

// clearDirtyMarkers removes push queue entries unless the row was edited again while
// the push was in flight (its updated_at moved past the pushed one). The read and the
// delete share one transaction, so that check cannot race; the whole batch shares it
// too, because a chunk can hold thousands of markers.
export async function clearDirtyMarkers(markers: DirtyMarker[]): Promise<void> {
  if (markers.length === 0) return;
  const database = await db();
  const transaction = database.transaction("dirty", "readwrite");
  for (const marker of markers) {
    const key = `${marker.table}:${marker.id}`;
    const current: DirtyMarker | undefined = await transaction.store.get(key);
    if (current && current.updated_at <= marker.updated_at) {
      void transaction.store.delete(key);
    }
  }
  await transaction.done;
}

export async function getCursor(): Promise<number> {
  return (await (await db()).get("meta", "cursor")) ?? 0;
}

export async function setCursor(seq: number): Promise<void> {
  await (await db()).put("meta", seq, "cursor");
}

export async function getMeta<Value>(key: string): Promise<Value | undefined> {
  return (await db()).get("meta", key);
}

export async function setMeta(key: string, value: unknown): Promise<void> {
  await (await db()).put("meta", value, key);
}

// wipeLocalData drops the whole local database. Used on logout and when a
// different account signs in on the same browser.
//
// A blocked delete is a failure, not a success: another tab is still holding the
// database, so the previous account's rows are all still there. Reporting success would
// have the caller carry on and sign the new account in on top of them. The blocking
// handler above asks the other tab to let go, so this normally resolves a moment later;
// the timeout is for a tab too old to have that handler.
export async function wipeLocalData(): Promise<void> {
  const database = await db();
  database.close();
  dbPromise = null;
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("worktime");
    let blockedTimer: ReturnType<typeof setTimeout> | undefined;
    const settle = (finish: () => void) => {
      clearTimeout(blockedTimer);
      finish();
    };
    request.onsuccess = () => settle(resolve);
    request.onerror = () => settle(() => reject(request.error));
    request.onblocked = () => {
      blockedTimer = setTimeout(() => reject(new Error("local data is still open in another tab")), 5_000);
    };
  });
}
