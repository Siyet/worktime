<script lang="ts">
  import {
    appState,
    clock,
    deleteEntry,
    projectByID,
    runningEntries,
    startTimer,
    stopTimer,
  } from "../lib/state/app.svelte";
  import { formatDay, formatDuration, formatDurationShort, formatTime, localDateISO } from "../lib/format";
  import ProjectSelect from "../lib/components/ProjectSelect.svelte";
  import TrashIcon from "../lib/components/TrashIcon.svelte";
  import type { TimeEntry } from "../lib/types";

  let description = $state("");
  let selectedProjectID = $state<string | null>(null);

  const activeProjects = $derived(
    appState.projects.filter((project) => !project.archived).sort((a, b) => a.name.localeCompare(b.name)),
  );
  const running = $derived(runningEntries());

  const todayISO = $derived(localDateISO(clock.now));
  const todayTimeOff = $derived(
    appState.timeOff.find((timeOff) => timeOff.date_from <= todayISO && todayISO <= timeOff.date_to),
  );

  // Finished entries from the last 7 days, newest first, grouped by day.
  const recentDays = $derived.by(() => {
    const weekAgo = clock.now - 7 * 24 * 3600 * 1000;
    const finished = appState.entries
      .filter((entry) => entry.stopped_at !== null && entry.started_at >= weekAgo)
      .sort((left, right) => right.started_at - left.started_at);
    const groups = new Map<string, TimeEntry[]>();
    for (const entry of finished) {
      const day = localDateISO(entry.started_at);
      const bucket = groups.get(day) ?? [];
      bucket.push(entry);
      groups.set(day, bucket);
    }
    return [...groups.entries()];
  });

  async function submitStart(event: SubmitEvent) {
    event.preventDefault();
    // Clear the input before the async write so text typed right after
    // submitting is never wiped by a late reset.
    const submitted = description.trim();
    description = "";
    await startTimer(submitted, selectedProjectID);
  }

  function dayTotal(entries: TimeEntry[]): number {
    return entries.reduce((sum, entry) => sum + ((entry.stopped_at ?? entry.started_at) - entry.started_at), 0);
  }
</script>

{#if todayTimeOff}
  <div class="card muted">
    Today is marked as {todayTimeOff.kind === "sick" ? "sick leave" : "vacation"} - timers still work.
  </div>
{/if}

<form class="card row" onsubmit={submitStart}>
  <input
    style="flex: 1"
    placeholder="What are you working on?"
    bind:value={description}
    aria-label="Description"
  />
  <ProjectSelect projects={activeProjects} bind:value={selectedProjectID} />
  <button class="primary" type="submit">Start</button>
</form>

{#if running.length > 0}
  <div class="card">
    <h3>Running</h3>
    {#each running as entry (entry.id)}
      {@const project = projectByID(entry.project_id)}
      <div class="row item">
        <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
        <span>{entry.description || "(no description)"}</span>
        {#if project}<span class="muted">{project.name}</span>{/if}
        <span class="spacer"></span>
        <span class="mono elapsed">{formatDuration(clock.now - entry.started_at)}</span>
        <button onclick={() => stopTimer(entry.id)}>Stop</button>
      </div>
    {/each}
  </div>
{/if}

{#each recentDays as [day, entries] (day)}
  <div class="card">
    <div class="row">
      <h3>{formatDay(entries[0]!.started_at)}</h3>
      <span class="spacer"></span>
      <span class="muted mono">{formatDurationShort(dayTotal(entries))}</span>
    </div>
    {#each entries as entry (entry.id)}
      {@const project = projectByID(entry.project_id)}
      <div class="row item">
        <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
        <span>{entry.description || "(no description)"}</span>
        {#if project}<span class="muted">{project.name}</span>{/if}
        <span class="spacer"></span>
        <span class="muted mono">
          {formatTime(entry.started_at)}-{formatTime(entry.stopped_at!)}
        </span>
        <span class="mono">{formatDurationShort(entry.stopped_at! - entry.started_at)}</span>
        <button class="danger icon" title="Delete entry" onclick={() => deleteEntry(entry.id)}><TrashIcon /></button>
      </div>
    {/each}
  </div>
{/each}

{#if running.length === 0 && recentDays.length === 0}
  <p class="muted">No entries yet. Start your first timer above.</p>
{/if}

<style>
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
  }

  .item {
    padding: 0.35rem 0;
    border-top: 1px solid var(--border);
  }

  .elapsed {
    font-size: 1.05rem;
    font-weight: 600;
  }
</style>
