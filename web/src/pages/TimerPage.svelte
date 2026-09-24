<script lang="ts">
  import { tick, untrack } from "svelte";
  import {
    appState,
    clock,
    entryTags,
    projectByID,
    runningEntries,
    startTimer,
    updateEntries,
    type GroupEntryPatch,
  } from "../lib/state/app.svelte";
  import { deleteEntryWithUndo, stopTimerWithUndo } from "../lib/state/undo.svelte";
  import {
    entryDurationMs,
    displayEntryDescription,
    formatDay,
    formatDuration,
    formatDurationShort,
    formatTime,
    localDateISO,
  } from "../lib/format";
  import {
    groupDayEntries,
    groupTwin,
    snapshotTaskGroup,
    suggestionWindowStart,
    taskSuggestions,
    wallClockMs,
    type TaskGroup,
    type TaskGroupSnapshot,
  } from "../lib/tasks";
  import {
    disposeTrackedLocalMutation,
    observeLocalMutation,
    summarizeMutationMembers,
    type MutationProgress,
  } from "../lib/mutation-progress";
  import { sameStoredRow } from "../lib/db";
  import type { GroupEditSaveResult } from "../lib/group-edit";
  import type { TrackedLocalMutationReceipt } from "../lib/mutation-progress";
  import { syncState } from "../lib/sync.svelte";
  import { feedDays, type FeedDay } from "../lib/feed";
  import { FeedScroller } from "../lib/feed-scroll.svelte";
  import { t } from "../lib/i18n";
  import { maxTextLength } from "../lib/limits";
  import DescriptionInput from "../lib/components/DescriptionInput.svelte";
  import EntryEditor from "../lib/components/EntryEditor.svelte";
  import GroupEditor from "../lib/components/GroupEditor.svelte";
  import PinnedStrip from "../lib/components/PinnedStrip.svelte";
  import DayRuler from "../lib/components/DayRuler.svelte";
  import type { RulerTarget } from "../lib/ruler";
  import EntryProjectMenu from "../lib/components/EntryProjectMenu.svelte";
  import EntryTagsMenu from "../lib/components/EntryTagsMenu.svelte";
  import ProjectSelect from "../lib/components/ProjectSelect.svelte";
  import RowMenu from "../lib/components/RowMenu.svelte";
  import TagChips from "../lib/components/TagChips.svelte";
  import TagSelect from "../lib/components/TagSelect.svelte";
  import type { SyncedRow, TimeEntry } from "../lib/types";

  let description = $state("");
  let selectedProjectID = $state<string | null>(null);
  let selectedTags = $state<string[]>([]);
  let editingID = $state<string | null>(null);
  let editingGroup = $state<TaskGroupSnapshot | null>(null);
  let startForm = $state<HTMLFormElement | null>(null);
  const entryButtonRefs = new Map<string, HTMLButtonElement>();
  const groupEditButtonRefs = new Map<string, HTMLButtonElement>();

  interface GroupMutationResult {
    snapshot: TaskGroupSnapshot;
    patch: GroupEntryPatch;
    receipt: TrackedLocalMutationReceipt;
    progress: MutationProgress;
    expectedRows: SyncedRow[];
  }

  let groupMutationResult = $state<GroupMutationResult | null>(null);
  let retryingGroupMutation = $state(false);
  let stopMutationObserver: (() => void) | null = null;
  let componentActive = true;
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

  // Every finished entry, by day. Only the days around the viewport are in the
  // DOM: the scroller loads older days as the reader approaches the bottom and
  // unmounts the ones several screens away, leaving a gap of their height.
  const days = $derived(feedDays(appState.entries));
  const scroller = new FeedScroller(() => days);
  const mountedDays = $derived(days.slice(scroller.range.first, scroller.range.last + 1));
  const probeDays = $derived(
    scroller.probeRange === null ? [] : days.slice(scroller.probeRange.first, scroller.probeRange.last + 1),
  );
  const currentYear = $derived(Number(todayISO.slice(0, 4)));

  // The day ruler: wide windows with a mouse, and only once there is a day
  // before today to navigate to.
  const RULER_MEDIA = "(min-width: 85rem) and (hover: hover) and (pointer: fine)";
  let rulerRoom = $state(false);
  $effect(() => {
    const query = window.matchMedia(RULER_MEDIA);
    const update = () => (rulerRoom = query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  });
  const showRuler = $derived(rulerRoom && (days.at(-1)?.iso ?? todayISO) < todayISO);

  // Where a jump from the ruler landed: its card flashes a ring for a moment.
  let landedISO = $state<string | null>(null);
  let landedTimer: ReturnType<typeof setTimeout> | undefined;

  // A mouse jump also moves focus to where it landed, as an in-page link does:
  // Tab goes on from the day, the arrow keys scroll the page rather than the
  // ruler, and a field typed in before the jump lets go of the next Space.
  async function jumpTo(target: RulerTarget, takeFocus: boolean): Promise<void> {
    clearTimeout(landedTimer);
    landedISO = null;
    if (target === "top") {
      scroller.revealTop();
      if (takeFocus && document.activeElement instanceof HTMLElement) document.activeElement.blur();
      return;
    }
    await scroller.reveal(target);
    const day = feedElement?.querySelector<HTMLElement>(`:scope > .day[data-key="${target}"]`) ?? null;
    if (takeFocus && day !== null) focusLanded(day);
    // A frame apart, so a second jump to the same day restarts the ring.
    await tick();
    landedISO = target;
    landedTimer = setTimeout(() => (landedISO = null), 1200);
  }

  // WebKit starts Tab over from the top of the page when focus is on an element
  // outside the tab order, so Tab from a landed day is taken to its first
  // control here, as the ruler does after Enter.
  function focusLanded(day: HTMLElement): void {
    if (document.activeElement === day) return;
    const onkeydown = (event: KeyboardEvent) => {
      if (event.key !== "Tab" || event.shiftKey || event.target !== day) return;
      const control = day.querySelector<HTMLElement>(`:is(button, [href], input, [tabindex="0"])`);
      if (control === null) return;
      event.preventDefault();
      control.focus();
    };
    day.addEventListener("keydown", onkeydown);
    day.addEventListener("blur", () => day.removeEventListener("keydown", onkeydown), { once: true });
    day.focus({ preventScroll: true });
  }

  $effect(() => () => clearTimeout(landedTimer));

  let feedElement = $state<HTMLElement | null>(null);
  let sentinelElement = $state<HTMLElement | null>(null);
  let pinnedElement = $state<HTMLElement | null>(null);
  let fullCardElement = $state<HTMLElement | null>(null);
  let stripElement = $state<HTMLElement | null>(null);

  // The running timers exist twice: the full card, which scrolls away with the
  // page and is where they are edited, and the compact strip, pinned while the
  // page is scrolled into the feed. Exactly one is live. On the switch, focus
  // moves to the same control in the other one before this one goes inert -
  // inert would drop it to the page body - and a quick menu left open in the
  // full card closes rather than waiting there for the reader's return.
  $effect(() => {
    const stuck = scroller.stuck;
    const card = fullCardElement;
    const strip = stripElement;
    if (card === null || strip === null) return;
    untrack(() => handOff(stuck, card, strip));
  });

  function handOff(stuck: boolean, card: HTMLElement, strip: HTMLElement): void {
    const from = stuck ? card : strip;
    const to = stuck ? strip : card;
    to.inert = false;
    const active = document.activeElement;
    if (active instanceof HTMLElement && from.contains(active)) twinOf(active, to)?.focus({ preventScroll: true });
    if (stuck) document.dispatchEvent(new CustomEvent("worktime:close-menus", { detail: card }));
    from.inert = true;
  }

  // The same entry and the same role on the other side. A project or tags
  // trigger has no counterpart in the strip, so its row's title stands in; a
  // session whose group is collapsed on the other side is reached through the
  // group's line; a row hidden behind "+N more" through that line.
  function twinOf(control: HTMLElement, target: HTMLElement): HTMLElement | null {
    const key =
      control.closest<HTMLElement>("[data-twin]")?.dataset.twin ??
      control.closest(".item, .group-line")?.querySelector<HTMLElement>("[data-twin]")?.dataset.twin;
    const group = control.closest<HTMLElement>("[data-twin-group]")?.dataset.twinGroup;
    const find = (twin: string | undefined) =>
      twin === undefined ? null : target.querySelector<HTMLElement>(`[data-twin="${CSS.escape(twin)}"]`);
    return find(key) ?? find(group) ?? find("more") ?? target.querySelector<HTMLElement>("button");
  }

  // Without scrolling: the reader may be deep in the feed, and stopping the last
  // timer must not move the page any more than stopping another one does.
  function focusStartForm(): void {
    startForm?.querySelector<HTMLInputElement>("input")?.focus({ preventScroll: true });
  }

  $effect(() => {
    if (feedElement === null) return;
    return scroller.start({
      feed: feedElement,
      sentinel: () => sentinelElement,
      pinned: () => pinnedElement,
      card: () => fullCardElement,
    });
  });

  // New data can move or resize the mounted days; the scroller replans on the
  // next frame. Scheduling writes no state, so this never reruns itself.
  $effect(() => {
    void days;
    scroller.schedule();
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

  function groupFocusKey(dayISO: string, group: TaskGroup): string {
    return `${dayISO}\u0000${group.key}`;
  }

  function registerEntryButton(node: HTMLButtonElement, entryID: string) {
    entryButtonRefs.set(entryID, node);
    return {
      update(nextID: string) {
        entryButtonRefs.delete(entryID);
        entryID = nextID;
        entryButtonRefs.set(entryID, node);
      },
      destroy() {
        entryButtonRefs.delete(entryID);
      },
    };
  }

  function registerGroupEditButton(node: HTMLButtonElement, key: string) {
    groupEditButtonRefs.set(key, node);
    return {
      update(nextKey: string) {
        groupEditButtonRefs.delete(key);
        key = nextKey;
        groupEditButtonRefs.set(key, node);
      },
      destroy() {
        groupEditButtonRefs.delete(key);
      },
    };
  }

  function currentGroupContaining(entryID: string): { key: string; group: TaskGroup } | null {
    for (const group of runningGroups) {
      if (group.entries.some((entry) => entry.id === entryID)) {
        return { key: groupFocusKey("running", group), group };
      }
    }
    // Only mounted days have buttons to focus; grouping the rest would be wasted.
    for (const day of mountedDays) {
      for (const group of groupDayEntries(day.entries)) {
        if (group.entries.some((entry) => entry.id === entryID)) {
          return { key: groupFocusKey(day.iso, group), group };
        }
      }
    }
    return null;
  }

  async function focusEditedMembers(snapshot: TaskGroupSnapshot): Promise<void> {
    await tick();
    let control = editedMemberControl(snapshot);
    if (control === null) {
      // The edited rows may sit in a day the feed has unmounted since.
      const edited = new Set(snapshot.entryIDs);
      const day = days.find((candidate) => candidate.entries.some((entry) => edited.has(entry.id)));
      if (day !== undefined) {
        await scroller.reveal(day.iso);
        control = editedMemberControl(snapshot);
      }
    }
    (control ?? startForm?.querySelector<HTMLInputElement>("input"))?.focus();
  }

  function editedMemberControl(snapshot: TaskGroupSnapshot): HTMLElement | null {
    for (const entryID of snapshot.entryIDs) {
      const current = currentGroupContaining(entryID);
      if (current?.group.entries.length && current.group.entries.length > 1) {
        const trigger = groupEditButtonRefs.get(current.key);
        if (trigger) return trigger;
      }
      const entryButton = entryButtonRefs.get(entryID);
      if (entryButton) return entryButton;
    }
    return null;
  }

  function watchMutation(
    snapshot: TaskGroupSnapshot,
    result: GroupEditSaveResult,
    baseMembers: MutationProgress["members"] = [],
    baseExpectedRows: SyncedRow[] = [],
  ): void {
    if (!componentActive) {
      disposeTrackedLocalMutation(result.receipt);
      return;
    }
    stopMutationObserver?.();
    const retriedIDs = new Set(result.receipt.markers.map((marker) => marker.id));
    const retained = baseMembers.filter((member) => !retriedIDs.has(member.marker.id));
    const expectedRows = [
      ...baseExpectedRows.filter((row) => !retriedIDs.has(row.id)),
      ...result.receipt.rows,
    ];
    const initial = summarizeMutationMembers([
      ...retained,
      ...result.receipt.markers.map((marker) => ({ marker, status: "pending" as const })),
    ]);
    groupMutationResult = { snapshot, patch: result.patch, receipt: result.receipt, progress: initial, expectedRows };
    stopMutationObserver = observeLocalMutation(result.receipt, (progress) => {
      if (groupMutationResult?.receipt.trackingID !== result.receipt.trackingID) return;
      groupMutationResult = {
        ...groupMutationResult,
        progress: summarizeMutationMembers([...retained, ...progress.members]),
      };
    });
  }

  function closeGroupEditor(result: GroupEditSaveResult | null): void {
    const snapshot = editingGroup;
    editingGroup = null;
    if (snapshot && result) {
      watchMutation(snapshot, result);
      void focusEditedMembers(snapshot);
    }
  }

  async function retryGroupMutation(): Promise<void> {
    const current = groupMutationResult;
    if (!current || retryingGroupMutation || current.progress.pending > 0) return;
    retryingGroupMutation = true;
    const operationID = current.receipt.trackingID;
    try {
      const retryableIDs: string[] = [];
      const checkedMembers = current.progress.members.map((member) => {
        if (member.status !== "rejected") return member;
        const expected = current.expectedRows.find((row) => row.id === member.marker.id);
        const latest = appState.entries.find((entry) => entry.id === member.marker.id);
        if (
          expected &&
          latest &&
          latest.updated_at === member.marker.updated_at &&
          sameStoredRow(expected, latest)
        ) {
          retryableIDs.push(member.marker.id);
          return member;
        }
        // The rejected version was superseded after quarantine. Treat it like
        // any other LWW winner: Review may focus it, but Retry must not overwrite it.
        return { ...member, status: "conflict" as const };
      });
      if (groupMutationResult?.receipt.trackingID !== operationID) return;
      groupMutationResult = {
        ...groupMutationResult,
        progress: summarizeMutationMembers(checkedMembers),
      };
      if (retryableIDs.length === 0) return;
      const receipt = await updateEntries(retryableIDs, current.patch);
      if (!componentActive || groupMutationResult?.receipt.trackingID !== operationID) {
        disposeTrackedLocalMutation(receipt);
        return;
      }
      watchMutation(
        current.snapshot,
        { receipt, patch: current.patch },
        checkedMembers,
        current.expectedRows,
      );
    } catch (error) {
      console.error("group edit retry failed", error);
    } finally {
      retryingGroupMutation = false;
    }
  }

  function dismissGroupMutation(): void {
    stopMutationObserver?.();
    stopMutationObserver = null;
    groupMutationResult = null;
  }

  $effect(() => () => {
    componentActive = false;
    stopMutationObserver?.();
    stopMutationObserver = null;
  });

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
  <div class="row item" class:member data-anchor>
    <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
    <span class="main">
      <button
        type="button"
        class="desc"
        data-twin={entry.stopped_at === null ? `title:${entry.id}` : undefined}
        use:registerEntryButton={entry.id}
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
      <button data-twin="stop:{entry.id}" onclick={() => void stopTimerWithUndo(entry)}>{t("Stop")}</button>
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
  {@const editLabel = t("Edit group {task}, {n} entries", {
    task: displayedGroupDescription || t("(no description)"),
    n: group.entries.length,
  })}
  <!-- A running group is already this task, right now: repeating it would only
       add a second timer for the same work. -->
  {@const repeatable = group.lastStoppedAt !== null}
  <div class="row group-line" class:open={shown} data-anchor>
    <button
      type="button"
      class="row group-row"
      aria-expanded={shown}
      {...shown ? { "aria-controls": listID } : {}}
      data-twin={dayISO === "running" ? groupTwin(group.key) : undefined}
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
    <span class="group-actions">
      <button
        type="button"
        class="kebab icon edit-group"
        aria-label={editLabel}
        title={editLabel}
        use:registerGroupEditButton={groupFocusKey(dayISO, group)}
        onclick={() => (editingGroup = snapshotTaskGroup(group))}
      >
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L8 18l-4 1 1-4Z" />
        </svg>
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
      {/if}
    </span>
  </div>
  {#if shown}
    <div class="members" id={listID} data-twin-group={dayISO === "running" ? groupTwin(group.key) : undefined}>
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

<form class="card row" bind:this={startForm} onsubmit={submitStart}>
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

<!-- The running timers: the full card scrolls with the page, and once its last
     strip-sized slice passes under the top edge the compact strip takes over. -->
{#if running.length > 0}
  <div
    class="card running-full"
    class:stuck={scroller.stuck}
    class:has-groups={runningGroups.some((group) => group.entries.length > 1)}
    bind:this={fullCardElement}
  >
    <h3>{t("Running")}</h3>
    {#each runningGroups as group, index (group.key)}
      {#if group.entries.length === 1}
        {@render entryRow(group.entries[0]!, false)}
      {:else}
        {@render groupRow("running", group, index)}
      {/if}
    {/each}
  </div>
  <!-- Marks the strip's box in the flow: once the box is further down the
       screen than this marker, it is stuck to the top. -->
  <div bind:this={sentinelElement} aria-hidden="true"></div>
  <PinnedStrip
    groups={runningGroups}
    now={clock.now}
    stuck={scroller.stuck}
    onedit={(entryID) => (editingID = entryID)}
    onstop={stopTimerWithUndo}
    onempty={focusStartForm}
    bind:box={pinnedElement}
    bind:section={stripElement}
  />
{/if}

{#if showRuler}
  <DayRuler
    {days}
    timeOff={appState.timeOff}
    {todayISO}
    visible={scroller.visible}
    scrolledAt={() => scroller.scrolledAt}
    onjump={jumpTo}
  />
{/if}

<!-- One day of the feed, in a wrapper that is measured as a whole, margin
     included. -->
{#snippet dayCard(day: FeedDay)}
  <!-- Every entry here is finished, so neither groupDayEntries nor wallClockMs
       reads `now` - passing clock.now would subscribe every mounted day to the
       one-second ticker for nothing. -->
  {@const groups = groupDayEntries(day.entries)}
  {@const tracked = dayTotal(groups)}
  {@const wall = wallClockMs(day.entries, 0)}
  <div class="day" class:landed={landedISO === day.iso} data-key={day.iso} tabindex="-1" {@attach scroller.observeDay}>
    <div class="card" class:has-groups={groups.some((group) => group.entries.length > 1)}>
      <div class="row" data-anchor>
        <h3>{formatDay(day.entries[0]!.started_at, currentYear)}</h3>
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
          {@render groupRow(day.iso, group, index)}
        {/if}
      {/each}
    </div>
  </div>
{/snippet}

<!-- The feed keeps its own scroll anchoring (see feed-scroll.svelte.ts), so the
     browser's is switched off inside it. The gaps stand in for the days that are
     not mounted; a probe - stale days above the window, mounted for one frame to
     be measured again - splits the top gap in two. data-measuring is up while any
     such day is left, which the tests wait on. -->
<div
  class="feed"
  class:below-running={running.length > 0}
  bind:this={feedElement}
  data-measuring={scroller.measuring ? "" : undefined}
>
  <div class="feed-gap" style:height="{scroller.gapAbove}px"></div>
  {#each probeDays as day (day.iso)}
    {@render dayCard(day)}
  {/each}
  <div class="feed-gap" style:height="{scroller.gapTop}px"></div>
  {#each mountedDays as day (day.iso)}
    {@render dayCard(day)}
  {/each}
  <div class="feed-gap" style:height="{scroller.gapBottom}px"></div>
</div>

{#if running.length === 0 && days.length === 0}
  <p class="muted">{t("No entries yet. Start your first timer above.")}</p>
{/if}

{#if editingID !== null}
  <EntryEditor entryID={editingID} onclose={() => (editingID = null)} />
{/if}

{#if editingGroup !== null}
  <GroupEditor snapshot={editingGroup} onclose={closeGroupEditor} />
{/if}

{#if groupMutationResult}
  <div class="toast group-sync-result">
    <span role="status" aria-live="polite">
      {t("{done}/{total} group entries synced", {
        done: groupMutationResult.progress.accepted,
        total: groupMutationResult.progress.total,
      })}
      {#if groupMutationResult.progress.pending > 0}
        · {t("{n} pending", { n: groupMutationResult.progress.pending })}
        {#if syncState.status === "offline"} ({t("offline")}){/if}
      {/if}
      {#if groupMutationResult.progress.rejected > 0}
        · {t("{n} rejected", { n: groupMutationResult.progress.rejected })}
      {/if}
      {#if groupMutationResult.progress.conflict > 0}
        · {t("{n} changed elsewhere", { n: groupMutationResult.progress.conflict })}
      {/if}
    </span>
    {#if groupMutationResult.progress.rejected > 0}
      <a href="#/settings">{t("Sync details")}</a>
      <button type="button" disabled={groupMutationResult.progress.pending > 0 || retryingGroupMutation} onclick={() => void retryGroupMutation()}>{t("Retry")}</button>
    {/if}
    {#if groupMutationResult.progress.conflict > 0}
      <button type="button" onclick={() => void focusEditedMembers(groupMutationResult!.snapshot)}>{t("Review entries")}</button>
    {/if}
    <button type="button" disabled={retryingGroupMutation} onclick={dismissGroupMutation}>{t("Dismiss")}</button>
  </div>
{/if}

<style>
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
  }

  /* The editing surface for running timers, uncapped and in the flow. The gap
     below it belongs to the feed, so the strip's box - right after this card -
     sits exactly at the card's bottom edge. While the strip is pinned the card
     is hidden, but only once the strip has faded in over it. */
  .running-full {
    margin-bottom: 0;
    overflow-anchor: none;
  }

  .running-full.stuck {
    visibility: hidden;
    transition: visibility 0s linear 120ms;
  }

  @media (prefers-reduced-motion: reduce) {
    .running-full.stuck {
      transition: none;
    }
  }

  .feed {
    overflow-anchor: none;
  }

  .feed.below-running {
    margin-top: 1rem;
  }

  /* A block formatting context keeps the card's bottom margin inside the wrapper,
     so the measured height is exactly the space the day takes up. */
  .day {
    display: flow-root;
  }

  /* Focused only as the place a jump landed, which its ring already shows. */
  .day:focus {
    outline: none;
  }

  /* Where a jump from the day ruler landed: a ring that fades on the card. */
  .day.landed > .card {
    animation: landed 1.2s ease-out;
  }

  @keyframes landed {
    from {
      box-shadow: 0 0 0 2px var(--accent);
    }

    to {
      box-shadow: 0 0 0 2px transparent;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .day.landed > .card {
      animation: none;
      box-shadow: 0 0 0 2px var(--accent);
    }
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

  .group-actions {
    display: inline-flex;
    align-items: center;
    flex: none;
  }

  .group-sync-result {
    bottom: calc(4.9rem + env(safe-area-inset-bottom));
    flex-wrap: wrap;
    z-index: 21;
  }

  .group-sync-result > span {
    min-width: 12rem;
    flex: 1;
  }

  .group-sync-result a {
    color: var(--accent);
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
