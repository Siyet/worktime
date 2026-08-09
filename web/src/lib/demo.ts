// Demo mode: a backend-less build for GitHub Pages. All data lives only in
// the browser's IndexedDB; the sync engine and auth are never started.
import { getAllRows, mergeServerRows } from "./db";
import { localDateISO } from "./format";
import type { Project, TimeEntry, TimeOff } from "./types";
import { uuidv7 } from "./uuid";

export const DEMO = import.meta.env.VITE_DEMO === "1";

// Seeds a small believable dataset on first launch so the demo is not empty:
// three projects, ~3 weeks of entries, every time-off kind and a running timer.
export async function seedDemoDataIfEmpty(): Promise<void> {
  const existing = await getAllRows<TimeEntry>("time_entries");
  if (existing.length > 0) return;

  const now = Date.now();
  const day = 86_400_000;
  const isoAt = (offsetDays: number) => localDateISO(now + offsetDays * day);

  const projects: Project[] = [
    { id: uuidv7(), name: "Backend", color: "#e8a33d", archived: false, created_at: now, updated_at: now, deleted_at: null },
    { id: uuidv7(), name: "Frontend", color: "#607dbe", archived: false, created_at: now, updated_at: now, deleted_at: null },
    { id: uuidv7(), name: "Research", color: "#40bec4", archived: false, created_at: now, updated_at: now, deleted_at: null },
  ];
  const descriptions: Record<string, string[]> = {
    [projects[0]!.id]: ["API pagination", "Sync engine tuning", "Code review", "Fix flaky migration test"],
    [projects[1]!.id]: ["Reports chart polish", "PWA install flow", "Design system pass"],
    [projects[2]!.id]: ["Benchmark SQLite batching", "Read MCP spec"],
  };

  let randomState = 7;
  const random = () => (randomState = (randomState * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;

  const entries: TimeEntry[] = [];
  for (let offset = -24; offset <= 0; offset++) {
    const date = new Date(now + offset * day);
    const weekday = date.getDay();
    const isWeekend = weekday === 0 || weekday === 6;
    if (isWeekend && random() < 0.75) continue;
    const count = isWeekend ? 1 : 2 + Math.floor(random() * 3);
    date.setHours(9, 10 + Math.floor(random() * 40), 0, 0);
    let startedAt = date.getTime();
    for (let index = 0; index < count; index++) {
      const roll = random();
      const project = roll < 0.5 ? projects[0]! : roll < 0.82 ? projects[1]! : projects[2]!;
      const options = descriptions[project.id]!;
      const minutes = 35 + Math.floor(random() * 130);
      entries.push({
        id: uuidv7(),
        project_id: project.id,
        description: options[Math.floor(random() * options.length)]!,
        started_at: startedAt,
        stopped_at: startedAt + minutes * 60_000,
        created_at: startedAt,
        updated_at: startedAt,
        deleted_at: null,
      });
      startedAt += (minutes + 15 + Math.floor(random() * 40)) * 60_000;
    }
  }
  // The same task picked up three times yesterday: the demo has to show what
  // the grouped row looks like, not only single entries.
  const yesterdayNine = new Date(now - day);
  yesterdayNine.setHours(9, 0, 0, 0);
  for (const [offsetMinutes, minutes] of [
    [0, 55],
    [130, 40],
    [300, 70],
  ]) {
    const startedAt = yesterdayNine.getTime() + offsetMinutes! * 60_000;
    entries.push({
      id: uuidv7(),
      project_id: projects[0]!.id,
      description: "Code review",
      started_at: startedAt,
      stopped_at: startedAt + minutes! * 60_000,
      created_at: startedAt,
      updated_at: startedAt,
      deleted_at: null,
    });
  }
  entries.push({
    id: uuidv7(),
    project_id: projects[0]!.id,
    description: "Sync engine tuning",
    started_at: now - 23 * 60_000,
    stopped_at: null,
    created_at: now - 23 * 60_000,
    updated_at: now - 23 * 60_000,
    deleted_at: null,
  });

  const timeOff: TimeOff[] = [
    { id: uuidv7(), kind: "vacation", date_from: isoAt(-17), date_to: isoAt(-15), note: "", created_at: now, updated_at: now, deleted_at: null },
    { id: uuidv7(), kind: "sick", date_from: isoAt(-9), date_to: isoAt(-8), note: "", created_at: now, updated_at: now, deleted_at: null },
    { id: uuidv7(), kind: "dayoff", date_from: isoAt(-3), date_to: isoAt(-3), note: "", created_at: now, updated_at: now, deleted_at: null },
  ];

  // One bulk transaction (no dirty markers): atomic, so an interrupted first
  // load can never leave a half-seeded demo behind.
  await mergeServerRows({ projects, time_entries: entries, time_off: timeOff });
}
