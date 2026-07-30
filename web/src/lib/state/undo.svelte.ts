// Undo state for entry deletion. Lives at module level so the toast can be
// mounted in the app shell: in-app navigation must not dismiss the 8-second
// undo window, only the timer or an explicit action does.
import type { TimeEntry } from "../types";
import { deleteEntry, restoreEntry } from "./app.svelte";

const UNDO_WINDOW_MS = 8000;

export const undoState = $state({ deleted: null as TimeEntry | null });

let timer: ReturnType<typeof setTimeout> | null = null;

export async function deleteEntryWithUndo(entry: TimeEntry): Promise<void> {
  // Snapshot before the delete removes the row from appState: the toast needs
  // the description, and $state proxies must not leak out of the store.
  const snapshot = $state.snapshot(entry) as TimeEntry;
  await deleteEntry(entry.id);
  undoState.deleted = snapshot;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    undoState.deleted = null;
    timer = null;
  }, UNDO_WINDOW_MS);
}

export async function undoDelete(): Promise<void> {
  const entry = undoState.deleted;
  if (!entry) return;
  dismissUndo();
  await restoreEntry(entry.id);
}

export function dismissUndo(): void {
  if (timer) clearTimeout(timer);
  timer = null;
  undoState.deleted = null;
}
