<script lang="ts">
  // Entry editor: a native <dialog> opened with showModal() - bottom sheet
  // below 34rem, centred card above, focus trap and Escape from the browser.
  //
  // Save patches, it never writes its snapshot: updateEntry(id, patch) merges
  // only the fields the user touched into whatever the row looks like at save
  // time, so a concurrent Stop (other device, MCP) is not undone and tags
  // written by another writer are not blanked.
  import { appState, clock, entryTags, projectByID, updateEntry } from "../state/app.svelte";
  import { formatDurationShort, formatTime, localDateISO } from "../format";
  import { t } from "../i18n";
  import { maxTextLength } from "../limits";
  import { suggestionWindowStart, taskSuggestions } from "../tasks";
  import type { TimeEntry } from "../types";
  import {
    calendarDayDiff,
    composeTimestamp,
    formatTimeOfDay,
    nudgeTimeText,
    parseTimeText,
    shiftDateISO,
    timeOfDayFromMs,
  } from "../time-input";
  import DescriptionInput from "./DescriptionInput.svelte";
  import ProjectSelect from "./ProjectSelect.svelte";
  import TagPicker from "./TagPicker.svelte";

  interface Props {
    entryID: string;
    onclose: () => void;
  }

  let { entryID, onclose }: Props = $props();

  // Snapshot at open: the patch is computed against this, so fields the user
  // never touched are absent from the patch and remote edits to them survive.
  const original = $state.snapshot(appState.entries.find((entry) => entry.id === entryID)!) as TimeEntry;
  const originalTags = entryTags(original);
  const originalOffset =
    original.stopped_at === null ? 0 : calendarDayDiff(localDateISO(original.started_at), localDateISO(original.stopped_at));
  const wasRunning = original.stopped_at === null;

  const current = $derived(appState.entries.find((entry) => entry.id === entryID));
  const isRunning = $derived(current !== undefined && current.stopped_at === null);
  const agentSessionID = $derived(current?.agent_session_id ?? original.agent_session_id ?? null);

  let draftDescription = $state(original.description);
  let draftProjectID = $state(original.project_id);
  let draftTags = $state(entryTags(original));
  let dateISO = $state(localDateISO(original.started_at));
  let fromText = $state(formatTimeOfDay(timeOfDayFromMs(original.started_at)));
  let toText = $state(original.stopped_at === null ? "" : formatTimeOfDay(timeOfDayFromMs(original.stopped_at)));
  let endDayOffset = $state(originalOffset);

  let dateTouched = $state(false);
  let fromTouched = $state(false);
  let toTouched = $state(false);
  let offsetTouched = $state(false);
  let stoppedRemotely = $state(false);

  let dialogElement = $state<HTMLDialogElement | null>(null);

  $effect(() => {
    if (dialogElement && !dialogElement.open) dialogElement.showModal();
  });

  // The row can be stopped by another device or MCP while this dialog is open.
  // The editor swaps to the finished layout showing the stored end time; it
  // must not keep its running-entry snapshot and save that.
  $effect(() => {
    const stoppedAt = current?.stopped_at ?? null;
    if (!wasRunning || stoppedAt === null) return;
    stoppedRemotely = true;
    if (!toTouched && !offsetTouched) {
      toText = formatTimeOfDay(timeOfDayFromMs(stoppedAt));
      setEndDayOffset(calendarDayDiff(dateISO, localDateISO(stoppedAt)));
    }
  });

  // Deleted underneath (another device, or the undo window elsewhere): nothing
  // left to edit, close rather than let Save resurrect the row.
  $effect(() => {
    if (current === undefined) dialogElement?.close();
  });

  const fromTime = $derived(parseTimeText(fromText));
  const fromInvalid = $derived(fromTime === null);
  const toInvalid = $derived.by(() => {
    if (toText.trim() === "") return !isRunning;
    return parseTimeText(toText) === null;
  });

  const startMs = $derived(fromTime === null ? null : composeTimestamp(dateISO, fromTime));
  const endMs = $derived.by(() => {
    const time = toText.trim() === "" ? null : parseTimeText(toText);
    if (time === null) return null;
    return composeTimestamp(dateISO, time, endDayOffset);
  });

  const orderInvalid = $derived(startMs !== null && endMs !== null && endMs < startMs);
  const canSave = $derived(!fromInvalid && !toInvalid && !orderInvalid && current !== undefined);

  const durationLabel = $derived.by(() => {
    if (startMs === null) return "—";
    if (toText.trim() === "" && isRunning) return formatDurationShort(clock.now - startMs);
    if (endMs === null || endMs < startMs) return "—";
    return formatDurationShort(endMs - startMs);
  });

  const longEntry = $derived(startMs !== null && endMs !== null && endMs - startMs > 12 * 3_600_000);

  // Boundary line: the reason to edit is almost always a boundary, so the
  // neighbours' edges and the live gap/overlap are shown inside the dialog.
  // Narrowed by timestamp and keyed on the date alone, so typing in the From field does
  // not rescan the table - and building a Date per row to compare formatted strings is
  // not how a day is selected out of ten thousand entries.
  const dayEntries = $derived.by(() => {
    // Both bounds go through composeTimestamp: a DST day is 23 or 25 hours long, so
    // adding a fixed 86_400_000 would clip an hour off one day a year.
    const dayStart = composeTimestamp(dateISO, { hour: 0, minute: 0 });
    const dayEnd = composeTimestamp(dateISO, { hour: 0, minute: 0 }, 1);
    return appState.entries.filter(
      (entry) =>
        entry.id !== entryID &&
        entry.stopped_at !== null &&
        entry.started_at >= dayStart &&
        entry.started_at < dayEnd,
    );
  });

  const neighbours = $derived.by(() => {
    if (startMs === null) return null;
    let prev: TimeEntry | null = null;
    let next: TimeEntry | null = null;
    for (const entry of dayEntries) {
      if (entry.started_at < startMs) {
        if (prev === null || entry.stopped_at! > prev.stopped_at!) prev = entry;
      } else if (next === null || entry.started_at < next.started_at) {
        next = entry;
      }
    }
    if (prev === null && next === null) return null;
    const referenceEnd = endMs ?? clock.now;
    const prevDelta = prev === null ? 0 : startMs - prev.stopped_at!;
    const nextDelta = next === null ? 0 : next.started_at - referenceEnd;
    const asMinutes = (ms: number) => Math.round(ms / 60_000);
    return {
      prevEnd: prev === null ? null : formatTime(prev.stopped_at!),
      prevGapMin: Math.max(0, asMinutes(prevDelta)),
      prevOverlapMin: Math.max(0, asMinutes(-prevDelta)),
      nextStart: next === null ? null : formatTime(next.started_at),
      nextGapMin: Math.max(0, asMinutes(nextDelta)),
      nextOverlapMin: Math.max(0, asMinutes(-nextDelta)),
    };
  });

  function fmtMin(minutes: number): string {
    return formatDurationShort(minutes * 60_000);
  }

  function shiftDate(deltaDays: number) {
    dateISO = shiftDateISO(dateISO, deltaDays);
    dateTouched = true;
  }

  // The offset says how many days after the start date the entry ends, so it is never
  // negative. Every write goes through here: calendarDayDiff against a stored end can
  // return -1 once the date has been shifted forward, and that renders as "+-1d", puts
  // the end before the start, disables Save and disables the − button that would undo
  // it - leaving the dialog with no obvious way out.
  function setEndDayOffset(value: number) {
    endDayOffset = Math.max(0, value);
  }

  function shiftEndDay(delta: number) {
    setEndDayOffset(endDayOffset + delta);
    offsetTouched = true;
  }

  function normaliseFrom() {
    const time = parseTimeText(fromText);
    if (time === null) return;
    // Round-trip through the timestamp so a value in a DST gap is written back
    // as the time that will actually be stored.
    fromText = formatTimeOfDay(timeOfDayFromMs(composeTimestamp(dateISO, time)));
  }

  function normaliseTo() {
    if (toText.trim() === "") {
      if (isRunning) return;
      // To can never be cleared: toReportEntries skips stopped_at === null, so
      // a cleared To would silently drop the hours from every report. Restore.
      const stored = current?.stopped_at ?? original.stopped_at;
      if (stored !== null && stored !== undefined) {
        toText = formatTimeOfDay(timeOfDayFromMs(stored));
        setEndDayOffset(calendarDayDiff(dateISO, localDateISO(stored)));
        toTouched = false;
        offsetTouched = false;
      }
      return;
    }
    const time = parseTimeText(toText);
    if (time === null) return;
    // An end typed before the start means the entry crosses midnight: bump the
    // end-day offset instead of leaving the entry unsaveable.
    if (fromTime !== null && endDayOffset === 0 && composeTimestamp(dateISO, time) < composeTimestamp(dateISO, fromTime)) {
      endDayOffset = 1;
    }
    toText = formatTimeOfDay(timeOfDayFromMs(composeTimestamp(dateISO, time, endDayOffset)));
  }

  function nudge(event: KeyboardEvent, field: "from" | "to") {
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    const delta = (event.key === "ArrowUp" ? 1 : -1) * (event.shiftKey ? 15 : 1);
    if (field === "from") {
      const nudged = nudgeTimeText(fromText, delta);
      if (nudged !== null) {
        fromText = nudged;
        fromTouched = true;
      }
    } else {
      const nudged = nudgeTimeText(toText, delta);
      if (nudged !== null) {
        toText = nudged;
        toTouched = true;
      }
    }
  }

  function stopNow() {
    const now = Date.now();
    toText = formatTimeOfDay(timeOfDayFromMs(now));
    setEndDayOffset(calendarDayDiff(dateISO, localDateISO(now)));
    toTouched = true;
  }

  function sameTags(left: string[], right: string[]): boolean {
    return left.length === right.length && left.every((tag, index) => tag === right[index]);
  }

  async function save() {
    if (!canSave || current === undefined || fromTime === null) return;
    // A future start on a running entry reads as a broken timer and produces
    // stopped_at < started_at on the next Stop, so it is clamped to now.
    let newStart = composeTimestamp(dateISO, fromTime);
    if (isRunning && newStart > Date.now()) newStart = Date.now();

    const patch: Partial<TimeEntry> = {};
    if (draftDescription !== original.description) patch.description = draftDescription;
    if (draftProjectID !== original.project_id) patch.project_id = draftProjectID;
    if (!sameTags(draftTags, originalTags)) patch.tags = [...draftTags];
    if ((dateTouched || fromTouched) && newStart !== original.started_at) patch.started_at = newStart;
    if ((dateTouched || toTouched || offsetTouched) && endMs !== null && endMs !== original.stopped_at) {
      patch.stopped_at = endMs;
      // The fields are minute-precision but the stored start keeps its seconds,
      // so an end typed at the start's own minute would land seconds before it
      // and the server rejects stopped_at < started_at for the whole batch.
      const effectiveStart = patch.started_at ?? current.started_at;
      if (patch.stopped_at < effectiveStart) patch.stopped_at = effectiveStart;
    }
    if (Object.keys(patch).length > 0) await updateEntry(entryID, patch);
    dialogElement?.close();
  }

  const activeProjects = $derived(
    appState.projects.filter((project) => !project.archived).sort((a, b) => a.name.localeCompare(b.name)),
  );

  // Renaming an entry to an existing task is what puts it into that task's
  // group, so the same suggestions belong here - and from the same window the Timer
  // page uses. The source is narrowed in its own $derived so that typing rebuilds only
  // the suggestion list, not a copy of the entry table, and the cutoff is a day-level
  // value so the one-second ticker never enters the picture.
  const suggestionCutoff = $derived(suggestionWindowStart(clock.now));
  const suggestionSource = $derived(
    appState.entries.filter(
      (entry) => entry.id !== entryID && (entry.stopped_at === null || entry.started_at >= suggestionCutoff),
    ),
  );
  const suggestions = $derived(taskSuggestions(suggestionSource, draftDescription, suggestionCutoff));

  function projectName(projectID: string | null): string {
    return projectByID(projectID)?.name ?? "";
  }

  function applySuggestion(suggestion: { projectID: string | null; tags: string[] }) {
    if (draftProjectID === null) draftProjectID = suggestion.projectID;
    if (draftTags.length === 0) draftTags = [...suggestion.tags];
  }
</script>

<dialog
  class="sheet"
  bind:this={dialogElement}
  aria-labelledby="ed-title"
  onclose={() => onclose()}
  onclick={(event) => {
    if (event.target === dialogElement) dialogElement?.close();
  }}
>
  <form
    method="dialog"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <div class="ed-head">
      <h3 id="ed-title">{t("Edit entry")}</h3>
      <span class="spacer"></span>
      <span class="ed-calc">{durationLabel}</span>
    </div>

    <div class="ed-body">
      {#if neighbours}
        <p class="ed-neighbours">
          {#if neighbours.prevEnd}
            <span>{t("prev ends")} <b>{neighbours.prevEnd}</b></span>
            {#if neighbours.prevOverlapMin > 0}
              <span class="gap bad">{t("overlaps {n}", { n: fmtMin(neighbours.prevOverlapMin) })}</span>
            {:else if neighbours.prevGapMin > 0}
              <span class="gap">{t("gap {n}", { n: fmtMin(neighbours.prevGapMin) })}</span>
            {/if}
          {/if}
          {#if neighbours.nextStart}
            {#if neighbours.nextOverlapMin > 0}
              <span class="gap bad">{t("overlaps {n}", { n: fmtMin(neighbours.nextOverlapMin) })}</span>
            {:else if neighbours.nextGapMin > 0}
              <span class="gap">{t("gap {n}", { n: fmtMin(neighbours.nextGapMin) })}</span>
            {/if}
            <span>{t("next starts")} <b>{neighbours.nextStart}</b></span>
          {/if}
        </p>
      {/if}

      {#if stoppedRemotely}
        <p class="ed-hint bad">{t("This entry was stopped on another device; the end time below is the stored one.")}</p>
      {/if}

      {#if agentSessionID}
        <div class="ed-field">
          <span class="ed-label">{t("Session identifier")}</span>
          <output class="ed-session-id mono" aria-label={t("Session identifier")}>{agentSessionID}</output>
        </div>
      {/if}

      <div class="ed-field">
        <label class="ed-label" for="ed-desc">{t("Description")}</label>
        <DescriptionInput
          id="ed-desc"
          bind:value={draftDescription}
          {suggestions}
          {projectName}
          onpick={applySuggestion}
          maxlength={maxTextLength}
        />
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Project")}</span>
        <div><ProjectSelect projects={activeProjects} bind:value={draftProjectID} /></div>
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Tags")} <span class="muted">{draftTags.length}/8</span></span>
        <TagPicker selected={draftTags} onchange={(tags) => (draftTags = tags)} />
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Date")}</span>
        <div class="ed-line">
          <input type="date" bind:value={dateISO} aria-label={t("Date")} onchange={() => (dateTouched = true)} />
          <span class="seg">
            <button type="button" onclick={() => shiftDate(-1)}>−1d</button>
            <button type="button" onclick={() => shiftDate(1)}>+1d</button>
          </span>
        </div>
      </div>

      <div class="ed-field">
        <span class="ed-label">{t("Time")} <span class="muted">24h</span></span>
        <div class="ed-line">
          <label class="muted" for="ed-from">{t("From")}</label>
          <input
            id="ed-from"
            class="timefield"
            inputmode="numeric"
            maxlength="5"
            bind:value={fromText}
            aria-invalid={fromInvalid}
            oninput={() => (fromTouched = true)}
            onblur={normaliseFrom}
            onkeydown={(event) => nudge(event, "from")}
          />

          <label class="muted" for="ed-to">{t("To")}</label>
          <input
            id="ed-to"
            class="timefield"
            inputmode="numeric"
            maxlength="5"
            placeholder={isRunning ? t("running") : ""}
            bind:value={toText}
            aria-invalid={toInvalid}
            oninput={() => (toTouched = true)}
            onblur={normaliseTo}
            onkeydown={(event) => nudge(event, "to")}
          />

          <!-- End-day offset: what makes a 23:40-00:20 entry representable
               without writing stopped_at < started_at, which the server
               rejects for the whole batch. -->
          <span class="seg" aria-label={t("End day offset")}>
            <button type="button" onclick={() => shiftEndDay(-1)} disabled={endDayOffset <= 0}>−</button>
            <button type="button" class:on={endDayOffset > 0} tabindex={-1}>+{endDayOffset}d</button>
            <button type="button" onclick={() => shiftEndDay(1)}>+</button>
          </span>
        </div>

        {#if isRunning}
          <p class="ed-hint">{t("Entering an end time stops this timer at that moment. Start cannot be in the future.")}</p>
        {/if}
        {#if orderInvalid}
          <p class="ed-hint bad">{t("End is before start - check the +Nd offset.")}</p>
        {:else if longEntry}
          <p class="ed-hint">{t("This entry is longer than 12 hours - check the +Nd offset.")}</p>
        {/if}
      </div>
    </div>

    <div class="ed-foot">
      {#if isRunning}
        <button type="button" onclick={stopNow}>{t("Stop now")}</button>
      {/if}
      <span class="spacer"></span>
      <button type="button" onclick={() => dialogElement?.close()}>{t("Cancel")}</button>
      <button type="submit" class="primary" disabled={!canSave}>{t("Save")}</button>
    </div>
  </form>
</dialog>
