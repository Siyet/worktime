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
  import {
    entryDurationMs,
    displayEntryDescription,
    formatDay,
    formatDuration,
    formatDurationShort,
    formatTime,
    localDateISO,
  } from "../lib/format";
  import { groupDayEntries, suggestionWindowStart, taskSuggestions, wallClockMs, type TaskGroup } from "../lib/tasks";
  import { t } from "../lib/i18n";
  import { maxTextLength } from "../lib/limits";
  import DescriptionInput from "../lib/components/DescriptionInput.svelte";
  import EntryEditor from "../lib/components/EntryEditor.svelte";
  import EntryProjectMenu from "../lib/components/EntryProjectMenu.svelte";
  import EntryTagsMenu from "../lib/components/EntryTagsMenu.svelte";
  import ProjectSelect from "../lib/components/ProjectSelect.svelte";
  import RowMenu from "../lib/components/RowMenu.svelte";
  import TagChips from "../lib/components/TagChips.svelte";
  import TagSelect from "../lib/components/TagSelect.svelte";
  import type { TimeEntry } from "../lib/types";

  let description = $state("");
  let selectedProjectID = $state<string | null>(null);
  let selectedTags = $state<string[]>([]);
  let editingID = $state<string | null>(null);
  // Expansion is keyed by day as well: an agent task carries the same name every
  // day, and one shared key would unfold it in every card at once.
  let expanded = $state(new Set<string>());

  const activeProjects = $derived(
    appState.projects.filter((project) => !project.archived).sort((a, b) => a.name.localeCompare(b.name)),
  );
  const running = $derived(runningEntries());
  const runningGroups = $derived(groupDayEntries(running, clock.now));

  const todayISO = $derived(localDateISO(clock.now));
  const todayTimeOff = $derived(
    appState.timeOff.find((timeOff) => timeOff.date_from <= todayISO && todayISO <= timeOff.date_to),
  );

  // Derived from todayISO rather than clock.now, so this changes once a day instead of
  // once a second - otherwise every scan below reruns on every tick.
  const windowStart = $derived(suggestionWindowStart(new Date(todayISO + "T12:00").getTime()));

  // Finished entries from the feed window, grouped by day and then by task.
  const recentDays = $derived.by(() => {
    const finished = appState.entries
      .filter((entry) => entry.stopped_at !== null && entry.started_at >= windowStart)
      .sort((left, right) => right.started_at - left.started_at);
    const days = new Map<string, TimeEntry[]>();
    for (const entry of finished) {
      const day = localDateISO(entry.started_at);
      const bucket = days.get(day) ?? [];
      bucket.push(entry);
      days.set(day, bucket);
    }
    return [...days.entries()].map(
      // Every entry here is finished, so groupDayEntries never reads `now` - passing
      // clock.now would subscribe the whole feed to the ticker for nothing.
      ([day, entries]) => [day, entries, groupDayEntries(entries)] as [string, TimeEntry[], TaskGroup[]],
    );
  });

  // The window the suggestions come from is derived once, not rebuilt per
  // keystroke: the instance holds more than ten thousand entries.
  const suggestionSource = $derived(
    appState.entries.filter((entry) => entry.stopped_at === null || entry.started_at >= windowStart),
  );
  const suggestions = $derived(taskSuggestions(suggestionSource, description, windowStart));

  function projectName(projectID: string | null): string {
    return projectByID(projectID)?.name ?? "";
  }

  function applySuggestion(suggestion: { projectID: string | null; tags: string[] }) {
    // Only empty fields are filled: a project already chosen by hand wins.
    if (selectedProjectID === null) selectedProjectID = suggestion.projectID;
    if (selectedTags.length === 0) selectedTags = [...suggestion.tags];
  }

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

  function toggleGroup(dayISO: string, group: TaskGroup) {
    const key = `${dayISO}\u0000${group.key}`;
    const next = new Set(expanded);
    if (!next.delete(key)) next.add(key);
    expanded = next;
  }

  function isExpanded(dayISO: string, group: TaskGroup): boolean {
    return expanded.has(`${dayISO}\u0000${group.key}`);
  }

  function dayTotal(groups: TaskGroup[]): number {
    return groups.reduce((sum, group) => sum + group.totalMs, 0);
  }

  // Start the group's task over. Everything that made the entries one group is
  // exactly what a repeat needs - the description, the project and the tags -
  // so the row already holds the whole timer.
  function repeatGroup(group: TaskGroup) {
    void startTimer(group.description, group.projectID, [...group.tags]);
  }

</script>

<!-- The dial marks a wall-clock reading, so the two numbers next to each other
     are never mistaken for one another. Same shape as the app's own logo. -->
{#snippet dialIcon()}
  <svg
    width="11"
    height="11"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2.2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M12 7.5V12l3 2" /></svg>
{/snippet}

<!-- One row of the list. Groups of one render exactly like before, which keeps
     both the look and every existing selector intact. -->
{#snippet entryRow(entry: TimeEntry, member: boolean)}
  {@const project = projectByID(entry.project_id)}
  <div class="row item" class:member>
    <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
    <span class="main">
      <button
        type="button"
        class="desc"
        onclick={() => (editingID = entry.id)}
      >
        {displayEntryDescription(entry) || t("(no description)")}
      </button>
      <span class="meta muted">
        <EntryProjectMenu entryID={entry.id} projectID={entry.project_id} />
        <EntryTagsMenu entryID={entry.id} tags={entryTags(entry)} />
      </span>
    </span>
    {#if entry.stopped_at === null}
      <span class="when">
        <span class="dur elapsed">{formatDuration(entryDurationMs(entry, clock.now))}</span>
      </span>
      <button onclick={() => stopTimer(entry.id)}>{t("Stop")}</button>
    {:else}
      <span class="when">
        <!-- The entry is finished, so its duration is fixed; reading clock.now here
             would subscribe every row in the list to the one-second ticker. -->
        <span class="dur">{formatDurationShort(entryDurationMs(entry, entry.stopped_at))}</span>
        <span class="range muted"><span class="from">{formatTime(entry.started_at)}</span><span class="to"
          >-{formatTime(entry.stopped_at)}</span
        ></span>
      </span>
      <RowMenu onedit={() => (editingID = entry.id)} ondelete={() => void deleteEntryWithUndo(entry)} />
    {/if}
  </div>
{/snippet}

<!-- A group of several entries. The summary itself is the control: a group has
     nothing else to click, and a lone caret at the far right reads as another
     kebab menu rather than as "there is more inside". The repeat button is a
     sibling rather than a child - a button inside a button is invalid markup,
     and a click on it must not unfold the group. The summary deliberately does
     not carry .item - the row is a summary, not an entry, and counting .item
     must keep counting entries. -->
{#snippet groupRow(dayISO: string, group: TaskGroup, index: number)}
  {@const project = projectByID(group.projectID)}
  {@const listID = `g-${dayISO}-${index}`}
  {@const shown = isExpanded(dayISO, group)}
  {@const countLabel = t("{n} entries", { n: group.entries.length })}
  {@const displayedGroupDescription = displayEntryDescription(group.entries[0]!)}
  <!-- A running group is already this task, right now: repeating it would only
       add a second timer for the same work. -->
  {@const repeatable = group.lastStoppedAt !== null}
  <div class="row group-line" class:open={shown}>
    <button
      type="button"
      class="row group-row"
      aria-expanded={shown}
      {...shown ? { "aria-controls": listID } : {}}
      onclick={() => toggleGroup(dayISO, group)}
    >
      <svg
        class="caret"
        class:open={shown}
        width="13"
        height="13"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2.6"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"><path d="M9 5l7 7-7 7" /></svg>
      <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
      <span class="main">
        <span class="desc-line">
          <span class="desc-static">{displayedGroupDescription || t("(no description)")}</span>
          <!-- Stacked lines plus the number: "several rows live here" without a
               noun, which no plural rule can then get wrong. -->
          <span class="count" title={countLabel}>
            <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" aria-hidden="true">
              <path d="M3 4.5h10" /><path d="M3 8h10" /><path d="M3 11.5h10" />
            </svg>
            <span class="count-value">{group.entries.length}</span>
            <span class="sr-only">{countLabel}</span>
          </span>
        </span>
        <span class="meta muted">
          {#if project}<span class="proj">{project.name}</span>{/if}
          <TagChips tags={group.tags} />
          {#if group.wallMs !== group.totalMs}
            <!-- Two hours of tracked time inside one hour of wall clock reads as a
                 mistake without the second number; the dial says which of the two
                 it is without a word of text. It sits here rather than in the
                 right column, where it would knock the duration out of the line
                 every other row keeps. -->
            {@const overlapLabel = t("{n} entries overlapped; on the clock this is {wall}", {
              n: group.entries.length,
              wall: formatDurationShort(group.wallMs),
            })}
            <span class="wall mono" title={overlapLabel}>
              {@render dialIcon()}{formatDurationShort(group.wallMs)}
              <span class="sr-only">{overlapLabel}</span>
            </span>
          {/if}
        </span>
      </span>
      <span class="when">
        <span class="dur">{formatDurationShort(group.totalMs)}</span>
        {#if group.lastStoppedAt !== null}
          <span class="range muted"><span class="from">{formatTime(group.firstStartedAt)}</span><span class="to"
            >-{formatTime(group.lastStoppedAt)}</span
          ></span>
        {/if}
      </span>
      <span class="sr-only">{shown ? t("Collapse") : t("Expand")}</span>
    </button>
    {#if repeatable}
      <!-- "Repeat", not "Start again": Playwright matches an accessible name by
           substring, and every spec that clicks the form's Start button would
           then hit this one too. -->
      {@const repeatLabel = t("Repeat {task}", { task: displayedGroupDescription })}
      <button type="button" class="kebab icon repeat" aria-label={repeatLabel} title={repeatLabel} onclick={() => repeatGroup(group)}>
        <svg
          width="15"
          height="15"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"><path d="M20.5 12a8.5 8.5 0 1 1-2.49-6.01" /><path d="M20.5 4.5V10H15" /></svg>
      </button>
    {:else}
      <span class="menu-space" aria-hidden="true"></span>
    {/if}
  </div>
  {#if shown}
    <div class="members" id={listID}>
      {#each group.entries as entry (entry.id)}
        {@render entryRow(entry, true)}
      {/each}
    </div>
  {/if}
{/snippet}

{#if todayTimeOff}
  <div class="card muted">
    {t("Today is marked as {kind} - timers still work.", {
      kind: t(todayTimeOff.kind === "sick" ? "sick leave" : todayTimeOff.kind === "dayoff" ? "a day off" : "vacation"),
    })}
  </div>
{/if}

<form class="card row" onsubmit={submitStart}>
  <DescriptionInput
    bind:value={description}
    {suggestions}
    {projectName}
    onpick={applySuggestion}
    maxlength={maxTextLength}
    placeholder={t("What are you working on?")}
    ariaLabel="Description"
  />
  <ProjectSelect projects={activeProjects} bind:value={selectedProjectID} />
  <TagSelect bind:selected={selectedTags} />
  <button class="primary" type="submit">{t("Start")}</button>
</form>

{#if running.length > 0}
  <div class="card" class:has-groups={runningGroups.some((group) => group.entries.length > 1)}>
    <h3>{t("Running")}</h3>
    {#each runningGroups as group, index (group.key)}
      {#if group.entries.length === 1}
        {@render entryRow(group.entries[0]!, false)}
      {:else}
        {@render groupRow("running", group, index)}
      {/if}
    {/each}
  </div>
{/if}

{#each recentDays as [day, entries, groups] (day)}
  {@const tracked = dayTotal(groups)}
  {@const wall = wallClockMs(entries, clock.now)}
  <div class="card" class:has-groups={groups.some((group) => group.entries.length > 1)}>
    <div class="row">
      <h3>{formatDay(entries[0]!.started_at)}</h3>
      <span class="spacer"></span>
      {#if wall !== tracked}
        <!-- Two readings of one day: how much was tracked, and how much clock time
             it took with parallel work counted once. Side by side and unlabelled
             they read as the same number printed twice, so they get a divider and
             one sentence that says which is which. -->
        {@const totalsLabel = t("{wall} on the clock, {tracked} tracked - work that ran in parallel is counted once", {
          wall: formatDurationShort(wall),
          tracked: formatDurationShort(tracked),
        })}
        <span class="muted mono totals" title={totalsLabel}>
          <span class="wall">{@render dialIcon()}{formatDurationShort(wall)}</span>
          <span class="sep" aria-hidden="true">/</span>
          <span class="tracked">{formatDurationShort(tracked)}</span>
          <span class="sr-only">{totalsLabel}</span>
        </span>
      {:else}
        <span class="muted mono tracked">{formatDurationShort(tracked)}</span>
      {/if}
    </div>
    {#each groups as group, index (group.key)}
      {#if group.entries.length === 1}
        {@render entryRow(group.entries[0]!, false)}
      {:else}
        {@render groupRow(day, group, index)}
      {/if}
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

  /* The line carries what a row carries - the rule above it, the padding and the
     tint - so the summary and the repeat button sit inside one band instead of
     looking like two controls stacked side by side. */
  .group-line {
    padding: 0.35rem 0;
    border-top: 1px solid var(--border);
  }

  /* The summary itself is a real button spanning everything but the repeat slot:
     the group has no other action, so every part of it toggles. Button defaults
     are reset rather than avoided, because the row has to keep looking like a
     row. */
  .group-row {
    flex: 1;
    min-width: 0;
    padding: 0;
    border: none;
    border-radius: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  @media (hover: hover) {
    .group-line:hover {
      background: var(--hover);
    }
  }

  /* An open group keeps the tint: the summary is the header of the block below
     it, and the rail alone is a thin thing to carry that on its own. */
  .group-line.open {
    background: var(--hover);
  }

  /* Same footprint as the kebab it stands in for, so the right edge of every
     row in the card stays put whether or not the row is a group. */
  .repeat {
    flex: none;
  }

  /* The rail ties the unfolded entries to the row they came from; without it
     the indent alone reads as a stray gap. */
  .members {
    position: relative;
  }

  .members::before {
    content: "";
    position: absolute;
    left: 0.42rem;
    top: 0;
    bottom: 0.35rem;
    width: 1px;
    background: var(--border);
  }

  .members .item:first-child {
    border-top: none;
  }

  /* A card holding a group reserves the caret column on its plain rows too, so
     every project dot in the card stays in one line - the same way a file tree
     lines up leaves with folders. */
  .has-groups .item {
    padding-left: 1.5rem;
  }

  .item.member,
  .has-groups .item.member {
    padding-left: 2.4rem;
  }

  .elapsed {
    font-size: 1.05rem;
    font-weight: 600;
  }

  .desc-line {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    min-width: 0;
  }

  .desc-static {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .group-row .dot {
    align-self: flex-start;
    margin-top: 0.45rem;
  }

  /* Not dim: the count is what says "this row stands for several", so it has to
     read before the description does. */
  /* Deliberately not a pill: tag chips are pills, and a second pill on the same
     line reads as another tag. Square, tinted and tabular says "counter". */
  .count {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    flex: none;
    font-size: 0.78rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--text);
    background: color-mix(in srgb, var(--accent) 20%, transparent);
    border-radius: 5px;
    padding: 0.05rem 0.35rem;
  }

  .count svg {
    display: block;
    color: var(--accent);
  }

  /* Stands in for the kebab the summary row does not have, so both columns of
     numbers keep the same right edge as every other row. */
  .menu-space {
    flex: none;
    width: 2.2rem;
  }

  .wall {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.85rem;
    white-space: nowrap;
  }

  /* The day's two figures as one unit, with a divider that has to be visible
     without hovering: a tooltip explains the pair, it cannot separate it. */
  .totals {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }

  .totals .sep {
    color: var(--text-dim);
  }

  /* Leading position, where a disclosure chevron is read as hierarchy. On the
     right it competed with the kebab of ordinary rows and read as a menu. */
  .caret {
    flex: none;
    display: block;
    /* Pinned to the first line, like the twisty of a tree: on a narrow screen
       the row grows to three lines and a centred chevron floats away from the
       title it belongs to. */
    align-self: flex-start;
    margin-top: 0.3rem;
    color: var(--text-dim);
    transition: transform 0.12s ease;
  }

  .caret.open {
    transform: rotate(90deg);
  }

  .group-line:hover .caret {
    color: var(--text);
  }

  /* Day totals like "3h 10m" must never break mid-value. */
  .row > .mono {
    white-space: nowrap;
  }

  @media (max-width: 34rem) {
    form.row {
      flex-wrap: wrap;
    }

    /* Row 2: project + tags share the line; row 3: a full-width Start in the
       thumb zone. */
    form.row :global(.dinput) {
      flex: 1 1 100%;
    }

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

    /* The summary title wraps like every other description below 34rem, instead
       of being the one line in the list that truncates. */
    .desc-static {
      white-space: normal;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      line-clamp: 2;
      -webkit-box-orient: vertical;
    }

    .has-groups .item {
      padding-left: 1.2rem;
    }

    .item.member,
    .has-groups .item.member {
      padding-left: 1.9rem;
    }
  }
</style>
