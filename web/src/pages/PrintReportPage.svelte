<script lang="ts">
  import "../lib/print/doc-page.js";
  import { appState } from "../lib/state/app.svelte";
  import { route } from "../lib/router.svelte";
  import { localDateISO } from "../lib/format";
  import {
    apportion,
    expandTimeOff,
    isWeekend,
    listDays,
    NO_PROJECT_KEY,
    splitOverlapMinutes,
    toReportEntries,
    UNTAGGED_KEY,
    type ReportEntry,
  } from "../lib/report";
  import DailyChart, { type ChartDay } from "../lib/components/DailyChart.svelte";
  import Logo from "../lib/components/Logo.svelte";

  // The route carries the report parameters:
  // #/reports/print?from=...&to=...&projects=...&tags=...&print=1
  const params = $derived(new URLSearchParams(route.path.split("?")[1] ?? ""));
  const dateFrom = $derived(params.get("from") ?? localDateISO(Date.now()));
  const dateTo = $derived(params.get("to") ?? localDateISO(Date.now()));
  const projectFilter = $derived.by(() => {
    const raw = params.get("projects");
    return raw ? new Set(raw.split(",")) : null;
  });
  // Same any-of match as projects=, with UNTAGGED_KEY as the literal token
  // "__untagged" - user tags cannot start with an underscore, so no collision.
  const tagFilter = $derived.by(() => {
    const raw = params.get("tags");
    return raw ? new Set(raw.split(",")) : null;
  });

  const projects = $derived([...appState.projects].sort((left, right) => left.name.localeCompare(right.name)));

  const entryKey = (entry: ReportEntry) => entry.projectID ?? NO_PROJECT_KEY;

  function projectName(key: string): string {
    return projects.find((project) => project.id === key)?.name ?? "Без проекта";
  }

  function projectColor(key: string): string {
    return projects.find((project) => project.id === key)?.color ?? "#c9c4b8";
  }

  const days = $derived(listDays(dateFrom, dateTo));
  const off = $derived(expandTimeOff(appState.timeOff));
  const dateRangeEntries = $derived(
    toReportEntries(appState.entries).filter((entry) => entry.date >= dateFrom && entry.date <= dateTo),
  );
  const tagKeysOf = (entry: ReportEntry) => (entry.tags.length > 0 ? entry.tags : [UNTAGGED_KEY]);
  const entries = $derived(
    dateRangeEntries.filter(
      (entry) =>
        (!projectFilter || projectFilter.has(entryKey(entry))) &&
        (!tagFilter || tagKeysOf(entry).some((key) => tagFilter.has(key))),
    ),
  );

  const overlapOnce = $derived(params.get("overlap") === "1");
  // Shares come from the full date range, so a single-project report still
  // counts time shared with other projects' concurrent entries only once.
  const effectiveMinutes = $derived(overlapOnce ? splitOverlapMinutes(dateRangeEntries) : null);
  const minutesOf = (entry: ReportEntry) => effectiveMinutes?.get(entry.id) ?? entry.minutes;

  const totalMinutes = $derived(entries.reduce((sum, entry) => sum + minutesOf(entry), 0));
  const workDays = $derived(days.filter((day) => !isWeekend(day) && !off.has(day)));
  const avgMinutes = $derived(workDays.length ? totalMinutes / workDays.length : 0);
  const minutesByDay = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const entry of entries) totals.set(entry.date, (totals.get(entry.date) ?? 0) + minutesOf(entry));
    return totals;
  });
  const peakDay = $derived.by(() => {
    let best: string | null = null;
    for (const day of days) {
      if ((minutesByDay.get(day) ?? 0) > (best ? minutesByDay.get(best)! : 0)) best = day;
    }
    return best;
  });
  const weekendMinutes = $derived(
    entries.filter((entry) => isWeekend(entry.date)).reduce((sum, entry) => sum + minutesOf(entry), 0),
  );
  const offCounts = $derived.by(() => {
    const counts = { vacation: 0, sick: 0, dayoff: 0 };
    for (const day of days) {
      const kind = off.get(day);
      if (kind) counts[kind]++;
    }
    return counts;
  });

  const byProject = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const entry of entries) totals.set(entryKey(entry), (totals.get(entryKey(entry)) ?? 0) + minutesOf(entry));
    return [...totals.entries()].sort((left, right) => right[1] - left[1]);
  });

  // Split shares: an entry with k tags contributes 1/k of its minutes to each,
  // so По тегам adds up to the same Итого as По проектам. Suppressed entirely
  // when nothing in the range is tagged - historical reports must not grow an
  // empty section. "без тега" is always last: italic, hatched, ordered - three
  // cues that survive greyscale and disabled background graphics.
  const byTag = $derived.by(() => {
    if (!entries.some((entry) => entry.tags.length > 0)) return [];
    const totals = new Map<string, number>();
    for (const entry of entries) {
      const keys = tagKeysOf(entry);
      for (const key of keys) totals.set(key, (totals.get(key) ?? 0) + minutesOf(entry) / keys.length);
    }
    const rows = [...totals.entries()].sort((left, right) => right[1] - left[1]);
    const untaggedIndex = rows.findIndex(([key]) => key === UNTAGGED_KEY);
    if (untaggedIndex >= 0) rows.push(...rows.splice(untaggedIndex, 1));
    return rows;
  });

  // The Итого row invites the reader to add the column up, so the displayed
  // values must actually sum to it: largest-remainder allocation instead of
  // rounding each group independently (which drifts minutes and renders three
  // equal groups as 17%+17%+17%).
  interface DisplayRow {
    key: string;
    minutes: number;
    pct: number;
  }

  function displayRows(rows: [string, number][]): DisplayRow[] {
    const minutes = apportion(
      rows.map(([, value]) => value),
      totalMinutes,
    );
    const pcts = apportion(
      rows.map(([, value]) => value),
      totalMinutes > 0 ? 100 : 0,
    );
    return rows.map(([key], index) => ({ key, minutes: minutes[index]!, pct: pcts[index]! }));
  }

  const byProjectDisplay = $derived(displayRows(byProject));
  const byTagDisplay = $derived(displayRows(byTag));

  const chartDays = $derived.by((): ChartDay[] => {
    const keys = byProject.map(([key]) => key);
    const perDay = new Map<string, Map<string, number>>();
    for (const entry of entries) {
      const bucket = perDay.get(entry.date) ?? new Map<string, number>();
      bucket.set(entryKey(entry), (bucket.get(entryKey(entry)) ?? 0) + minutesOf(entry));
      perDay.set(entry.date, bucket);
    }
    return days.map((day) => ({
      date: day,
      slices: keys.map((key) => ({
        key,
        name: projectName(key),
        color: projectColor(key),
        minutes: perDay.get(day)?.get(key) ?? 0,
      })),
    }));
  });

  // --- Russian formatting ---

  const MONTHS_GEN = [
    "января",
    "февраля",
    "марта",
    "апреля",
    "мая",
    "июня",
    "июля",
    "августа",
    "сентября",
    "октября",
    "ноября",
    "декабря",
  ];
  const MONTHS_NOM = [
    "январь",
    "февраль",
    "март",
    "апрель",
    "май",
    "июнь",
    "июль",
    "август",
    "сентябрь",
    "октябрь",
    "ноябрь",
    "декабрь",
  ];

  const pad2 = (value: number) => String(value).padStart(2, "0");
  // Round once before divmod: overlap shares are fractional, and rounding the
  // remainder independently would render 59.5 as "0ч 60м".
  const fmtRu = (minutes: number) => {
    const total = Math.round(minutes);
    return `${Math.floor(total / 60)}ч ${pad2(total % 60)}м`;
  };
  const fmtHoursRu = (minutes: number) => (minutes / 60).toFixed(1) + " ч";
  const dayShort = (dayISO: string) => `${Number(dayISO.slice(8))}.${dayISO.slice(5, 7)}`;
  const monthIndex = (dayISO: string) => Number(dayISO.slice(5, 7)) - 1;

  function ruRange(fromISO: string, toISO: string): string {
    const fromDay = Number(fromISO.slice(8));
    const toDay = Number(toISO.slice(8));
    if (fromISO.slice(0, 7) === toISO.slice(0, 7)) {
      const range = fromDay === toDay ? String(fromDay) : `${fromDay}–${toDay}`;
      return `${range} ${MONTHS_GEN[monthIndex(fromISO)]}`;
    }
    return `${fromDay} ${MONTHS_GEN[monthIndex(fromISO)]} – ${toDay} ${MONTHS_GEN[monthIndex(toISO)]}`;
  }

  const titlePeriod = $derived(
    dateFrom.slice(0, 7) === dateTo.slice(0, 7)
      ? `${MONTHS_NOM[monthIndex(dateFrom)]} ${dateFrom.slice(0, 4)}`
      : `${ruRange(dateFrom, dateTo)} ${dateTo.slice(0, 4)}`,
  );
  const generatedOn = $derived.by(() => {
    const today = new Date();
    return `${pad2(today.getDate())}.${pad2(today.getMonth() + 1)}.${today.getFullYear()}`;
  });
  const projectsLabel = $derived(projectFilter ? `проектов: ${projectFilter.size}` : "все проекты");

  const KIND_RU: Record<string, string> = { vacation: "отпуск", sick: "больничный", dayoff: "дей-офф" };

  const absences = $derived.by(() => {
    const overlapping = appState.timeOff
      .filter((timeOff) => timeOff.date_to >= dateFrom && timeOff.date_from <= dateTo)
      .sort((left, right) => left.date_from.localeCompare(right.date_from));
    return overlapping.map((timeOff) => {
      const clippedFrom = timeOff.date_from > dateFrom ? timeOff.date_from : dateFrom;
      const clippedTo = timeOff.date_to < dateTo ? timeOff.date_to : dateTo;
      return `${KIND_RU[timeOff.kind]} ${ruRange(clippedFrom, clippedTo)}`;
    });
  });

  const autoPrint = $derived(params.get("print") === "1");

  $effect(() => {
    if (!autoPrint) return;
    const timer = setTimeout(() => window.print(), 500);
    return () => clearTimeout(timer);
  });

  function detailEntries(key: string): ReportEntry[] {
    return entries.filter((entry) => entryKey(entry) === key).sort((left, right) => left.startedAt - right.startedAt);
  }

  function startTime(startedAt: number): string {
    const started = new Date(startedAt);
    return `${started.getHours()}:${pad2(started.getMinutes())}`;
  }
</script>

<div class="print-root">
  <div class="actions">
    <button onclick={() => (window.location.hash = "/reports")}>← Назад</button>
    <button class="print-button" onclick={() => window.print()}>Печать / PDF</button>
  </div>

  <doc-page margin="0">
    <div slot="footer" class="foot">WT · отчёт · {ruRange(dateFrom, dateTo)} {dateTo.slice(0, 4)}</div>
    <div class="sheet">
      <h1><Logo size={22} />WT · <span>отчёт по времени</span> · {titlePeriod}</h1>
      <div class="sub">
        Период: {ruRange(dateFrom, dateTo)} {dateTo.slice(0, 4)} · {projectsLabel} · сформировано {generatedOn}
      </div>

      <div class="stats">
        <div class="stat"><b>{fmtHoursRu(totalMinutes)}</b><i>всего за период</i></div>
        <div class="stat hi">
          <b>{workDays.length ? fmtHoursRu(avgMinutes) : "—"}</b>
          <i>в среднем на рабочий день ({workDays.length} дн.)</i>
        </div>
        <div class="stat">
          <b>{peakDay ? fmtHoursRu(minutesByDay.get(peakDay) ?? 0) : "—"}</b>
          <i>максимум за день{peakDay ? ` · ${dayShort(peakDay)}` : ""}</i>
        </div>
        <div class="stat">
          <b>{fmtHoursRu(weekendMinutes)}</b>
          <i>в выходные{totalMinutes ? ` (${Math.round((weekendMinutes / totalMinutes) * 100)}%)` : ""}</i>
        </div>
        <div class="stat">
          <b>{offCounts.vacation + offCounts.sick + offCounts.dayoff} дн.</b>
          <i>отсутствия: {offCounts.vacation} отпуск · {offCounts.sick} больничный · {offCounts.dayoff} дей-офф</i>
        </div>
      </div>

      <div class="figure">
        <h2>Часы по дням</h2>
        <DailyChart
          days={chartDays}
          {off}
          {avgMinutes}
          avgCaption="среднее {(avgMinutes / 60).toFixed(1)}ч / раб. день"
          hourUnit="ч"
          interactive={false}
        />
        <div class="legend">
          <span><span class="sw" style="background: #e8a33d"></span>часы за день</span>
          <span><span class="sw" style="background: rgba(96,125,190,0.35)"></span>выходной</span>
          <span><span class="sw" style="background: rgba(64,190,196,0.4)"></span>отпуск</span>
          <span><span class="sw sick-sw"></span>больничный</span>
          <span><span class="sw" style="background: rgba(181,125,232,0.45)"></span>дей-офф</span>
        </div>
      </div>

      <div class="figure">
        <h2>По проектам</h2>
        <table>
          <thead>
            <tr>
              <th style="width: 8rem">Проект</th>
              <th aria-label="Доля"></th>
              <th class="num" style="width: 5rem">Время</th>
              <th class="num" style="width: 2.6rem">%</th>
            </tr>
          </thead>
          <tbody>
            {#each byProjectDisplay as row (row.key)}
              <tr>
                <td><span class="dot" style="background: {projectColor(row.key)}"></span>{projectName(row.key)}</td>
                <td>
                  <div class="pbar">
                    <div style="width: {row.pct}%; background: {projectColor(row.key)}"></div>
                  </div>
                </td>
                <td class="num">{fmtRu(row.minutes)}</td>
                <td class="num">{row.pct}%</td>
              </tr>
            {:else}
              <tr><td colspan="4" class="empty">Нет данных за период</td></tr>
            {/each}
            {#if byProjectDisplay.length > 0}
              <tr class="sumrow">
                <td>Итого</td>
                <td></td>
                <td class="num">{fmtRu(totalMinutes)}</td>
                <td class="num">100%</td>
              </tr>
            {/if}
          </tbody>
        </table>
      </div>

      {#if byTagDisplay.length > 0}
        <div class="figure">
          <h2>По тегам</h2>
          <table>
            <thead>
              <tr>
                <th style="width: 8rem">Тег</th>
                <th aria-label="Доля"></th>
                <th class="num" style="width: 5rem">Время</th>
                <th class="num" style="width: 2.6rem">%</th>
              </tr>
            </thead>
            <tbody>
              {#each byTagDisplay as row (row.key)}
                <tr>
                  <td class:untagged-label={row.key === UNTAGGED_KEY}>
                    {row.key === UNTAGGED_KEY ? "без тега" : row.key}
                  </td>
                  <td>
                    <!-- Single ink for every bar: length is the only channel
                         that survives a greyscale office laser. -->
                    <div class="pbar">
                      <div class={row.key === UNTAGGED_KEY ? "hatch" : "ink"} style="width: {row.pct}%"></div>
                    </div>
                  </td>
                  <td class="num">{fmtRu(row.minutes)}</td>
                  <td class="num">{row.pct}%</td>
                </tr>
              {/each}
              <tr class="sumrow">
                <td>Итого</td>
                <td></td>
                <td class="num">{fmtRu(totalMinutes)}</td>
                <td class="num">100%</td>
              </tr>
            </tbody>
          </table>
        </div>
      {/if}

      <h2>Детализация</h2>
      <table>
        <thead>
          <tr>
            <th>Описание</th>
            <th style="width: 5.5rem">Дата</th>
            <th style="width: 4.5rem">Начало</th>
            <th class="num" style="width: 4.5rem">Время</th>
          </tr>
        </thead>
        <tbody>
          {#each byProject as [key, minutes] (key)}
            <tr class="group">
              <td colspan="3"><span class="dot" style="background: {projectColor(key)}"></span>{projectName(key)}</td>
              <td class="num">{fmtRu(minutes)}</td>
            </tr>
            {#each detailEntries(key) as entry (entry.id)}
              <tr class="entry">
                <td>
                  {entry.description || "(без описания)"}
                  {#if entry.tags.length > 0}<span class="tagtrail">{entry.tags.join(" · ")}</span>{/if}
                </td>
                <td class="num">{dayShort(entry.date)}</td>
                <td class="num">{startTime(entry.startedAt)}</td>
                <td class="num">{fmtRu(minutesOf(entry))}</td>
              </tr>
            {/each}
          {:else}
            <tr><td colspan="4" class="empty">Нет записей за период</td></tr>
          {/each}
        </tbody>
      </table>

      <div class="note">
        Среднее считается по всем затреканным часам, делённым на количество рабочих дней в периоде (без выходных,
        отпуска, дей-оффов и больничных).
        {#if overlapOnce}
          Пересекающиеся записи учтены один раз: одновременная работа делит затраченное время поровну.
        {/if}
        {#if byTagDisplay.length > 0}
          Запись с несколькими тегами делит своё время между ними поровну, поэтому суммы по тегам и по проектам
          совпадают с общим итогом; в детализации записи показаны целиком.
        {/if}
        {#if absences.length > 0}
          Отсутствия в периоде: {absences.join(", ")}.
        {:else}
          Отсутствий в периоде нет.
        {/if}
      </div>
    </div>
  </doc-page>
</div>

<style>
  /* The printable sheet is always light, independent of the system theme. */
  .print-root {
    --text: #232936;
    --text-dim: #6b7386;
    --border: #e3e0d8;
    --accent: #b97a1e;
    --accent-text: #ffffff;
    --surface: #ffffff;
    --grid: rgba(20, 26, 40, 0.09);
    --axis: rgba(20, 26, 40, 0.4);
    --hover: rgba(20, 26, 40, 0.04);
    color: var(--text);
    font-size: 10pt;
  }

  .actions {
    position: fixed;
    top: 10px;
    right: 12px;
    z-index: 20;
    display: flex;
    gap: 0.5rem;
  }

  .actions button {
    background: #ffffff;
    color: #232936;
    border-color: #c9c4b8;
  }

  .actions .print-button {
    background: #cd8a26;
    border-color: #cd8a26;
    color: #ffffff;
    font-weight: 600;
  }

  @media print {
    .actions {
      display: none;
    }
  }

  doc-page:not(:defined) {
    visibility: hidden;
  }

  .sheet {
    background: #ffffff;
    padding: 0.55in 0.6in;
    min-height: 100%;
  }

  h1 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 3px;
    font-size: 15pt;
    letter-spacing: 0.2px;
  }

  h1 span {
    color: #cd8a26;
  }

  .sub {
    color: var(--text-dim);
    font-size: 8.5pt;
  }

  .stats {
    display: flex;
    gap: 22px;
    flex-wrap: wrap;
    margin: 14px 0 4px;
    font-family: var(--mono);
    break-inside: avoid;
  }

  .stat b {
    display: block;
    font-size: 13pt;
    font-weight: 600;
  }

  .stat.hi b {
    color: #b97a1e;
  }

  .stat i {
    font-style: normal;
    font-size: 7.5pt;
    color: var(--text-dim);
  }

  h2 {
    font-size: 9pt;
    margin: 16px 0 6px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-dim);
  }

  .figure {
    break-inside: avoid;
  }

  .legend {
    display: flex;
    gap: 14px;
    flex-wrap: wrap;
    margin-top: 5px;
    font-size: 7.5pt;
    color: var(--text-dim);
    align-items: center;
  }

  .sw {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 2.5px;
    margin-right: 4px;
    vertical-align: -1.5px;
  }

  .sick-sw {
    background: rgba(224, 82, 82, 0.35);
    border: 1px solid #e05252;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 8.5pt;
  }

  th {
    text-align: left;
    font-size: 6.8pt;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-dim);
    padding: 3px 5px;
    border-bottom: 1.2px solid #232936;
  }

  td {
    padding: 3.5px 5px;
    border-bottom: 1px solid var(--border);
  }

  td.num,
  th.num {
    text-align: right;
    font-family: var(--mono);
    font-variant-numeric: tabular-nums;
  }

  tr.group td {
    font-weight: 600;
    background: rgba(20, 26, 40, 0.04);
  }

  tr.entry td:first-child {
    padding-left: 16px;
    color: var(--text-dim);
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    display: inline-block;
    margin-right: 5px;
  }

  .pbar {
    height: 4px;
    border-radius: 2px;
    background: #e9e6df;
    overflow: hidden;
    width: 100%;
  }

  .pbar div {
    height: 100%;
    border-radius: 2px;
  }

  /* All tag bars share one ink, so the only variable is length - the channel
     that survives a greyscale laser. */
  .pbar div.ink {
    background: rgba(20, 26, 40, 0.55);
  }

  /* Redundant reinforcement for "no tag": hatch plus an italic label plus last
     position. If background graphics are off in the print dialog, the label
     and the ordering still carry it. */
  .pbar div.hatch {
    background: repeating-linear-gradient(
      45deg,
      rgba(20, 26, 40, 0.55) 0 2px,
      rgba(20, 26, 40, 0.12) 2px 4px
    );
  }

  td.untagged-label {
    font-style: italic;
  }

  tr.sumrow td {
    font-weight: 600;
    border-top: 1.2px solid #232936;
    border-bottom: none;
    background: transparent;
  }

  .tagtrail {
    color: var(--text-dim);
    font-size: 7.5pt;
  }

  .tagtrail::before {
    content: " · ";
  }

  .note {
    color: var(--text-dim);
    font-size: 7.5pt;
    line-height: 1.5;
    margin-top: 14px;
  }

  .empty {
    color: var(--text-dim);
  }

  .foot {
    font-size: 7.5pt;
    color: #6b7386;
    text-align: center;
    background: #ffffff;
    padding: 4px 0;
  }
</style>
