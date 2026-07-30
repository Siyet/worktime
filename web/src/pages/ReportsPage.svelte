<script lang="ts">
  import { appState, projectByID } from "../lib/state/app.svelte";
  import { formatDurationShort, formatTime, localDateISO } from "../lib/format";
  import {
    buildCSV,
    expandTimeOff,
    formatDayISO,
    isWeekend,
    listDays,
    roundMinutes,
    splitOverlapMinutes,
    toReportEntries,
    type ReportEntry,
  } from "../lib/report";
  import { t } from "../lib/i18n";
  import DailyChart, { type ChartDay } from "../lib/components/DailyChart.svelte";
  import Seg from "../lib/components/Seg.svelte";

  type Preset = "week" | "lastweek" | "month" | "30" | "";
  type GroupBy = "project" | "day" | "description";

  const NO_PROJECT_KEY = "none";

  function presetRange(preset: Exclude<Preset, "">): { from: string; to: string } {
    const today = new Date();
    const monday = (date: Date) => {
      const copy = new Date(date);
      copy.setDate(copy.getDate() - ((copy.getDay() + 6) % 7));
      return copy;
    };
    if (preset === "week") {
      return { from: localDateISO(monday(today).getTime()), to: localDateISO(today.getTime()) };
    }
    if (preset === "lastweek") {
      const start = monday(today);
      start.setDate(start.getDate() - 7);
      const end = new Date(start);
      end.setDate(end.getDate() + 6);
      return { from: localDateISO(start.getTime()), to: localDateISO(end.getTime()) };
    }
    if (preset === "month") {
      return {
        from: localDateISO(new Date(today.getFullYear(), today.getMonth(), 1).getTime()),
        to: localDateISO(today.getTime()),
      };
    }
    return { from: localDateISO(today.getTime() - 29 * 86_400_000), to: localDateISO(today.getTime()) };
  }

  let preset = $state<Preset>("month");
  let dateFrom = $state(presetRange("month").from);
  let dateTo = $state(presetRange("month").to);
  let dayFilter = $state<string | null>(null);
  // Projects toggled OFF by their chip (default: everything active).
  let disabledKeys = $state<Set<string>>(new Set());
  let groupBy = $state<GroupBy>("project");
  let showEntries = $state(false);
  let rounding = $state(0);
  let columns = $state({ duration: true, pct: true, entries: true, avg: false });
  // When on, wall-clock time is split equally between concurrent entries, so
  // an hour spent on two overlapping tasks counts as one hour in every total.
  let overlapOnce = $state(false);

  function applyPreset(value: string) {
    const range = presetRange(value as Exclude<Preset, "">);
    dateFrom = range.from;
    dateTo = range.to;
    dayFilter = null;
  }

  function onManualDate() {
    preset = "";
    dayFilter = null;
  }

  // --- filter chips ---

  const chips = $derived.by(() => {
    const projects = [...appState.projects].sort((left, right) => left.name.localeCompare(right.name));
    return [
      ...projects.map((project) => ({ key: project.id, name: project.name, color: project.color })),
      { key: NO_PROJECT_KEY, name: t("No project"), color: "var(--border)" },
    ];
  });
  const activeKeys = $derived(new Set(chips.map((chip) => chip.key).filter((key) => !disabledKeys.has(key))));

  function toggleChip(key: string) {
    const next = new Set(disabledKeys);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
      // Keep at least one project active.
      if (chips.every((chip) => next.has(chip.key))) return;
    }
    disabledKeys = next;
  }

  // --- derived report data ---

  const off = $derived(expandTimeOff(appState.timeOff));
  const days = $derived(listDays(dateFrom, dateTo));

  const entryKey = (entry: ReportEntry) => entry.projectID ?? NO_PROJECT_KEY;

  const dateRangeEntries = $derived(
    toReportEntries(appState.entries).filter((entry) => entry.date >= dateFrom && entry.date <= dateTo),
  );
  const rangeEntries = $derived(dateRangeEntries.filter((entry) => activeKeys.has(entryKey(entry))));
  const filteredEntries = $derived(rangeEntries.filter((entry) => !dayFilter || entry.date === dayFilter));

  // Per-entry minutes to aggregate: raw durations, or overlap-adjusted shares.
  // Shares are computed on the full date range BEFORE the project-chip filter,
  // so hiding one project never rewrites another project's numbers.
  const effectiveMinutes = $derived(overlapOnce ? splitOverlapMinutes(dateRangeEntries) : null);
  const minutesOf = (entry: ReportEntry) => effectiveMinutes?.get(entry.id) ?? entry.minutes;

  const totalMinutes = $derived(rangeEntries.reduce((sum, entry) => sum + minutesOf(entry), 0));
  const workDays = $derived(days.filter((day) => !isWeekend(day) && !off.has(day)));
  const minutesByDay = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const entry of rangeEntries) totals.set(entry.date, (totals.get(entry.date) ?? 0) + minutesOf(entry));
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
    rangeEntries.filter((entry) => isWeekend(entry.date)).reduce((sum, entry) => sum + minutesOf(entry), 0),
  );
  const offCounts = $derived.by(() => {
    const counts = { vacation: 0, sick: 0, dayoff: 0 };
    for (const day of days) {
      const kind = off.get(day);
      if (kind) counts[kind]++;
    }
    return counts;
  });
  const avgMinutes = $derived(workDays.length ? totalMinutes / workDays.length : 0);

  const fmtHours = (minutes: number) => (minutes / 60).toFixed(1) + "h";
  const fmtMin = (minutes: number) => formatDurationShort(minutes * 60000);

  function chipName(key: string): string {
    return chips.find((chip) => chip.key === key)?.name ?? "No project";
  }

  function chipColor(key: string): string {
    return chips.find((chip) => chip.key === key)?.color ?? "var(--border)";
  }

  const chartDays = $derived.by((): ChartDay[] => {
    const perDay = new Map<string, Map<string, number>>();
    for (const entry of rangeEntries) {
      const bucket = perDay.get(entry.date) ?? new Map<string, number>();
      bucket.set(entryKey(entry), (bucket.get(entryKey(entry)) ?? 0) + minutesOf(entry));
      perDay.set(entry.date, bucket);
    }
    return days.map((day) => ({
      date: day,
      slices: chips
        .filter((chip) => activeKeys.has(chip.key))
        .map((chip) => ({
          key: chip.key,
          name: chip.name,
          color: chip.color,
          minutes: perDay.get(day)?.get(chip.key) ?? 0,
        })),
    }));
  });

  const byProject = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const entry of rangeEntries) {
      totals.set(entryKey(entry), (totals.get(entryKey(entry)) ?? 0) + minutesOf(entry));
    }
    return [...totals.entries()].sort((left, right) => right[1] - left[1]);
  });

  // --- report table ---

  const visibleColumns = $derived(
    (["duration", "pct", "entries", "avg"] as const).filter((column) => columns[column]),
  );

  function columnHead(column: string): string {
    if (column === "duration") return t("Duration");
    if (column === "pct") return "%";
    if (column === "entries") return t("Entries");
    return t("Avg / entry");
  }

  const tableGroups = $derived.by(() => {
    const key = (entry: ReportEntry) =>
      groupBy === "project"
        ? entryKey(entry)
        : groupBy === "day"
          ? entry.date
          : entry.description || t("(no description)");
    const groups = new Map<string, ReportEntry[]>();
    for (const entry of filteredEntries) {
      const bucket = groups.get(key(entry)) ?? [];
      bucket.push(entry);
      groups.set(key(entry), bucket);
    }
    const rows = [...groups.entries()];
    if (groupBy === "day") {
      rows.sort((left, right) => left[0].localeCompare(right[0]));
    } else {
      const total = (entries: ReportEntry[]) => entries.reduce((sum, entry) => sum + minutesOf(entry), 0);
      rows.sort((left, right) => total(right[1]) - total(left[1]));
    }
    return rows.map(([groupKey, entries]) => ({
      key: groupKey,
      label: groupBy === "day" ? formatDayISO(groupKey) : groupBy === "project" ? chipName(groupKey) : groupKey,
      color: groupBy === "project" ? chipColor(groupKey) : null,
      // With overlaps-once on, rounding applies to the group sum: per-share
      // rounding would clamp each fraction up to a full step and re-inflate
      // the very overlap the option removes.
      minutes: overlapOnce
        ? roundMinutes(
            entries.reduce((sum, entry) => sum + minutesOf(entry), 0),
            rounding,
          )
        : entries.reduce((sum, entry) => sum + roundMinutes(entry.minutes, rounding), 0),
      entries: [...entries].sort((left, right) => left.startedAt - right.startedAt),
    }));
  });

  // The header total is the sum of the visible groups, so table and header can
  // never disagree regardless of rounding mode.
  const tableTotal = $derived(tableGroups.reduce((sum, group) => sum + group.minutes, 0));

  function cellValue(group: { minutes: number; entries: ReportEntry[] }, column: string): string {
    if (column === "duration") return fmtMin(group.minutes);
    if (column === "pct") return (tableTotal ? Math.round((group.minutes / tableTotal) * 100) : 0) + "%";
    if (column === "entries") return String(group.entries.length);
    return fmtMin(Math.round(group.minutes / Math.max(1, group.entries.length)));
  }

  // --- actions ---

  function exportCSV() {
    // With overlaps-once on, per-row rounding would clamp fractional shares
    // back up to a full step, so the CSV exports the raw shares instead.
    const csv = buildCSV(
      filteredEntries,
      (projectID) => chipName(projectID ?? NO_PROJECT_KEY),
      overlapOnce ? 0 : rounding,
      minutesOf,
    );
    const blobURL = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
    const anchor = document.createElement("a");
    anchor.href = blobURL;
    anchor.download = "worktime-report.csv";
    anchor.click();
    URL.revokeObjectURL(blobURL);
  }

  function openPrint() {
    const params = new URLSearchParams({ from: dateFrom, to: dateTo });
    if (disabledKeys.size > 0) {
      params.set("projects", [...activeKeys].join(","));
    }
    if (overlapOnce) {
      params.set("overlap", "1");
    }
    window.location.hash = "/reports/print?" + params.toString();
  }
</script>

<div class="card toolbar">
  <Seg
    options={[
      { value: "week", label: t("Week") },
      { value: "lastweek", label: t("Last week") },
      { value: "month", label: t("Month") },
      { value: "30", label: t("30 days") },
    ]}
    bind:value={preset}
    onselect={applyPreset}
  />
  <input type="date" bind:value={dateFrom} onchange={onManualDate} aria-label={t("From")} />
  <span class="muted">–</span>
  <input type="date" bind:value={dateTo} onchange={onManualDate} aria-label={t("To")} />
  <span class="chips">
    {#each chips as chip (chip.key)}
      <button type="button" class="chip" class:off={disabledKeys.has(chip.key)} onclick={() => toggleChip(chip.key)}>
        <span class="dot" style="background: {chip.color}"></span>{chip.name}
      </button>
    {/each}
  </span>
  {#if dayFilter}
    <button type="button" class="filterpill" onclick={() => (dayFilter = null)}>
      {formatDayISO(dayFilter)}&nbsp;&nbsp;✕
    </button>
  {/if}
  <label
    class="overlap-toggle"
    title={t("Overlapping entries are counted once: simultaneous work shares the elapsed time.")}
  >
    <input type="checkbox" bind:checked={overlapOnce} />
    {t("Overlaps once")}
  </label>
  <span class="spacer"></span>
  <button onclick={exportCSV}>{t("Export CSV")}</button>
  <button class="primary" onclick={openPrint}>{t("PDF report")}</button>
</div>

<div class="card stats">
  <div class="stat"><b>{fmtHours(totalMinutes)}</b><i>{t("total tracked")}</i></div>
  <div class="stat hi">
    <b>{workDays.length ? fmtHours(avgMinutes) : "—"}</b>
    <i>{t("avg per work day ({n}d)", { n: workDays.length })}</i>
  </div>
  <div class="stat">
    <b>{peakDay ? fmtHours(minutesByDay.get(peakDay) ?? 0) : "—"}</b>
    <i>{t("peak day")}{peakDay ? ` · ${formatDayISO(peakDay)}` : ""}</i>
  </div>
  <div class="stat">
    <b>{fmtHours(weekendMinutes)}{totalMinutes ? ` (${Math.round((weekendMinutes / totalMinutes) * 100)}%)` : ""}</b>
    <i>{t("on weekends")}</i>
  </div>
  <div class="stat">
    <b>{offCounts.vacation + offCounts.sick + offCounts.dayoff}d</b>
    <i>
      {t("time off ({v} vac · {s} sick · {d} dayoff)", {
        v: offCounts.vacation,
        s: offCounts.sick,
        d: offCounts.dayoff,
      })}
    </i>
  </div>
</div>

<div class="grid2">
  <div class="card">
    <div class="row">
      <h3>{t("Daily hours")}</h3>
      <span class="spacer"></span>
      <span class="muted hint">{t("click a day to filter the table")}</span>
    </div>
    <DailyChart
      days={chartDays}
      {off}
      {avgMinutes}
      avgCaption={t("avg {h}h / work day", { h: (avgMinutes / 60).toFixed(1) })}
      selected={dayFilter}
      onselect={(day) => (dayFilter = day)}
    />
    <div class="legend">
      <span><span class="sw" style="background: var(--accent)"></span>{t("tracked hours")}</span>
      <span><span class="sw" style="background: rgba(96,125,190,0.3)"></span>{t("weekend")}</span>
      <span><span class="sw" style="background: rgba(64,190,196,0.35)"></span>{t("vacation")}</span>
      <span><span class="sw sick-sw"></span>{t("sick leave")}</span>
      <span><span class="sw" style="background: rgba(181,125,232,0.4)"></span>{t("day off")}</span>
    </div>
  </div>
  <div class="card">
    <h3>{t("By project")}</h3>
    {#each byProject as [key, minutes] (key)}
      <div class="proj-item">
        <div class="row">
          <span class="dot" style="background: {chipColor(key)}"></span>
          <span>{chipName(key)}</span>
          <span class="spacer"></span>
          <span class="mono">{fmtMin(minutes)}</span>
          <span class="muted mono pct">{totalMinutes ? Math.round((minutes / totalMinutes) * 100) : 0}%</span>
        </div>
        <div class="pbar">
          <div style="width: {totalMinutes ? (minutes / totalMinutes) * 100 : 0}%; background: {chipColor(key)}"></div>
        </div>
      </div>
    {:else}
      <p class="muted">{t("No data")}</p>
    {/each}
    <div class="muted hint" style="margin-top: 0.6rem">{t("toggle projects with the chips above")}</div>
  </div>
</div>

<div class="card">
  <h3>{t("Custom report")}</h3>
  <div class="builder">
    <div>
      <h4>{t("Group by")}</h4>
      <Seg
        vertical
        options={[
          { value: "project", label: t("Project") },
          { value: "day", label: t("Day") },
          { value: "description", label: t("Description") },
        ]}
        bind:value={groupBy}
      />
    </div>
    <div>
      <h4>{t("Columns")}</h4>
      <label><input type="checkbox" bind:checked={columns.duration} /> {t("Duration")}</label>
      <label><input type="checkbox" bind:checked={columns.pct} /> {t("% of total")}</label>
      <label><input type="checkbox" bind:checked={columns.entries} /> {t("Entries")}</label>
      <label><input type="checkbox" bind:checked={columns.avg} /> {t("Avg / entry")}</label>
    </div>
    <div>
      <h4>{t("Detail")}</h4>
      <label><input type="checkbox" bind:checked={showEntries} /> {t("Show individual entries")}</label>
      <h4 style="margin-top: 0.7rem">{t("Rounding")}</h4>
      <Seg
        options={[
          { value: "0", label: t("Off") },
          { value: "15", label: "15m" },
          { value: "30", label: "30m" },
          { value: "60", label: "1h" },
        ]}
        value={String(rounding)}
        onselect={(value) => (rounding = Number(value))}
      />
    </div>
  </div>
</div>

<div class="card">
  <div class="row">
    <h3>{t("Report")} — {t("by " + groupBy)}{dayFilter ? ` · ${formatDayISO(dayFilter)}` : ""}</h3>
    <span class="spacer"></span>
    <span class="muted mono">{fmtMin(tableTotal)}</span>
  </div>
  <table>
    <thead>
      <tr>
        <th>{groupBy === "day" ? t("Day") : groupBy === "project" ? t("Project") : t("Description")}</th>
        {#each visibleColumns as column (column)}
          <th class="num">{columnHead(column)}</th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each tableGroups as group (group.key)}
        <tr class="group">
          <td>
            {#if group.color}<span class="dot inline-dot" style="background: {group.color}"></span>{/if}
            {group.label}
          </td>
          {#each visibleColumns as column (column)}
            <td class="num">{cellValue(group, column)}</td>
          {/each}
        </tr>
        {#if showEntries}
          {#each group.entries as entry (entry.id)}
            <tr class="entry">
              <td>
                {entry.description || t("(no description)")}
                <span class="muted">
                  · {chipName(entryKey(entry))} · {formatDayISO(entry.date)} {formatTime(entry.startedAt)}
                </span>
              </td>
              {#each visibleColumns as column (column)}
                <td class="num muted">
                  {column === "duration"
                    ? fmtMin(overlapOnce ? minutesOf(entry) : roundMinutes(entry.minutes, rounding))
                    : ""}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      {:else}
        <tr><td colspan="9" class="muted">{t("No entries in range")}</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
  }

  .grid2 {
    display: grid;
    grid-template-columns: 2.2fr 1fr;
    gap: 1rem;
    align-items: start;
  }

  @media (max-width: 860px) {
    .grid2 {
      grid-template-columns: 1fr;
    }
  }

  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
  }

  .hint {
    font-size: 0.78rem;
  }

  .chips {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    border: 1px solid var(--border);
    border-radius: 99px;
    padding: 0.2rem 0.7rem;
    cursor: pointer;
    font-size: 0.85rem;
    background: var(--surface);
    user-select: none;
  }

  .chip.off {
    opacity: 0.4;
  }

  .chip .dot {
    width: 8px;
    height: 8px;
  }

  .filterpill {
    background: var(--accent);
    color: var(--accent-text);
    border: none;
    border-radius: 99px;
    padding: 0.15rem 0.6rem;
    font-size: 0.78rem;
    cursor: pointer;
    font-weight: 600;
  }

  .overlap-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.85rem;
    color: var(--text-dim);
    cursor: pointer;
    white-space: nowrap;
  }

  .overlap-toggle:has(input:checked) {
    color: var(--text);
  }

  .legend {
    display: flex;
    gap: 1.1rem;
    flex-wrap: wrap;
    margin-top: 0.6rem;
    font-size: 0.78rem;
    color: var(--text-dim);
    align-items: center;
  }

  .sw {
    display: inline-block;
    width: 12px;
    height: 12px;
    border-radius: 3px;
    margin-right: 5px;
    vertical-align: -2px;
  }

  .sick-sw {
    background: rgba(224, 82, 82, 0.35);
    border: 1px solid #e05252;
  }

  .proj-item {
    padding: 0.35rem 0;
    border-top: 1px solid var(--border);
  }

  .pct {
    width: 2.6rem;
    text-align: right;
  }

  .pbar {
    height: 5px;
    border-radius: 3px;
    background: var(--border);
    overflow: hidden;
    margin-top: 0.2rem;
  }

  .pbar div {
    height: 100%;
    border-radius: 3px;
  }

  .builder {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 0.9rem;
  }

  .builder h4 {
    margin: 0 0 0.4rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-dim);
  }

  .builder label {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.85rem;
    padding: 0.15rem 0;
    cursor: pointer;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }

  th {
    text-align: left;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-dim);
    padding: 0.4rem 0.5rem;
    border-bottom: 1px solid var(--border);
  }

  td {
    padding: 0.45rem 0.5rem;
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
    background: var(--hover);
  }

  tr.entry td:first-child {
    padding-left: 1.6rem;
    color: var(--text-dim);
  }

  .inline-dot {
    display: inline-block;
    margin-right: 0.4rem;
  }
</style>
