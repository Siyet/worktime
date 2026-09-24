// Undo state for deleting an entry and for stopping timers. Lives at module
// level so the toast can be mounted in the app shell: in-app navigation must not
// dismiss the 8-second undo window, only the timer or an explicit action does.
// A delete is undoable on its own; stops add up while the window is open, since
// the line below a stopped one slides up under the same thumb or key, and a
// second quick press must not leave the first stop without a way back.
import type { TimeEntry } from "../types";
import { deleteEntry, restoreEntry, stopTimer, updateEntry } from "./app.svelte";

const UNDO_WINDOW_MS = 8000;

export const undoState = $state({ deleted: null as TimeEntry | null, stopped: [] as TimeEntry[] });

let timer: ReturnType<typeof setTimeout> | null = null;

function openWindow(): void {
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    undoState.deleted = null;
    undoState.stopped = [];
    timer = null;
  }, UNDO_WINDOW_MS);
}

export async function deleteEntryWithUndo(entry: TimeEntry): Promise<void> {
  // Snapshot before the delete removes the row from appState: the toast needs
  // the description, and $state proxies must not leak out of the store.
  const snapshot = $state.snapshot(entry) as TimeEntry;
  await deleteEntry(entry.id);
  undoState.stopped = [];
  undoState.deleted = snapshot;
  openWindow();
}

// A Stop is one tap in the pinned strip, right where the thumb rests, so it gets
// the same way back as a delete. The undo restarts the same row rather than a
// copy: it keeps its original start, and for an agent row the server hands the
// session back to it (see docs/agent-tracking.md).
export async function stopTimerWithUndo(entry: TimeEntry): Promise<void> {
  if (entry.stopped_at !== null) return;
  const snapshot = $state.snapshot(entry) as TimeEntry;
  await stopTimer(entry.id);
  undoState.deleted = null;
  undoState.stopped = [...undoState.stopped, snapshot];
  openWindow();
}

export async function undoLast(): Promise<void> {
  const deleted = undoState.deleted;
  const stopped = undoState.stopped;
  dismissUndo();
  if (deleted) await restoreEntry(deleted.id);
  for (const entry of stopped) await updateEntry(entry.id, { stopped_at: null });
}

export function dismissUndo(): void {
  if (timer) clearTimeout(timer);
  timer = null;
  undoState.deleted = null;
  undoState.stopped = [];
}
