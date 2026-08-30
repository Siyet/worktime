// Application state: runes-based stores backed by IndexedDB. Every mutation
// writes to the local database first, then pokes the sync engine.
import { getAllRows, getRow, saveLocalRow, saveLocalRows, type LocalMutationReceipt } from "../db";
import { maxTagLength } from "../limits";
import {
  disposeTrackedLocalMutation,
  trackLocalMutation,
  type TrackedLocalMutationReceipt,
} from "../mutation-progress";
import { requestSync, SYNC_MERGED_EVENT } from "../sync.svelte";
import type { Project, TimeEntry, TimeOff, TimeOffKind } from "../types";
import { uuidv7 } from "../uuid";

export const appState = $state({
  loaded: false,
  projects: [] as Project[],
  entries: [] as TimeEntry[],
  timeOff: [] as TimeOff[],
});

// A shared one-second ticker drives all running timer displays.
export const clock = $state({ now: Date.now() });
setInterval(() => {
  clock.now = Date.now();
}, 1000);

export async function loadStateFromDB(): Promise<void> {
  const [projects, entries, timeOff] = await Promise.all([
    getAllRows<Project>("projects"),
    getAllRows<TimeEntry>("time_entries"),
    getAllRows<TimeOff>("time_off"),
  ]);
  appState.projects = projects.filter((row) => row.deleted_at === null);
  appState.entries = entries.filter((row) => row.deleted_at === null);
  appState.timeOff = timeOff.filter((row) => row.deleted_at === null);
  appState.loaded = true;
}

window.addEventListener(SYNC_MERGED_EVENT, () => void loadStateFromDB());

// IndexedDB stores values through structured clone, which refuses a Proxy. Anything
// read out of $state is one, and spreading a row only copies the reference to nested
// values such as the tags array - so every row is snapshotted on its way to disk.
function persistable<Row>(row: Row): Row {
  return $state.snapshot(row) as Row;
}

function upsertInto<Row extends { id: string }>(rows: Row[], row: Row): void {
  const index = rows.findIndex((candidate) => candidate.id === row.id);
  if (index >= 0) {
    rows[index] = row;
  } else {
    rows.push(row);
  }
}

function removeFrom<Row extends { id: string }>(rows: Row[], id: string): void {
  const index = rows.findIndex((candidate) => candidate.id === id);
  if (index >= 0) {
    rows.splice(index, 1);
  }
}

// updated_at is both a wall clock and a version: conflicts resolve last-write-wins on
// it. A clock that jumps backwards - NTP correction, waking from sleep - would then
// make an edit lose to the version it replaces, and the server would silently keep the
// old one. Stepping past the previous value keeps every edit strictly newer than what
// it edits, and two edits can never share a millisecond.
function nextUpdatedAt(previous: { updated_at: number }): number {
  return Math.max(Date.now(), previous.updated_at + 1);
}

// --- time entries ---

export async function startTimer(description: string, projectID: string | null, tags: string[] = []): Promise<void> {
  const now = Date.now();
  const entry: TimeEntry = {
    id: uuidv7(),
    project_id: projectID,
    description,
    tags: sortTags(tags),
    started_at: now,
    stopped_at: null,
    created_at: now,
    updated_at: now,
    deleted_at: null,
  };
  await saveLocalRow("time_entries", persistable(entry));
  upsertInto(appState.entries, entry);
  requestSync();
}

export async function stopTimer(entryID: string): Promise<void> {
  const entry = appState.entries.find((candidate) => candidate.id === entryID);
  if (!entry || entry.stopped_at !== null) return;
  await updateEntry(entryID, { stopped_at: Date.now() });
}

// updateEntry merges a patch into whatever the row looks like right now, rather than
// writing a caller-supplied snapshot. The editor can stay open while a timer is stopped
// on another device, and a caller written before tags existed cannot blank them.
export async function updateEntry(entryID: string, patch: Partial<TimeEntry>): Promise<void> {
  const entry = appState.entries.find((candidate) => candidate.id === entryID);
  if (!entry) return;
  const updated: TimeEntry = { ...entry, ...patch, id: entry.id, updated_at: nextUpdatedAt(entry) };
  if (patch.tags) updated.tags = sortTags(patch.tags);
  await saveLocalRow("time_entries", persistable(updated));
  upsertInto(appState.entries, updated);
  requestSync();
}

export type GroupEntryPatch = Partial<Pick<TimeEntry, "description" | "project_id" | "tags">>;

// updateEntries is one local-first user action: every member is resolved again at
// save time, patched over its latest row, and committed with all dirty markers in one
// IndexedDB transaction. appState changes only after that transaction succeeds.
export async function updateEntries(entryIDs: readonly string[], patch: GroupEntryPatch): Promise<TrackedLocalMutationReceipt> {
  if (Object.keys(patch).length === 0) return trackLocalMutation({ markers: [], rows: [] });
  const ids = [...new Set(entryIDs)];
  const entries = ids.map((id) => appState.entries.find((candidate) => candidate.id === id));
  if (entries.some((entry) => entry === undefined)) {
    throw new Error("group member is no longer available");
  }
  const updated = entries.map((entry) => {
    const current = entry!;
    const next: TimeEntry = { ...current, ...patch, id: current.id, updated_at: nextUpdatedAt(current) };
    if (patch.tags !== undefined) next.tags = sortTags(patch.tags);
    return next;
  });
  const stored = updated.map(persistable);
  // Track before the IndexedDB commit becomes visible. A sync already in flight
  // can read a just-committed marker before this async function resumes.
  const prepared: LocalMutationReceipt = {
    markers: stored.map((entry) => ({ table: "time_entries", id: entry.id, updated_at: entry.updated_at })),
    rows: stored,
  };
  const tracked = trackLocalMutation(prepared);
  try {
    await saveLocalRows("time_entries", stored);
  } catch (error) {
    disposeTrackedLocalMutation(tracked);
    throw error;
  }
  for (const entry of updated) upsertInto(appState.entries, entry);
  requestSync();
  return tracked;
}

export async function deleteEntry(entryID: string): Promise<void> {
  const entry = appState.entries.find((candidate) => candidate.id === entryID);
  if (!entry) return;
  const now = nextUpdatedAt(entry);
  await saveLocalRow("time_entries", persistable({ ...entry, deleted_at: now, updated_at: now }));
  removeFrom(appState.entries, entryID);
  requestSync();
}

// restoreEntry is the whole undo implementation: clearing the tombstone with a fresh
// updated_at makes the restore win last-write-wins on every device, and it works offline.
export async function restoreEntry(entryID: string): Promise<TimeEntry | undefined> {
  const buried = await getRow<TimeEntry>("time_entries", entryID);
  if (!buried) return undefined;
  const restored: TimeEntry = { ...buried, deleted_at: null, updated_at: nextUpdatedAt(buried) };
  await saveLocalRow("time_entries", persistable(restored));
  upsertInto(appState.entries, restored);
  requestSync();
  return restored;
}

// --- projects ---

export async function createProject(name: string, color: string): Promise<Project> {
  const now = Date.now();
  const project: Project = {
    id: uuidv7(),
    name,
    color,
    archived: false,
    created_at: now,
    updated_at: now,
    deleted_at: null,
  };
  await saveLocalRow("projects", persistable(project));
  upsertInto(appState.projects, project);
  requestSync();
  return project;
}

export async function updateProject(updatedProject: Project): Promise<void> {
  const updated: Project = { ...updatedProject, updated_at: nextUpdatedAt(updatedProject) };
  await saveLocalRow("projects", persistable(updated));
  upsertInto(appState.projects, updated);
  requestSync();
}

export async function deleteProject(projectID: string): Promise<void> {
  const project = appState.projects.find((candidate) => candidate.id === projectID);
  if (!project) return;
  const now = nextUpdatedAt(project);
  await saveLocalRow("projects", persistable({ ...project, deleted_at: now, updated_at: now }));
  removeFrom(appState.projects, projectID);
  requestSync();
}

// --- time off ---

export async function addTimeOff(kind: TimeOffKind, dateFrom: string, dateTo: string, note: string): Promise<void> {
  const now = Date.now();
  const timeOff: TimeOff = {
    id: uuidv7(),
    kind,
    date_from: dateFrom,
    date_to: dateTo,
    note,
    created_at: now,
    updated_at: now,
    deleted_at: null,
  };
  await saveLocalRow("time_off", persistable(timeOff));
  upsertInto(appState.timeOff, timeOff);
  requestSync();
}

export async function deleteTimeOff(timeOffID: string): Promise<void> {
  const timeOff = appState.timeOff.find((candidate) => candidate.id === timeOffID);
  if (!timeOff) return;
  const now = nextUpdatedAt(timeOff);
  await saveLocalRow("time_off", persistable({ ...timeOff, deleted_at: now, updated_at: now }));
  removeFrom(appState.timeOff, timeOffID);
  requestSync();
}

// --- tags ---

// Tags on this instance start from these five; the list is not a limit, only a floor,
// so the picker is never empty on a fresh instance.
export const seedTags = ["analysis", "development", "meeting", "other", "review"];

export { maxTagsPerEntry } from "../limits";

// normaliseTag produces the single canonical spelling of a name. Tags are values, not
// ids, so two spellings of the same word would silently split a report group in two.
// The leading underscore is stripped rather than merely rejected downstream: the server
// reserves that prefix for the "__untagged" report bucket, and a tag it refuses takes
// the whole entry down with it - the push 400s and the row is quarantined.
export function normaliseTag(name: string): string {
  return name
    .trim()
    .replace(/\s+/g, " ")
    .toLowerCase()
    .replace(/^_+/, "")
    .slice(0, maxTagLength);
}

// Stored sorted, so the one chip a row has room for is predictable rather than
// whichever tag happened to be added first.
function sortTags(tags: string[]): string[] {
  return [...new Set(tags.map(normaliseTag).filter((tag) => tag !== ""))].sort();
}

// entryTags is the only way to read tags off an entry: the field is absent on rows
// written before tags shipped and on anything pulled from an unmigrated server.
export function entryTags(entry: TimeEntry): string[] {
  return entry.tags ?? [];
}

export interface TagUsage {
  name: string;
  count: number;
}

// tagCatalogue is every tag in use, unioned with the seeds, most used first. One pass
// over the entries is cheap even at ten thousand rows, and callers wrap it in $derived.
export function tagCatalogue(): TagUsage[] {
  const counts = new Map<string, number>();
  for (const tag of seedTags) counts.set(tag, 0);
  for (const entry of appState.entries) {
    for (const tag of entryTags(entry)) {
      counts.set(tag, (counts.get(tag) ?? 0) + 1);
    }
  }
  return [...counts.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((left, right) => right.count - left.count || left.name.localeCompare(right.name));
}

// --- derived helpers ---

export function projectByID(projectID: string | null): Project | undefined {
  if (projectID === null) return undefined;
  return appState.projects.find((candidate) => candidate.id === projectID);
}

export function runningEntries(): TimeEntry[] {
  return appState.entries
    .filter((entry) => entry.stopped_at === null)
    .sort((left, right) => right.started_at - left.started_at);
}
