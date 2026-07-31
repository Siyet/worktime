<script lang="ts">
  import {
    appState,
    clock,
    entryTags,
    projectByID,
    runningEntries,
    startTimer,
    stopTimer,
  } from "../lib/state/app.svelte";
  import { deleteEntryWithUndo } from "../lib/state/undo.svelte";
  import { formatDay, formatDuration, formatDurationShort, formatTime, localDateISO } from "../lib/format";
  import { t } from "../lib/i18n";
  import EntryEditor from "../lib/components/EntryEditor.svelte";
  import ProjectSelect from "../lib/components/ProjectSelect.svelte";
  import RowMenu from "../lib/components/RowMenu.svelte";
  import TagChips from "../lib/components/TagChips.svelte";
  import TagSelect from "../lib/components/TagSelect.svelte";
  import type { TimeEntry } from "../lib/types";

  let description = $state("");
  let selectedProjectID = $state<string | null>(null);
  let selectedTags = $state<string[]>([]);
  let editingID = $state<string | null>(null);

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
    // Clear the inputs before the async write so text typed right after
    // submitting is never wiped by a late reset. Tags reset like the
    // description - they describe the task, not the session.
    const submitted = description.trim();
    const tags = [...selectedTags];
    description = "";
    selectedTags = [];
    await startTimer(submitted, selectedProjectID, tags);
  }

  function dayTotal(entries: TimeEntry[]): number {
    return entries.reduce((sum, entry) => sum + ((entry.stopped_at ?? entry.started_at) - entry.started_at), 0);
  }
</script>

{#if todayTimeOff}
  <div class="card muted">
    {t("Today is marked as {kind} - timers still work.", {
      kind: t(todayTimeOff.kind === "sick" ? "sick leave" : todayTimeOff.kind === "dayoff" ? "a day off" : "vacation"),
    })}
  </div>
{/if}

<form class="card row" onsubmit={submitStart}>
  <input
    class="grow"
    placeholder={t("What are you working on?")}
    bind:value={description}
    aria-label={t("Description")}
  />
  <ProjectSelect projects={activeProjects} bind:value={selectedProjectID} />
  <TagSelect bind:selected={selectedTags} />
  <button class="primary" type="submit">{t("Start")}</button>
</form>

{#if running.length > 0}
  <div class="card">
    <h3>{t("Running")}</h3>
    {#each running as entry (entry.id)}
      {@const project = projectByID(entry.project_id)}
      <div class="row item">
        <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
        <span class="main">
          <button type="button" class="desc" onclick={() => (editingID = entry.id)}>
            {entry.description || t("(no description)")}
          </button>
          <span class="meta muted">
            {#if project}<span class="proj">{project.name}</span>{/if}
            <TagChips tags={entryTags(entry)} />
          </span>
        </span>
        <span class="when">
          <span class="dur elapsed">{formatDuration(clock.now - entry.started_at)}</span>
        </span>
        <button onclick={() => stopTimer(entry.id)}>{t("Stop")}</button>
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
        <span class="main">
          <button type="button" class="desc" onclick={() => (editingID = entry.id)}>
            {entry.description || t("(no description)")}
          </button>
          <span class="meta muted">
            {#if project}<span class="proj">{project.name}</span>{/if}
            <TagChips tags={entryTags(entry)} />
          </span>
        </span>
        <span class="when">
          <span class="dur">{formatDurationShort(entry.stopped_at! - entry.started_at)}</span>
          <span class="range muted"><span class="from">{formatTime(entry.started_at)}</span><span class="to"
            >-{formatTime(entry.stopped_at!)}</span
          ></span>
        </span>
        <RowMenu onedit={() => (editingID = entry.id)} ondelete={() => void deleteEntryWithUndo(entry)} />
      </div>
    {/each}
  </div>
{/each}

{#if running.length === 0 && recentDays.length === 0}
  <p class="muted">{t("No entries yet. Start your first timer above.")}</p>
{/if}

{#if editingID !== null}
  <EntryEditor entryID={editingID} onclose={() => (editingID = null)} />
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

  .grow {
    flex: 1;
  }

  /* Day totals like "3h 10m" must never break mid-value. */
  .row > .mono {
    white-space: nowrap;
  }

  @media (max-width: 34rem) {
    form.row {
      flex-wrap: wrap;
    }

    .grow {
      flex: 1 1 100%;
    }

    /* Row 2: project + tags share the line; row 3: a full-width Start in the
       thumb zone. */
    form.row :global(.pselect) {
      flex: 1 1 auto;
      min-width: 0;
    }

    form.row :global(.pselect > button) {
      width: 100%;
      max-width: none;
    }

    form.row :global(.pselect .caret) {
      margin-left: auto;
    }

    form.row :global(.menu-wrap) {
      flex: 0 1 auto;
      min-width: 0;
    }

    form.row > button.primary {
      flex: 1 1 100%;
      min-width: 0;
    }
  }
</style>
