<script lang="ts">
  import "../lib/print/doc-page.js";
  import { appState } from "../lib/state/app.svelte";
  import { route } from "../lib/router.svelte";
  import { localDateISO } from "../lib/format";
  import {
    apportion,
    dateRangeEntries,
    expandTimeOff,
    listDays,
    maxChartDays,
    NO_PROJECT_KEY,
    splitOverlapMinutes,
    summariseReport,
    UNTAGGED_KEY,
    type ReportEntry,
  } from "../lib/report";
  import DailyChart, { type ChartDay } from "../lib/components/DailyChart.svelte";
  import Logo from "../lib/components/Logo.svelte";
  import { t } from "../lib/i18n";
  import { formattingLocale, hourCycle } from "../lib/settings.svelte";

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
    return projects.find((project) => project.id === key)?.name ?? t("No project");
  }

  function projectColor(key: string): string {
    return projects.find((project) => project.id === key)?.color ?? "#c9c4b8";
  }

  const days = $derived(listDays(dateFrom, dateTo));
  const off = $derived(expandTimeOff(appState.timeOff));
  const rangeAllEntries = $derived(dateRangeEntries(appState.entries, dateFrom, dateTo));
  const tagKeysOf = (entry: ReportEntry) => (entry.tags.length > 0 ? entry.tags : [UNTAGGED_KEY]);
  const entries = $derived(
    rangeAllEntries.filter(
      (entry) =>
        (!projectFilter || projectFilter.has(entryKey(entry))) &&
        (!tagFilter || tagKeysOf(entry).some((key) => tagFilter.has(key))),
    ),
  );

  const overlapOnce = $derived(params.get("overlap") === "1");
  // Shares come from the full date range, so a single-project report still
  // counts time shared with other projects' concurrent entries only once.
  const effectiveMinutes = $derived(overlapOnce ? splitOverlapMinutes(rangeAllEntries) : null);
  const minutesOf = (entry: ReportEntry) => effectiveMinutes?.get(entry.id) ?? entry.minutes;

  // The same aggregation the Reports page renders. The untagged bucket sorts last there too:
  // italic, hatched, ordered - three cues that survive greyscale and disabled
  // background graphics.
  const summary = $derived(summariseReport(entries, days, off, minutesOf));
  const totalMinutes = $derived(summary.totalMinutes);
  const workDays = $derived(summary.workDays);
  const avgMinutes = $derived(summary.avgMinutes);
  const minutesByDay = $derived(summary.minutesByDay);
  const peakDay = $derived(summary.peakDay);
  const weekendMinutes = $derived(summary.weekendMinutes);
  const offCounts = $derived(summary.offCounts);
  const byProject = $derived(summary.byProject);
  const byTag = $derived(summary.byTag);

  // The total row invites the reader to add the column up, so the displayed
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

  // Same cap as the Reports page: a printed sheet with 40 000 SVG nodes is neither
  // readable nor printable.
  const chartTooWide = $derived(days.length > maxChartDays);

  const chartDays = $derived.by((): ChartDay[] => {
    if (chartTooWide) return [];
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

  // --- Locale-aware print formatting ---

  const pad2 = (value: number) => String(value).padStart(2, "0");
  // Round once before divmod: overlap shares are fractional, and rounding the
  // remainder independently would render 59.5 as "0h 60m".
  const fmtDuration = (minutes: number) => {
    const total = Math.round(minutes);
    return t("{h}h {m}m", { h: Math.floor(total / 60), m: pad2(total % 60) });
  };
  const fmtHours = (minutes: number) => t("{h} h", { h: (minutes / 60).toFixed(1) });
  const isoDate = (dayISO: string) => new Date(`${dayISO}T12:00:00`);
  const rangeFormatter = $derived.by(
    () => new Intl.DateTimeFormat(formattingLocale(), { year: "numeric", month: "short", day: "numeric" }),
  );
  const monthFormatter = $derived.by(
    () => new Intl.DateTimeFormat(formattingLocale(), { year: "numeric", month: "long" }),
  );
  const shortDayFormatter = $derived.by(
    () => new Intl.DateTimeFormat(formattingLocale(), { month: "numeric", day: "numeric" }),
  );

  function dateRange(fromISO: string, toISO: string): string {
    const from = isoDate(fromISO);
    const to = isoDate(toISO);
    return typeof rangeFormatter.formatRange === "function"
      ? rangeFormatter.formatRange(from, to)
      : `${rangeFormatter.format(from)} – ${rangeFormatter.format(to)}`;
  }

  const titlePeriod = $derived(
    dateFrom.slice(0, 7) === dateTo.slice(0, 7)
      ? monthFormatter.format(isoDate(dateFrom))
      : dateRange(dateFrom, dateTo),
  );
  const generatedOn = $derived(rangeFormatter.format(new Date()));
  const projectsLabel = $derived(projectFilter ? t("{n} projects", { n: projectFilter.size }) : t("all projects"));

  const kindLabel = (kind: string) =>
    kind === "vacation" ? t("Vacation") : kind === "sick" ? t("Sick leave") : t("Day off");

  const absences = $derived.by(() => {
    const overlapping = appState.timeOff
      .filter((timeOff) => timeOff.date_to >= dateFrom && timeOff.date_from <= dateTo)
      .sort((left, right) => left.date_from.localeCompare(right.date_from));
    return overlapping.map((timeOff) => {
      const clippedFrom = timeOff.date_from > dateFrom ? timeOff.date_from : dateFrom;
      const clippedTo = timeOff.date_to < dateTo ? timeOff.date_to : dateTo;
      return `${kindLabel(timeOff.kind)} ${dateRange(clippedFrom, clippedTo)}`;
    });
  });

  const autoPrint = $derived(params.get("print") === "1");

  $effect(() => {
    if (!autoPrint) return;
    const timer = setTimeout(() => window.print(), 500);
    return () => clearTimeout(timer);
  });

  // Grouped once rather than filtered per project row: the detail table renders one
  // row group per project, and a filter inside that loop rescans every entry each time.
  const entriesByProject = $derived.by(() => {
    const grouped = new Map<string, ReportEntry[]>();
    for (const entry of entries) {
      const bucket = grouped.get(entryKey(entry));
      if (bucket) {
        bucket.push(entry);
      } else {
        grouped.set(entryKey(entry), [entry]);
      }
    }
    for (const bucket of grouped.values()) bucket.sort((left, right) => left.startedAt - right.startedAt);
    return grouped;
  });

  function startTime(startedAt: number): string {
    return new Date(startedAt).toLocaleTimeString(formattingLocale(), {
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: hourCycle(),
    });
  }

  const dayShort = (dayISO: string) => shortDayFormatter.format(isoDate(dayISO));
</script>

<div class="print-root">
  <div class="actions">
    <button onclick={() => (window.location.hash = "/reports")}>← {t("Back")}</button>
    <button class="print-button" onclick={() => window.print()}>{t("Print / PDF")}</button>
  </div>

  <doc-page margin="0">
    <div slot="footer" class="foot">WT · {t("Report")} · {dateRange(dateFrom, dateTo)}</div>
    <div class="sheet">
      <h1><Logo size={22} />WT · <span>{t("time report")}</span> · {titlePeriod}</h1>
      <div class="sub">
        {t("Period")}: {dateRange(dateFrom, dateTo)} · {projectsLabel} · {t("generated")} {generatedOn}
      </div>

      <div class="stats">
        <div class="stat"><b>{fmtHours(totalMinutes)}</b><i>{t("total for period")}</i></div>
        <div class="stat hi">
          <b>{workDays.length ? fmtHours(avgMinutes) : "—"}</b>
          <i>{t("average per work day ({n}d)", { n: workDays.length })}</i>
        </div>
        <div class="stat">
          <b>{peakDay ? fmtHours(minutesByDay.get(peakDay) ?? 0) : "—"}</b>
          <i>{t("maximum per day")}{peakDay ? ` · ${dayShort(peakDay)}` : ""}</i>
        </div>
        <div class="stat">
          <b>{fmtHours(weekendMinutes)}</b>
          <i>{t("on weekends")}{totalMinutes ? ` (${Math.round((weekendMinutes / totalMinutes) * 100)}%)` : ""}</i>
        </div>
        <div class="stat">
          <b>{offCounts.vacation + offCounts.sick + offCounts.dayoff} {t("days")}</b>
          <i>{t("time off ({v} vac · {s} sick · {d} dayoff)", { v: offCounts.vacation, s: offCounts.sick, d: offCounts.dayoff })}</i>
        </div>
      </div>

      <div class="figure">
        <h2>{t("Daily hours")}</h2>
        {#if chartTooWide}
          <p class="toowide">{t("The period is too long for a daily chart; the totals below cover it in full.")}</p>
        {:else}
          <DailyChart
            days={chartDays}
            {off}
            {avgMinutes}
            avgCaption={t("avg {h}h / work day", { h: (avgMinutes / 60).toFixed(1) })}
            hourUnit={t("h")}
            interactive={false}
          />
        {/if}
        <div class="legend">
          <span><span class="sw" style="background: #e8a33d"></span>{t("tracked hours")}</span>
          <span><span class="sw" style="background: rgba(96,125,190,0.35)"></span>{t("weekend")}</span>
          <span><span class="sw" style="background: rgba(64,190,196,0.4)"></span>{t("vacation")}</span>
          <span><span class="sw sick-sw"></span>{t("Sick leave")}</span>
          <span><span class="sw" style="background: rgba(181,125,232,0.45)"></span>{t("Day off")}</span>
        </div>
      </div>

      <div class="figure">
        <h2>{t("By project")}</h2>
        <table>
          <thead>
            <tr>
              <th style="width: 8rem">{t("Project")}</th>
              <th aria-label={t("Share")}></th>
              <th class="num" style="width: 5rem">{t("Time")}</th>
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
                <td class="num">{fmtDuration(row.minutes)}</td>
                <td class="num">{row.pct}%</td>
              </tr>
            {:else}
              <tr><td colspan="4" class="empty">{t("No data")}</td></tr>
            {/each}
            {#if byProjectDisplay.length > 0}
              <tr class="sumrow">
                <td>{t("Total")}</td>
                <td></td>
                <td class="num">{fmtDuration(totalMinutes)}</td>
                <td class="num">100%</td>
              </tr>
            {/if}
          </tbody>
        </table>
      </div>

      {#if byTagDisplay.length > 0}
        <div class="figure">
          <h2>{t("By tag")}</h2>
          <table>
            <thead>
              <tr>
                <th style="width: 8rem">{t("Tag")}</th>
                <th aria-label={t("Share")}></th>
                <th class="num" style="width: 5rem">{t("Time")}</th>
                <th class="num" style="width: 2.6rem">%</th>
              </tr>
            </thead>
            <tbody>
              {#each byTagDisplay as row (row.key)}
                <tr>
                  <td class:untagged-label={row.key === UNTAGGED_KEY}>
                    {row.key === UNTAGGED_KEY ? t("untagged") : row.key}
                  </td>
                  <td>
                    <!-- Single ink for every bar: length is the only channel
                         that survives a greyscale office laser. -->
                    <div class="pbar">
                      <div class={row.key === UNTAGGED_KEY ? "hatch" : "ink"} style="width: {row.pct}%"></div>
                    </div>
                  </td>
                  <td class="num">{fmtDuration(row.minutes)}</td>
                  <td class="num">{row.pct}%</td>
                </tr>
              {/each}
              <tr class="sumrow">
                <td>{t("Total")}</td>
                <td></td>
                <td class="num">{fmtDuration(totalMinutes)}</td>
                <td class="num">100%</td>
              </tr>
            </tbody>
          </table>
        </div>
      {/if}

      <h2>{t("Detail")}</h2>
      <table>
        <thead>
          <tr>
            <th>{t("Description")}</th>
            <th style="width: 5.5rem">{t("Date")}</th>
            <th style="width: 4.5rem">{t("Start")}</th>
            <th class="num" style="width: 4.5rem">{t("Time")}</th>
          </tr>
        </thead>
        <tbody>
          {#each byProject as [key, minutes] (key)}
            <tr class="group">
              <td colspan="3"><span class="dot" style="background: {projectColor(key)}"></span>{projectName(key)}</td>
              <td class="num">{fmtDuration(minutes)}</td>
            </tr>
            {#each entriesByProject.get(key) ?? [] as entry (entry.id)}
              <tr class="entry">
                <td>
                  {entry.description || t("(no description)")}
                  {#if entry.tags.length > 0}<span class="tagtrail">{entry.tags.join(" · ")}</span>{/if}
                </td>
                <td class="num">{dayShort(entry.date)}</td>
                <td class="num">{startTime(entry.startedAt)}</td>
                <td class="num">{fmtDuration(minutesOf(entry))}</td>
              </tr>
            {/each}
          {:else}
            <tr><td colspan="4" class="empty">{t("No entries in range")}</td></tr>
          {/each}
        </tbody>
      </table>

      <div class="note">
        {t("Average is total tracked time divided by work days in the period, excluding weekends and time off.")}
        {#if overlapOnce}
          {t("Overlapping entries are counted once; concurrent work splits elapsed time equally.")}
        {/if}
        {#if byTagDisplay.length > 0}
          {t("Entries with multiple tags split their time equally between tags, so tag and project totals match; details show complete entries.")}
        {/if}
        {#if absences.length > 0}
          {t("Time off in period: {items}.", { items: absences.join(", ") })}
        {:else}
          {t("No time off in period.")}
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

  .toowide {
    margin: 1.2rem 0;
    text-align: center;
    font-size: 8.5pt;
    color: #6b6558;
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
