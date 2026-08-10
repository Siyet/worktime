<script lang="ts">
  import { appState } from "../lib/state/app.svelte";
  import { formatDurationShort, formatTime, localDateISO } from "../lib/format";
  import {
    apportion,
    buildCSV,
    dateRangeEntries,
    expandTimeOff,
    formatDayISO,
    groupKeysOf,
    listDays,
    maxChartDays,
    NO_PROJECT_KEY,
    roundMinutes,
    splitOverlapMinutes,
    summariseReport,
    UNTAGGED_KEY,
    type GroupBy,
    type ReportEntry,
  } from "../lib/report";
  import { t } from "../lib/i18n";
  import DailyChart, { type ChartDay } from "../lib/components/DailyChart.svelte";
  import Seg from "../lib/components/Seg.svelte";

  type Preset = "week" | "lastweek" | "month" | "30" | "";

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
  // Chips toggled OFF (default: everything active). Two independent strips;
  // an entry must pass both filters (AND across strips, any-of within one).
  let disabledProjects = $state<Set<string>>(new Set());
  let disabledTags = $state<Set<string>>(new Set());
  let groupBy = $state<GroupBy>("project");
  let showEntries = $state(false);
  let rounding = $state(0);
  let columns = $state({ duration: true, pct: true, entries: true, avg: false });
  // When on, wall-clock time is split equally between concurrent entries, so
  // an hour spent on two overlapping tasks counts as one hour in every total.
  let overlapOnce = $state(false);

  // Rounding and overlaps-once are mutually exclusive: rounding a share that
  // was deliberately halved re-inflates the very overlap the option removes
  // (roundMinutes has a one-step minimum). exportCSV has always forced
  // rounding to 0 in that mode; the UI follows the same rule.
  const effectiveRounding = $derived(overlapOnce ? 0 : rounding);

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
  const activeProjectKeys = $derived(new Set(chips.map((chip) => chip.key).filter((key) => !disabledProjects.has(key))));

  function toggleProject(key: string) {
    const next = new Set(disabledProjects);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
      // Keep at least one project active.
      if (chips.every((chip) => next.has(chip.key))) return;
    }
    disabledProjects = next;
  }

  // --- derived report data ---

  const off = $derived(expandTimeOff(appState.timeOff));
  const days = $derived(listDays(dateFrom, dateTo));

  const projectKey = (entry: ReportEntry) => entry.projectID ?? NO_PROJECT_KEY;
  const tagKeysOf = (entry: ReportEntry) => (entry.tags.length > 0 ? entry.tags : [UNTAGGED_KEY]);

  const rangeAllEntries = $derived(dateRangeEntries(appState.entries, dateFrom, dateTo));

  // The tag strip exists only while the range actually contains tagged entries - a
  // history with no tags must not grow empty UI. It is built from the unfiltered range
  // on purpose: chips that disappeared as you filtered would leave no way back.
  const hasTaggedEntries = $derived(rangeAllEntries.some((entry) => entry.tags.length > 0));
  const tagChips = $derived.by(() => {
    if (!hasTaggedEntries) return [];
    const names = new Set<string>();
    for (const entry of rangeAllEntries) {
      for (const tag of entry.tags) names.add(tag);
    }
    return [
      ...[...names].sort().map((name) => ({ key: name, name })),
      { key: UNTAGGED_KEY, name: t("untagged") },
    ];
  });
  const activeTagKeys = $derived(new Set(tagChips.map((chip) => chip.key).filter((key) => !disabledTags.has(key))));

  function toggleTag(key: string) {
    const next = new Set(disabledTags);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
      // Keep at least one tag chip active.
      if (tagChips.every((chip) => next.has(chip.key))) return;
    }
    disabledTags = next;
  }

  const rangeEntries = $derived(
    rangeAllEntries.filter(
      (entry) =>
        activeProjectKeys.has(projectKey(entry)) &&
        (tagChips.length === 0 || tagKeysOf(entry).some((key) => activeTagKeys.has(key))),
    ),
  );
  const filteredEntries = $derived(rangeEntries.filter((entry) => !dayFilter || entry.date === dayFilter));

  // Per-entry minutes to aggregate: raw durations, or overlap-adjusted shares.
  // Shares are computed on the full date range BEFORE the chip filters,
  // so hiding one project or tag never rewrites another's numbers.
  const effectiveMinutes = $derived(overlapOnce ? splitOverlapMinutes(rangeAllEntries) : null);
  const minutesOf = (entry: ReportEntry) => effectiveMinutes?.get(entry.id) ?? entry.minutes;

  // One aggregation, shared with the printable sheet, so the two cannot disagree
  // about the same range again.
  const summary = $derived(summariseReport(rangeEntries, days, off, minutesOf));
  const totalMinutes = $derived(summary.totalMinutes);
  const workDays = $derived(summary.workDays);
  const minutesByDay = $derived(summary.minutesByDay);
  const peakDay = $derived(summary.peakDay);
  const weekendMinutes = $derived(summary.weekendMinutes);
  const offCounts = $derived(summary.offCounts);
  const avgMinutes = $derived(summary.avgMinutes);

  const fmtHours = (minutes: number) => (minutes / 60).toFixed(1) + "h";
  const fmtMin = (minutes: number) => formatDurationShort(minutes * 60000);

  function chipName(key: string): string {
    return chips.find((chip) => chip.key === key)?.name ?? "No project";
  }

  function chipColor(key: string): string {
    return chips.find((chip) => chip.key === key)?.color ?? "var(--border)";
  }

  const chartTooWide = $derived(days.length > maxChartDays);

  const chartDays = $derived.by((): ChartDay[] => {
    if (chartTooWide) return [];
    const perDay = new Map<string, Map<string, number>>();
    for (const entry of rangeEntries) {
      const bucket = perDay.get(entry.date) ?? new Map<string, number>();
      bucket.set(projectKey(entry), (bucket.get(projectKey(entry)) ?? 0) + minutesOf(entry));
      perDay.set(entry.date, bucket);
    }
    return days.map((day) => ({
      date: day,
      slices: chips
        .filter((chip) => activeProjectKeys.has(chip.key))
        .map((chip) => ({
          key: chip.key,
          name: chip.name,
          color: chip.color,
          minutes: perDay.get(day)?.get(chip.key) ?? 0,
        })),
    }));
  });

  const byProject = $derived(summary.byProject);
  const byTag = $derived(summary.byTag);

  // The side cards invite the reader to add the column up, so the percentages are
  // apportioned rather than rounded one by one: three equal groups rendered
  // independently read 33+33+33, and the table on this very page - which does
  // apportion - would then disagree with the card beside it.
  const byProjectPct = $derived(
    apportion(
      byProject.map(([, minutes]) => minutes),
      totalMinutes > 0 ? 100 : 0,
    ),
  );
  const byTagPct = $derived(
    apportion(
      byTag.map(([, minutes]) => minutes),
      totalMinutes > 0 ? 100 : 0,
    ),
  );

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

  interface TableRow {
    entry: ReportEntry;
    share: number;
    of: number;
  }

  // billable(entry) is the single per-entry number every aggregate is built
  // from; the 1/k weights below always sum to 1 per entry, so the table total
  // is independent of the grouping.
  const billableOf = (entry: ReportEntry) =>
    overlapOnce ? minutesOf(entry) : roundMinutes(entry.minutes, effectiveRounding);

  // Description groups are keyed by the normalised spelling, so the label comes
  // from the newest member instead - lowercased keys are not what was typed.
  function newestDescription(rows: TableRow[]): string {
    return rows.reduce(
      (newest, row) => (row.entry.startedAt >= newest.startedAt ? row.entry : newest),
      rows[0]!.entry,
    ).description;
  }

  const tableGroups = $derived.by(() => {
    const groups = new Map<string, { minutes: number; rows: TableRow[] }>();
    for (const entry of filteredEntries) {
      const keys = groupKeysOf(entry, groupBy);
      const share = billableOf(entry) / keys.length;
      for (const key of keys) {
        const bucket = groups.get(key) ?? { minutes: 0, rows: [] };
        bucket.minutes += share;
        bucket.rows.push({ entry, share, of: keys.length });
        groups.set(key, bucket);
      }
    }
    const rows = [...groups.entries()];
    if (groupBy === "day") {
      rows.sort((left, right) => left[0].localeCompare(right[0]));
    } else {
      rows.sort((left, right) => right[1].minutes - left[1].minutes);
    }
    return rows.map(([key, bucket]) => ({
      key,
      label:
        groupBy === "day"
          ? formatDayISO(key)
          : groupBy === "project"
            ? chipName(key)
            : groupBy === "description"
              ? newestDescription(bucket.rows) || t("(no description)")
              : key,
      color: groupBy === "project" ? chipColor(key) : null,
      minutes: bucket.minutes,
      rows: bucket.rows.sort((left, right) => left.entry.startedAt - right.entry.startedAt),
    }));
  });

  // The header total is the sum of the visible groups, so table and header can
  // never disagree; the 1/k weights make it equal Σ billable over entries.
  const tableTotal = $derived(tableGroups.reduce((sum, group) => sum + group.minutes, 0));

  // Entries is a count, not a sum: a multi-tag entry is one entry in each of
  // its tag groups, so the Total row shows the distinct count instead.
  const distinctEntryCount = $derived(filteredEntries.length);
  const multiTagCount = $derived(filteredEntries.filter((entry) => entry.tags.length > 1).length);

  // Largest-remainder percentages: they sum to exactly 100 under every grouping.
  const pctByKey = $derived.by(() => {
    const shares = apportion(
      tableGroups.map((group) => group.minutes),
      tableTotal > 0 ? 100 : 0,
    );
    return new Map(tableGroups.map((group, index) => [group.key, shares[index]!]));
  });

  function cellValue(group: (typeof tableGroups)[number], column: string): string {
    if (column === "duration") return fmtMin(group.minutes);
    if (column === "pct") return (pctByKey.get(group.key) ?? 0) + "%";
    if (column === "entries") return String(group.rows.length);
    return fmtMin(Math.round(group.minutes / Math.max(1, group.rows.length)));
  }

  // --- actions ---

  function exportCSV() {
    // A CSV row is an entry, not a group: it carries the entry's full billable
    // minutes and never a tag share. With overlaps-once on, per-row rounding
    // would clamp fractional shares back up to a full step, so it exports the
    // raw shares instead.
    const csv = buildCSV(filteredEntries, (projectID) => chipName(projectID ?? NO_PROJECT_KEY), effectiveRounding, minutesOf);
    const blobURL = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
    const anchor = document.createElement("a");
    anchor.href = blobURL;
    anchor.download = "worktime-report.csv";
    // The download is handed to the browser asynchronously, so the URL is released on
    // the next tick - revoking it in the same task can invalidate the blob before the
    // fetch behind the download starts. The anchor goes into the document because a
    // synthetic click on a detached element does not navigate everywhere.
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(blobURL), 0);
  }

  function openPrint() {
    const params = new URLSearchParams({ from: dateFrom, to: dateTo });
    if (disabledProjects.size > 0) {
      params.set("projects", [...activeProjectKeys].join(","));
    }
    // The tag filter must travel too, or the sheet prints different numbers
    // than the screen it was printed from.
    if (disabledTags.size > 0 && tagChips.length > 0) {
      params.set("tags", [...activeTagKeys].join(","));
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
  <span class="chipstrip">
    <span class="caption">{t("Projects")}</span>
    {#each chips as chip (chip.key)}
      <button
        type="button"
        class="chip"
        class:off={disabledProjects.has(chip.key)}
        aria-pressed={!disabledProjects.has(chip.key)}
        onclick={() => toggleProject(chip.key)}
      >
        <span class="dot" style="background: {chip.color}"></span>{chip.name}
      </button>
    {/each}
  </span>
  {#if tagChips.length > 0}
    <span class="chipstrip">
      <span class="caption">{t("Tags")}</span>
      {#each tagChips as chip (chip.key)}
        <button
          type="button"
          class="chip tagchip"
          class:untagged={chip.key === UNTAGGED_KEY}
          class:off={disabledTags.has(chip.key)}
          aria-pressed={!disabledTags.has(chip.key)}
          onclick={() => toggleTag(chip.key)}
        >{chip.name}</button>
      {/each}
    </span>
  {/if}
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
    {#if chartTooWide}
      <p class="muted toowide">{t("The range is too wide to chart by day; the numbers below still cover all of it.")}</p>
    {:else}
      <DailyChart
        days={chartDays}
        {off}
        {avgMinutes}
        avgCaption={t("avg {h}h / work day", { h: (avgMinutes / 60).toFixed(1) })}
        selected={dayFilter}
        onselect={(day) => (dayFilter = day)}
      />
    {/if}
    <div class="legend">
      <span><span class="sw" style="background: var(--accent)"></span>{t("tracked hours")}</span>
      <span><span class="sw" style="background: rgba(96,125,190,0.3)"></span>{t("weekend")}</span>
      <span><span class="sw" style="background: rgba(64,190,196,0.35)"></span>{t("vacation")}</span>
      <span><span class="sw sick-sw"></span>{t("sick leave")}</span>
      <span><span class="sw" style="background: rgba(181,125,232,0.4)"></span>{t("day off")}</span>
    </div>
  </div>
  <div class="side">
    <div class="card">
      <h3>{t("By project")}</h3>
      {#each byProject as [key, minutes], index (key)}
        <div class="proj-item">
          <div class="row">
            <span class="dot" style="background: {chipColor(key)}"></span>
            <span>{chipName(key)}</span>
            <span class="spacer"></span>
            <span class="mono">{fmtMin(minutes)}</span>
            <span class="muted mono pct">{byProjectPct[index] ?? 0}%</span>
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
    {#if byTag.length > 0}
      <div class="card">
        <h3>{t("By tag")}</h3>
        {#each byTag as [key, minutes], index (key)}
          <div class="proj-item">
            <div class="row">
              {#if key === UNTAGGED_KEY}
                <span class="untaglabel">{t("untagged")}</span>
              {:else}
                <span>{key}</span>
              {/if}
              <span class="spacer"></span>
              <span class="mono">{fmtMin(minutes)}</span>
              <span class="muted mono pct">{byTagPct[index] ?? 0}%</span>
            </div>
            <!-- One ink for every bar: length ranks, hue would collide with the
                 project dots one card above. -->
            <div class="pbar">
              <div style="width: {totalMinutes ? (minutes / totalMinutes) * 100 : 0}%; background: var(--accent)"></div>
            </div>
          </div>
        {/each}
        <p class="reconcile">
          {t("An entry with several tags splits its time equally between them, so this adds up to the same total as By project.")}
        </p>
      </div>
    {/if}
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
          { value: "tag", label: t("Tag") },
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
        disabled={overlapOnce}
        options={[
          { value: "0", label: t("Off") },
          { value: "15", label: "15m" },
          { value: "30", label: "30m" },
          { value: "60", label: "1h" },
        ]}
        value={overlapOnce ? "0" : String(rounding)}
        onselect={(value) => (rounding = Number(value))}
      />
      {#if overlapOnce}
        <p class="reconcile">
          {t("Rounding is off while overlaps are counted once: rounding a halved share puts the overlap back.")}
        </p>
      {/if}
    </div>
  </div>
</div>

<div class="card">
  <div class="row">
    <h3>{t("Report")} — {t("by " + groupBy)}{dayFilter ? ` · ${formatDayISO(dayFilter)}` : ""}</h3>
    <span class="spacer"></span>
    <span class="muted mono">{fmtMin(tableTotal)}</span>
  </div>
  <div class="tablewrap">
  <table>
    <thead>
      <tr>
        <th>
          {groupBy === "day"
            ? t("Day")
            : groupBy === "project"
              ? t("Project")
              : groupBy === "tag"
                ? t("Tag")
                : t("Description")}
        </th>
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
            {#if group.key === UNTAGGED_KEY && groupBy === "tag"}
              <span class="untaglabel">{t("untagged")}</span>
            {:else}
              {group.label}
            {/if}
          </td>
          {#each visibleColumns as column (column)}
            <td class="num">{cellValue(group, column)}</td>
          {/each}
        </tr>
        {#if showEntries}
          {#each group.rows as row (row.entry.id)}
            <tr class="entry">
              <td>
                {row.entry.description || t("(no description)")}
                <span class="muted">
                  · {chipName(projectKey(row.entry))} · {formatDayISO(row.entry.date)} {formatTime(row.entry.startedAt)}
                </span>
              </td>
              {#each visibleColumns as column (column)}
                <td class="num muted">
                  {#if column === "duration"}
                    <!-- row.share, not the full duration: detail rows must sum
                         to their group header. The 1/k marker says why it is
                         smaller than the entry itself. -->
                    {fmtMin(row.share)}
                    {#if row.of > 1}
                      <span class="splitmark" title={t("shared with {n} tags", { n: row.of })}>1/{row.of}</span>
                    {/if}
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      {:else}
        <tr><td colspan="9" class="muted">{t("No entries in range")}</td></tr>
      {/each}
      {#if tableGroups.length > 0}
        <tr class="total">
          <td>{t("Total")}</td>
          {#each visibleColumns as column (column)}
            <td class="num">
              {column === "duration"
                ? fmtMin(tableTotal)
                : column === "pct"
                  ? "100%"
                  : column === "entries"
                    ? String(distinctEntryCount)
                    : fmtMin(Math.round(tableTotal / Math.max(1, distinctEntryCount)))}
            </td>
          {/each}
        </tr>
      {/if}
    </tbody>
  </table>
  </div>
  {#if groupBy === "tag" && multiTagCount > 0}
    <p class="reconcile">
      {t(
        "{n} entries carry more than one tag. Their duration is split equally, so Duration and % add up to the total exactly; the Entries column counts such an entry in every tag row, which is why the group counts sum above {total}.",
        { n: multiTagCount, total: distinctEntryCount },
      )}
    </p>
  {/if}
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

  .chipstrip {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
  }

  .chipstrip > .caption {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-dim);
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

  /* Tag filter chip: the .chip idiom minus the dot. */
  .chip.tagchip {
    padding: 0.2rem 0.7rem;
  }

  .chip.tagchip.untagged {
    border-style: dashed;
    font-style: italic;
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

  .toowide {
    margin: 1.2rem 0;
    text-align: center;
    font-size: 0.85rem;
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

  /* Right column is a stack now: By project + By tag. */
  .side {
    display: grid;
    gap: 1rem;
    align-content: start;
  }

  .side .card {
    margin-bottom: 0;
  }

  .proj-item {
    padding: 0.35rem 0;
    border-top: 1px solid var(--border);
  }

  .untaglabel {
    font-style: italic;
    color: var(--text-dim);
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
    white-space: nowrap;
  }

  .tablewrap {
    overflow-x: auto;
  }

  /* Header totals ("16h 30m") and per-project durations never wrap. */
  .row > .mono {
    white-space: nowrap;
  }

  tr.group td {
    font-weight: 600;
    background: var(--hover);
  }

  tr.entry td:first-child {
    padding-left: 1.6rem;
    color: var(--text-dim);
  }

  /* Proof row: Duration and % add up to the header, in the table itself. */
  tr.total td {
    font-weight: 600;
    border-top: 1.5px solid var(--text-dim);
    border-bottom: none;
  }

  /* Weighted contribution of a multi-tag entry under one tag group. */
  .splitmark {
    font-family: var(--mono);
    font-size: 0.78rem;
    color: var(--text-dim);
    margin-left: 0.35rem;
  }

  .reconcile {
    margin: 0.6rem 0 0;
    font-size: 0.78rem;
    color: var(--text-dim);
  }

  .inline-dot {
    display: inline-block;
    margin-right: 0.4rem;
  }

  @media (max-width: 34rem) {
    .toolbar {
      gap: 0.5rem;
    }

    .toolbar > :global(.seg) {
      display: flex;
      width: 100%;
    }

    /* flex-basis auto + min-width 0: long locale labels ("Прошлая неделя")
       keep their width and only shrink under real pressure, with ellipsis
       instead of the .seg overflow clip. */
    .toolbar > :global(.seg button) {
      flex: 1 1 auto;
      min-width: 0;
      padding: 0.35rem 0.4rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .toolbar > input[type="date"] {
      flex: 1 1 40%;
      min-width: 0;
    }

    .toolbar .overlap-toggle {
      flex: 1 1 100%;
    }

    .toolbar > .spacer {
      display: none;
    }

    /* Only the two action buttons; the day-filter pill keeps its shape. */
    .toolbar > button:not(.filterpill) {
      flex: 1 1 40%;
      min-height: 2.75rem;
    }

    /* Group by: 2x2 grid instead of a 150px vertical stack. Page-scoped
       :global rules out-specify Seg's :where()-scoped component rules. */
    .builder :global(.seg.vertical) {
      display: grid;
      grid-template-columns: 1fr 1fr;
      width: 100%;
    }

    .builder :global(.seg.vertical button) {
      border: none;
    }

    .builder :global(.seg.vertical button:nth-child(even)) {
      border-left: 1px solid var(--border);
    }

    .builder :global(.seg.vertical button:nth-child(n + 3)) {
      border-top: 1px solid var(--border);
    }

    table {
      font-size: 0.85rem;
    }

    td,
    th {
      padding: 0.4rem 0.3rem;
    }

    td:first-child {
      overflow-wrap: anywhere;
    }
  }
</style>
