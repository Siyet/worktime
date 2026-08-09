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

// saveServerRow persists a row received from the server without marking it dirty.
export async function saveServerRow(table: TableName, row: SyncedRow): Promise<void> {
  await (await db()).put(table, row);
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
      // the client has no other way to learn (paused_ms, agent_session_id, a
      // rename by the agent flow). Skipping those loses them for good - the
      // cursor has already moved past that server_seq. The updated_at comparison
      // below still protects an edit made while the push was in flight.
      if (!justPushed.has(`${table}:${row.id}`) && (await dirty.get(`${table}:${row.id}`))) continue;
      const local = (await store.get(row.id)) as SyncedRow | undefined;
      if (!local || row.updated_at >= local.updated_at) {
        void store.put(row);
        merged = true;
      }
    }
  }
  await transaction.done;
  return merged;
}

export async function listDirtyMarkers(): Promise<DirtyMarker[]> {
  return (await db()).getAll("dirty");
}

// clearDirtyMarker removes the push queue entry unless the row was edited again
// while the push was in flight (its updated_at moved past the pushed one).
export async function clearDirtyMarker(marker: DirtyMarker): Promise<void> {
  const database = await db();
  const transaction = database.transaction("dirty", "readwrite");
  const key = `${marker.table}:${marker.id}`;
  const current: DirtyMarker | undefined = await transaction.store.get(key);
  if (current && current.updated_at <= marker.updated_at) {
    await transaction.store.delete(key);
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
export async function wipeLocalData(): Promise<void> {
  const database = await db();
  database.close();
  dbPromise = null;
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("worktime");
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error);
    request.onblocked = () => resolve();
  });
}
