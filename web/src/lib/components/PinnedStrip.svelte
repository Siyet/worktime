<script lang="ts">
  // The running timers while the Timer page is scrolled into the feed: one fixed
  // line per timer instead of the full card, which scrolls away like any other.
  //
  // The layout is built so that nothing here can ever move the feed. The sticky
  // box has no height, and the strip is drawn above it: the box sits right after
  // the full card, so it sticks at the moment the card's last strip-sized slice
  // passes under the top edge, and the strip then covers exactly that slice. Its
  // height is arithmetic - a fixed line height times the line count - so the box
  // can stick at the strip's bottom edge without measuring anything. Expanding
  // "+N more" or a group opens an overlay below the strip and never changes it.
  import { tick } from "svelte";
  import { displayEntryDescription, entryDurationMs, formatDuration, formatTime, sessionTag } from "../format";
  import { t } from "../i18n";
  import { projectByID } from "../state/app.svelte";
  import { groupTwin, type TaskGroup } from "../tasks";
  import type { TimeEntry } from "../types";

  interface Props {
    groups: TaskGroup[];
    now: number;
    stuck: boolean;
    onedit: (entryID: string) => void;
    onstop: (entry: TimeEntry) => Promise<void>;
    /** Where focus goes when a keyboard Stop took the last timer away. */
    onempty: () => void;
    box?: HTMLElement | null;
    section?: HTMLElement | null;
  }

  let { groups, now, stuck, onedit, onstop, onempty, box = $bindable(null), section = $bindable(null) }: Props = $props();

  const LATE_MS = 8 * 3_600_000;
  const uid = $props.id();

  // How many lines the strip may take: few on a short screen, more where a mouse
  // leaves room. Read from 100svh, which mobile toolbars do not change, and only
  // re-read when the width changes (a rotation) or with a mouse - never while a
  // phone's toolbar collapses mid-scroll, which would resize the strip.
  let cap = $state(4);
  let widthTick = $state(0);

  $effect(() => {
    let lastWidth = window.innerWidth;
    cap = measureCap();
    const onResize = (): void => {
      const coarse = window.matchMedia("(pointer: coarse)").matches;
      if (coarse && window.innerWidth === lastWidth) return;
      lastWidth = window.innerWidth;
      cap = measureCap();
      widthTick += 1;
    };
    window.addEventListener("resize", onResize);
    window.addEventListener("orientationchange", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("orientationchange", onResize);
    };
  });

  function measureCap(): number {
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    const probe = document.createElement("div");
    probe.style.cssText = "position:fixed;top:0;left:0;width:0;height:100svh;visibility:hidden;pointer-events:none";
    document.body.append(probe);
    const height = probe.getBoundingClientRect().height || window.innerHeight;
    probe.remove();
    if (height < 30 * rem) return 2;
    return !window.matchMedia("(pointer: coarse)").matches && height >= 50 * rem ? 6 : 4;
  }

  // Up to the cap every row gets a line; above it the last line says how many
  // more there are. The rows behind it are the longest-running ones.
  const overflow = $derived(groups.length > cap);
  const visible = $derived(overflow ? groups.slice(0, cap - 1) : groups);
  const hidden = $derived(overflow ? groups.slice(cap - 1) : []);
  const lines = $derived(visible.length + (overflow ? 1 : 0));
  const hiddenEntries = $derived(hidden.flatMap((group) => group.entries));
  const hiddenLate = $derived(hidden.filter((group) => groupLate(group)).length);

  function late(entry: TimeEntry): boolean {
    return entryDurationMs(entry, now) >= LATE_MS;
  }

  function groupLate(group: TaskGroup): boolean {
    return group.entries.some((entry) => late(entry));
  }

  function title(entry: TimeEntry): string {
    return displayEntryDescription(entry) || t("(no description)");
  }

  // One overlay at a time: "more", or the key of an expanded group.
  let openKey = $state<string | null>(null);

  $effect(() => {
    if (!stuck) openKey = null;
  });

  function toggle(key: string): void {
    openKey = openKey === key ? null : key;
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== "Escape" || openKey === null) return;
    event.preventDefault();
    const key = openKey;
    openKey = null;
    const twin = key === "more" ? "more" : groupTwin(key);
    section?.querySelector<HTMLElement>(`[data-twin="${CSS.escape(twin)}"]`)?.focus({ preventScroll: true });
  }

  function onDocumentClick(event: MouseEvent): void {
    if (openKey !== null && section !== null && !event.composedPath().includes(section)) openKey = null;
  }

  // Two lines whose titles are cut to the same visible text cannot be told apart,
  // so those - and only those - carry the session tag or the start time. The
  // comparison uses the whole title cell, which the suffix never changes, so
  // adding one cannot change the outcome and loop.
  let suffixed = $state(new Set<string>());
  let measureCanvas: HTMLCanvasElement | null = null;

  $effect(() => {
    void widthTick;
    // The "+N more" overlay's lines sit right under the strip's, so they are
    // compared with them while it is open.
    const listed = openKey === "more" ? groups : visible;
    const singles = listed.filter((group) => group.entries.length === 1).map((group) => group.entries[0]!);
    if (section === null || singles.length < 2) {
      if (suffixed.size > 0) suffixed = new Set();
      return;
    }
    const byText = new Map<string, string[]>();
    for (const entry of singles) {
      const text = section.querySelector<HTMLElement>(`[data-twin="title:${CSS.escape(entry.id)}"] .pin-text`);
      if (text === null) continue;
      const cell = text.parentElement as HTMLElement;
      const shown = fittedText(title(entry), getComputedStyle(text).font, cell.clientWidth);
      byText.set(shown, [...(byText.get(shown) ?? []), entry.id]);
    }
    const next = new Set([...byText.values()].filter((ids) => ids.length > 1).flat());
    if (next.size !== suffixed.size || [...next].some((id) => !suffixed.has(id))) suffixed = next;
  });

  function fittedText(text: string, font: string, width: number): string {
    measureCanvas ??= document.createElement("canvas");
    const context = measureCanvas.getContext("2d");
    if (context === null) return text;
    context.font = font;
    if (context.measureText(text).width <= width) return text;
    let low = 0;
    let high = text.length;
    while (low < high) {
      const middle = Math.ceil((low + high) / 2);
      if (context.measureText(text.slice(0, middle) + "…").width <= width) low = middle;
      else high = middle - 1;
    }
    return text.slice(0, low);
  }

  function suffix(entry: TimeEntry): string {
    return entry.agent_session_id ? `#${sessionTag(entry.agent_session_id)}` : formatTime(entry.started_at);
  }

  // Stop from the keyboard or a screen reader: focus goes to whatever took the
  // stopped line's place, never to the page body.
  async function stop(entry: TimeEntry, event: MouseEvent): Promise<void> {
    const fromKeyboard = event.detail === 0;
    // Held on to: stopping the last timer unmounts the strip, and the binding
    // goes null with it.
    const strip = section;
    const overlay = (event.currentTarget as HTMLElement).closest<HTMLElement>("[data-pin-scroll]");
    const line = (event.currentTarget as HTMLElement).closest("li");
    const lineIndex = line === null ? -1 : [...(line.parentElement?.children ?? [])].indexOf(line);
    await onstop(entry);
    if (!fromKeyboard || strip === null) return;
    await tick();
    if (!strip.isConnected) {
      onempty();
      return;
    }
    if (overlay !== null && overlay.isConnected) {
      const next = overlay.querySelector<HTMLElement>(".pin-stop");
      if (next !== null) {
        next.focus({ preventScroll: true });
        return;
      }
    }
    const items = [...strip.querySelectorAll<HTMLElement>(":scope > ul > li")];
    const target = items[lineIndex] ?? items[lineIndex - 1] ?? items.at(-1);
    const control =
      target?.querySelector<HTMLElement>(":scope > .pin-stop, :scope > .pin-toggle") ??
      strip.querySelector<HTMLElement>(".pin-more");
    if (control) control.focus({ preventScroll: true });
    else onempty();
  }
</script>

<svelte:document onclick={onDocumentClick} onkeydown={onKeydown} />

{#snippet entryLine(entry: TimeEntry, label: string, showDot: boolean)}
  {@const project = projectByID(entry.project_id)}
  {@const titleID = `${uid}-t-${entry.id}`}
  <li class="pin-line">
    <span class="dot" style="background: {showDot ? (project?.color ?? 'var(--border)') : 'transparent'}"></span>
    <button
      type="button"
      class="pin-title"
      id={titleID}
      title={label}
      data-twin="title:{entry.id}"
      onclick={() => onedit(entry.id)}
    >
      <span class="pin-text">{label}</span>
      {#if project}<span class="pin-project" aria-hidden="true">{project.name}</span>{/if}
      {#if suffixed.has(entry.id)}<span class="pin-suffix mono">{suffix(entry)}</span>{/if}
      <span class="sr-only">, {project?.name ?? t("No project")}</span>
    </button>
    <span class="pin-time mono" class:late={late(entry)}>{formatDuration(entryDurationMs(entry, now))}</span>
    <button
      type="button"
      class="pin-stop"
      aria-label={t("Stop")}
      aria-describedby={titleID}
      data-twin="stop:{entry.id}"
      onclick={(event) => void stop(entry, event)}
    >
      <span class="face" aria-hidden="true"><span class="glyph"></span></span>
    </button>
  </li>
{/snippet}

<!-- A running group's sessions, one line each, told apart by their session tag
     or start time. -->
{#snippet sessions(group: TaskGroup)}
  {#each group.entries as entry (entry.id)}
    {@render entryLine(entry, entry.agent_session_id ? `#${sessionTag(entry.agent_session_id)}` : formatTime(entry.started_at), false)}
  {/each}
{/snippet}

{#snippet groupLine(group: TaskGroup, listID: string)}
  {@const project = projectByID(group.projectID)}
  {@const label = displayEntryDescription(group.entries[0]!) || t("(no description)")}
  {@const shown = openKey === group.key}
  <li class="pin-line group" class:open={shown}>
    <button
      type="button"
      class="pin-toggle"
      aria-expanded={shown}
      aria-controls={listID}
      data-twin={groupTwin(group.key)}
      onclick={() => toggle(group.key)}
    >
      <span class="dot" style="background: {project?.color ?? 'var(--border)'}"></span>
      <span class="pin-title-static">
        <span class="pin-text">{label}</span>
        <span class="count mono" aria-hidden="true">≡ {group.entries.length}</span>
        <span class="sr-only">, {project?.name ?? t("No project")}, {t("{n} entries", { n: group.entries.length })}, {shown ? t("Collapse") : t("Expand")}</span>
      </span>
      <span class="pin-time mono" class:late={groupLate(group)}>{formatDuration(group.totalMs)}</span>
      <span class="chevron" class:open={shown} aria-hidden="true">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6" /></svg>
      </span>
    </button>
    {#if shown}
      <ul class="pin-overlay" id={listID} data-pin-scroll>
        {@render sessions(group)}
      </ul>
    {/if}
  </li>
{/snippet}

<!-- The sticky box: zero height, its top edge is where the strip ends. The feed
     scroller treats it as the pinned element and its top as the reading line. -->
<div class="pinbox" style:--pin-lines={lines} bind:this={box}>
  <section class="pinbar" class:stuck aria-label={t("Running")} bind:this={section}>
    <ul>
      {#each visible as group, index (group.key)}
        {#if group.entries.length === 1}
          {@render entryLine(group.entries[0]!, title(group.entries[0]!), true)}
        {:else}
          {@render groupLine(group, `${uid}-g-${index}`)}
        {/if}
      {/each}
    </ul>
    {#if overflow}
      {@const shown = openKey === "more"}
      <button
        type="button"
        class="pin-more"
        class:late={hiddenLate > 0}
        aria-expanded={shown}
        aria-controls="{uid}-more"
        data-twin="more"
        onclick={() => toggle("more")}
      >
        <span class="dots" aria-hidden="true">
          {#each hiddenEntries.slice(0, 5) as entry (entry.id)}
            <span class="dot" style="background: {projectByID(entry.project_id)?.color ?? 'var(--border)'}"></span>
          {/each}
        </span>
        <span class="more-label">
          {t("+{n} more", { n: hiddenEntries.length })}{#if hiddenLate > 0}<span class="late-note">{` · ${t("{n} over 8h", { n: hiddenLate })}`}</span>{/if}
        </span>
        <span class="chevron" class:open={shown} aria-hidden="true">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6" /></svg>
        </span>
      </button>
      {#if shown}
        <ul class="pin-overlay" id="{uid}-more" data-pin-scroll>
          {#each hidden as group (group.key)}
            {#if group.entries.length === 1}
              {@render entryLine(group.entries[0]!, title(group.entries[0]!), true)}
            {:else}
              <li class="pin-line pin-caption">
                <span class="dot" style="background: {projectByID(group.projectID)?.color ?? 'var(--border)'}"></span>
                <span class="pin-title-static"><span class="pin-text">{title(group.entries[0]!)}</span><span class="count mono">≡ {group.entries.length}</span></span>
                <span class="pin-time mono" class:late={groupLate(group)}>{formatDuration(group.totalMs)}</span>
                <span></span>
              </li>
              {@render sessions(group)}
            {/if}
          {/each}
        </ul>
      {/if}
    {/if}
  </section>
</div>

<style>
  /* Line height and hit size per pointer: a thumb gets the app's 2.75rem target,
     a mouse a denser 2rem line. The box's top is the strip's bottom edge. */
  .pinbox {
    --pin-line: 2rem;
    --pin-hit: 28px;
    --pin-face: 26px;
    --pin-late: var(--accent);
    position: sticky;
    top: calc(env(safe-area-inset-top, 0px) + 0.5rem + 1px + var(--pin-lines) * var(--pin-line));
    z-index: 7;
    height: 0;
    overflow-anchor: none;
  }

  @media (pointer: coarse) {
    .pinbox {
      --pin-line: 2.75rem;
      --pin-hit: 44px;
      --pin-face: 32px;
    }
  }

  @media (prefers-color-scheme: light) {
    .pinbox {
      --pin-late: color-mix(in srgb, var(--accent) 60%, var(--text));
    }
  }

  /* The strip swaps with the full card by visibility alone: it fades in over the
     card's last slice, then the card hides (TimerPage). No height, position or
     transform ever animates. */
  .pinbar {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    padding: 4px 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-top: none;
    border-radius: 0 0 var(--radius) var(--radius);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
    opacity: 0;
    visibility: hidden;
    transition:
      opacity 120ms ease,
      visibility 0s linear 120ms;
  }

  .pinbar.stuck {
    opacity: 1;
    visibility: visible;
    transition:
      opacity 120ms ease,
      visibility 0s;
  }

  @media (prefers-color-scheme: light) {
    .pinbar,
    .pin-overlay {
      box-shadow: 0 6px 18px rgba(20, 26, 40, 0.12);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .pinbar,
    .pinbar.stuck {
      transition: none;
    }
  }

  /* While stuck the page carries a scroll padding up to the strip's bottom edge,
     so focus and PageDown keep rows out from under it. The strip's own controls
     sit inside that padded band by design and must not scroll the page. */
  .pinbox :global(*) {
    scroll-margin-top: calc(-1 * var(--pinned-offset, 0px));
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  /* Fixed height, one line, nothing wraps: the strip's height is arithmetic.
     Lines are deliberately not positioned - a group's overlay must take the
     strip, not its line, as its containing block - so the hairline between
     them is a background rather than a pseudo-element. */
  .pin-line,
  .pin-more,
  .pin-toggle {
    display: grid;
    grid-template-columns: 10px minmax(0, 1fr) auto var(--pin-hit);
    align-items: center;
    column-gap: 8px;
    height: var(--pin-line);
    padding: 0 4px 0 12px;
    overflow: hidden;
    box-sizing: border-box;
  }

  .pin-line + .pin-line,
  ul + .pin-more {
    background-image: linear-gradient(var(--border), var(--border));
    background-position: 12px 0;
    background-size: calc(100% - 24px) 1px;
    background-repeat: no-repeat;
  }

  .pin-title,
  .pin-toggle,
  .pin-more,
  .pin-stop {
    min-height: 0;
    margin: 0;
    border: none;
    border-radius: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  .pin-title,
  .pin-title-static {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    height: 100%;
    padding: 0;
    align-self: stretch;
    font-size: 0.9rem;
    line-height: 1.25;
    color: var(--text);
  }

  .pin-text {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pin-project {
    flex: 0 100 auto;
    min-width: 0;
    max-width: 14rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8rem;
    color: var(--text-dim);
  }

  .pin-suffix {
    flex: none;
    font-size: 0.75rem;
    color: var(--text-dim);
  }

  .count {
    flex: none;
    padding: 0 0.3rem;
    border-radius: 3px;
    font-size: 0.72rem;
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 20%, transparent);
  }

  .pin-time {
    min-width: 7ch;
    text-align: right;
    font-size: 0.875rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }

  .pin-time.late {
    color: var(--pin-late);
  }

  .pin-stop {
    display: grid;
    place-items: center;
    width: var(--pin-hit);
    height: var(--pin-hit);
    padding: 0;
  }

  .face {
    display: grid;
    place-items: center;
    width: var(--pin-face);
    height: var(--pin-face);
    box-sizing: border-box;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
  }

  .glyph {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    background: var(--text);
  }

  .pin-stop:focus-visible {
    outline: none;
  }

  .pin-stop:focus-visible .face {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  .pin-stop:active .face {
    background: var(--hover);
  }

  @media (hover: hover) {
    .pin-stop:hover .face {
      border-color: var(--text-dim);
    }

    .pin-toggle:hover,
    .pin-more:hover {
      background: var(--hover);
    }
  }

  /* A group is one button across the whole line, with the line's columns and
     padding. Not a subgrid: a subgrid's padding lands on its edge items as
     margin, which pushed the dot out of its 10px column and over the title. The
     transparent top border keeps the hairline above visible under a hover. */
  .group {
    display: block;
    padding: 0;
  }

  .pin-toggle {
    width: 100%;
    height: 100%;
  }

  .pin-line + .group > .pin-toggle {
    border-top: 1px solid transparent;
    background-clip: padding-box;
  }

  .group.open > .pin-toggle,
  .pin-more[aria-expanded="true"] {
    background: var(--hover);
  }

  .chevron {
    display: grid;
    place-items: center;
    width: var(--pin-hit);
    color: var(--text-dim);
    transition: transform 120ms ease;
  }

  .chevron.open {
    transform: rotate(180deg);
  }

  .pin-more {
    grid-template-columns: auto minmax(0, 1fr) var(--pin-hit);
    width: 100%;
    font-size: 0.8125rem;
    color: var(--text-dim);
  }

  .pin-more .dots {
    display: flex;
    align-items: center;
  }

  .pin-more .dots .dot + .dot {
    margin-left: -3px;
    box-shadow: 0 0 0 1.5px var(--surface);
  }

  .more-label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .pin-more.late {
    background: color-mix(in srgb, var(--accent) 10%, var(--surface));
    color: var(--text);
  }

  .late-note {
    color: var(--pin-late);
  }

  /* Below the strip, like a menu: it never changes the strip's height, the
     reading line or the scroll padding. */
  .pin-overlay {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    max-height: min(22rem, 50vh);
    max-height: min(22rem, 50svh);
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 4px 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
    scroll-padding-top: var(--pinned-offset, 0px);
  }

  .pin-caption {
    color: var(--text-dim);
  }

  @media (max-width: 34rem) {
    .pin-project {
      display: none;
    }
  }
</style>
