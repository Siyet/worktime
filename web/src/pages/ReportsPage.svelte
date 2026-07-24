<script lang="ts">
  import { appState, projectByID } from "../lib/state/app.svelte";
  import { formatDurationShort, localDateISO } from "../lib/format";

  type Period = "week" | "month" | "30days";
  let period = $state<Period>("week");

  // Reports are computed locally from IndexedDB state, so they work offline too.
  const range = $derived.by(() => {
    const now = new Date();
    if (period === "30days") {
      return { from: now.getTime() - 30 * 24 * 3600 * 1000, to: now.getTime() };
    }
    const start = new Date(now.getFullYear(), now.getMonth(), 1);
    if (period === "week") {
      const weekday = (now.getDay() + 6) % 7; // Monday as the first day
      start.setFullYear(now.getFullYear(), now.getMonth(), now.getDate() - weekday);
    }
    return { from: start.getTime(), to: now.getTime() };
  });

  const projectTotals = $derived.by(() => {
    const totals = new Map<string | null, number>();
    for (const entry of appState.entries) {
      if (entry.stopped_at === null) continue;
      if (entry.started_at < range.from || entry.started_at >= range.to) continue;
      const key = entry.project_id;
      totals.set(key, (totals.get(key) ?? 0) + (entry.stopped_at - entry.started_at));
    }
    return [...totals.entries()].sort((left, right) => right[1] - left[1]);
  });

  const grandTotal = $derived(projectTotals.reduce((sum, [, total]) => sum + total, 0));

  const timeOffDays = $derived.by(() => {
    const fromISO = localDateISO(range.from);
    const toISO = localDateISO(range.to);
    const days = { vacation: 0, sick: 0 };
    for (const timeOff of appState.timeOff) {
      const overlapFrom = timeOff.date_from > fromISO ? timeOff.date_from : fromISO;
      const overlapTo = timeOff.date_to < toISO ? timeOff.date_to : toISO;
      if (overlapTo < overlapFrom) continue;
      days[timeOff.kind] += Math.round((Date.parse(overlapTo) - Date.parse(overlapFrom)) / 86400000) + 1;
    }
    return days;
  });

  const maxTotal = $derived(projectTotals[0]?.[1] ?? 1);
</script>

<div class="card row">
  <label for="period">Period</label>
  <select id="period" bind:value={period}>
    <option value="week">This week</option>
    <option value="month">This month</option>
    <option value="30days">Last 30 days</option>
  </select>
  <span class="spacer"></span>
  <strong class="mono">{formatDurationShort(grandTotal)}</strong>
</div>

<div class="card">
  {#each projectTotals as [projectID, total] (projectID)}
    {@const project = projectByID(projectID)}
    <div class="item">
      <div class="row">
        <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
        <span>{project?.name ?? "No project"}</span>
        <span class="spacer"></span>
        <span class="mono">{formatDurationShort(total)}</span>
      </div>
      <div class="bar" style="width: {(total / maxTotal) * 100}%; background: {project?.color ?? 'var(--border)'}"></div>
    </div>
  {:else}
    <p class="muted">No finished entries in this period.</p>
  {/each}
</div>

{#if timeOffDays.vacation > 0 || timeOffDays.sick > 0}
  <div class="card row">
    <span class="muted">Time off:</span>
    {#if timeOffDays.vacation > 0}<span>{timeOffDays.vacation}d vacation</span>{/if}
    {#if timeOffDays.sick > 0}<span>{timeOffDays.sick}d sick</span>{/if}
  </div>
{/if}

<style>
  .item {
    padding: 0.4rem 0;
  }

  .bar {
    height: 4px;
    border-radius: 2px;
    margin: 0.25rem 0 0 1.2rem;
    min-width: 2px;
  }
</style>
