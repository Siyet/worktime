<!-- The day ruler: every calendar day from today back to the first recorded one,
     in the right margin of wide screens, scrolled on its own. A click jumps the
     feed to the day; an accent thumb over the spine marks what the feed shows and
     the ruler follows it, except while the reader is using the ruler itself.
     Spec: design/components/day-ruler.html. The model is lib/ruler.ts. -->
<script lang="ts">
  import { tick, untrack } from "svelte";
  import type { FeedDay } from "../feed";
  import type { FeedVisible } from "../feed-scroll.svelte";
  import { formatDurationShort } from "../format";
  import { t } from "../i18n";
  import { expandTimeOff } from "../report";
  import {
    RULER_HEAD,
    RULER_PITCH,
    buildRuler,
    dateOfISO,
    rowFrom,
    rowUntil,
    rulerOffset,
    stepMonth,
    type RulerModel,
    type RulerMonth,
    type RulerRow,
    type RulerTarget,
  } from "../ruler";
  import { formattingLocale } from "../settings.svelte";
  import type { TimeOff, TimeOffKind } from "../types";

  interface Props {
    days: FeedDay[];
    timeOff: TimeOff[];
    todayISO: string;
    visible: FeedVisible | null;
    /** Jumps the feed; resolves once the target is on screen. */
    onjump: (target: RulerTarget) => Promise<void> | void;
  }

  let { days, timeOff, todayISO, visible, onjump }: Props = $props();

  /** Hover intent before the first tooltip; after that it follows the pointer. */
  const TIP_DELAY_MS = 250;
  const TIP_HIDE_MS = 120;
  const STOPS = ".dr-day, .dr-mbtn";

  const model = $derived(buildRuler(days, expandTimeOff(timeOff), todayISO));
  const currentYear = $derived(Number(todayISO.slice(0, 4)));

  // Intl formatters for the UI's formatting locale, built once per locale.
  const formats = $derived.by(() => {
    const locale = formattingLocale();
    return {
      weekday: new Intl.DateTimeFormat(locale, { weekday: "short" }),
      month: new Intl.DateTimeFormat(locale, { month: "long" }),
      monthYear: new Intl.DateTimeFormat(locale, { month: "long", year: "numeric" }),
      full: new Intl.DateTimeFormat(locale, { weekday: "long", day: "numeric", month: "long", year: "numeric" }),
      tipDay: new Intl.DateTimeFormat(locale, { weekday: "long", day: "numeric", month: "long" }),
    };
  });

  let nav = $state<HTMLElement | null>(null);
  let scroller = $state<HTMLElement | null>(null);
  let tipElement = $state<HTMLElement | null>(null);

  let thumb = $state<{ top: number; bottom: number } | null>(null);
  let more = $state(false);
  let chip = $state<{ up: boolean; top: number | null; label: string } | null>(null);
  let tip = $state<{ title: string; lines: { text: string; time?: string; kind?: TimeOffKind }[] } | null>(null);

  // Plain fields: the reader's use of the ruler and the marks on its rows. The
  // marks are attributes set by hand, which Svelte never renders; a model
  // rebuild clears and sets them again.
  let hover = false;
  let keyboard = false;
  let tipButton: HTMLElement | null = null;
  let tipWarm = false;
  let tipTimer: ReturnType<typeof setTimeout> | undefined;
  let tabStop: HTMLElement | null = null;
  let marked = { first: -1, last: -1, current: -1 };
  let markedModel: RulerModel | null = null;
  let lastJump: { button: HTMLElement; iso: string | null } | null = null;

  function capitalise(text: string): string {
    return text.charAt(0).toLocaleUpperCase() + text.slice(1);
  }

  function shortLabel(iso: string): string {
    const date = dateOfISO(iso);
    return `${formats.weekday.format(date)} ${date.getDate()}`;
  }

  function monthName(month: RulerMonth): string {
    return capitalise(formats.month.format(new Date(month.year, month.month, 1, 12)));
  }

  function monthYear(month: RulerMonth): string {
    return capitalise(formats.monthYear.format(new Date(month.year, month.month, 1, 12)));
  }

  function kindLabel(kind: TimeOffKind): string {
    return t(kind === "sick" ? "sick leave" : kind === "dayoff" ? "day off" : "vacation");
  }

  function whereTo(target: RulerTarget, long: boolean): string {
    if (target === "top") return t("jumps to the top");
    return t("jumps to {day}", { day: long ? formats.full.format(dateOfISO(target)) : shortLabel(target) });
  }

  // A day's accessible name: the full date and what the day holds. The visible
  // "Tue 22 7h 35m" abbreviates it.
  function dayLabel(row: RulerRow): string {
    const full = formats.full.format(dateOfISO(row.iso));
    let label = row.today ? `${t("Today")}, ${full}` : capitalise(full);
    if (row.count > 0) label += `, ${formatDurationShort(row.trackedMs)}, ${t("{n} entries", { n: row.count })}`;
    if (row.off !== null) label += `, ${kindLabel(row.off).toLocaleLowerCase()}`;
    if (row.count === 0 && row.off === null) label += `, ${t("No entries").toLocaleLowerCase()}`;
    return label;
  }

  function monthLabel(month: RulerMonth): string {
    if (month.recorded) return t("{month}, go to the newest day with entries", { month: monthYear(month) });
    return t("{month}, no entries, {jump}", { month: monthYear(month), jump: whereTo(month.target, true) });
  }

  // The widest short weekday of the locale at the ruler's font, so the dates
  // line up in one column whatever the platform's fonts.
  const weekdayWidth = $derived.by(() => {
    if (nav === null) return null;
    const context = document.createElement("canvas").getContext("2d");
    if (context === null) return null;
    const style = getComputedStyle(nav);
    context.font = `400 ${style.fontSize} ${style.fontFamily}`;
    let widest = 0;
    const monday = dateOfISO("2026-09-21");
    for (let offset = 0; offset < 7; offset += 1) {
      const date = new Date(monday);
      date.setDate(monday.getDate() + offset);
      widest = Math.max(widest, context.measureText(formats.weekday.format(date)).width);
    }
    return Math.ceil(widest);
  });

  // --- where the feed is ------------------------------------------------------

  // A day's button, found by its date: indices shift when a new day is added
  // at the top.
  function buttonAt(index: number): HTMLButtonElement | null {
    const row = model.rows[index];
    if (row === undefined || nav === null) return null;
    return nav.querySelector<HTMLButtonElement>(`.dr-day[data-iso="${row.iso}"]`);
  }

  // A new day at midnight adds rows at the top; the rows the reader is looking
  // at stay put.
  let anchorRow: { iso: string; top: number } | null = null;

  $effect(() => {
    const range = visible;
    const current = model;
    const element = nav;
    untrack(() => {
      if (element === null) return;
      if (current !== markedModel) {
        const inside = scroller;
        if (inside !== null && anchorRow !== null && inside.scrollTop > 0) {
          const moved = current.byISO.get(anchorRow.iso);
          if (moved !== undefined && moved.top !== anchorRow.top) inside.scrollTop += moved.top - anchorRow.top;
        }
        const first = current.rows[0];
        anchorRow = first === undefined ? null : { iso: first.iso, top: first.top };
        // The rows were rebuilt: the old marks point at other days now.
        for (const marker of element.querySelectorAll("[data-vis]")) marker.removeAttribute("data-vis");
        for (const marker of element.querySelectorAll("[aria-current]")) marker.removeAttribute("aria-current");
        marked = { first: -1, last: -1, current: -1 };
        if (tabStop !== null && !tabStop.isConnected) tabStop = null;
        markedModel = current;
      }
      if (range === null || current.rows.length === 0) {
        thumb = null;
      } else {
        const top = rulerOffset(current, range.top);
        const bottom = Math.max(top + 4, rulerOffset(current, range.bottom));
        thumb = { top, bottom };
        const first = rowFrom(current, top);
        const at = range.current === null ? 0 : (current.byISO.get(range.current)?.index ?? 0);
        mark(first, Math.max(first, rowUntil(current, bottom)), at);
        if (!hover && !keyboard) follow(false);
      }
      updateChrome();
    });
  });

  // The visible range and the day at the reading line, marked by hand: toggling
  // a class on thousands of rows reactively would rerun every row each frame.
  function mark(first: number, last: number, current: number): void {
    if (first !== marked.first || last !== marked.last) {
      for (let index = Math.max(0, marked.first); index <= marked.last; index += 1) {
        if (index < first || index > last) buttonAt(index)?.removeAttribute("data-vis");
      }
      for (let index = first; index <= last; index += 1) {
        if (index < marked.first || index > marked.last) buttonAt(index)?.setAttribute("data-vis", "");
      }
      marked.first = first;
      marked.last = last;
    }
    if (current !== marked.current) {
      if (marked.current >= 0) buttonAt(marked.current)?.removeAttribute("aria-current");
      buttonAt(current)?.setAttribute("aria-current", "location");
      marked.current = current;
    }
    // While focus is elsewhere, Tab lands on where the reader is.
    if (!keyboard) setTabStop(buttonAt(current));
  }

  // Headers stuck at the top of the ruler at a track offset: a month's, and a
  // past year's above it.
  function stuckHeight(offset: number): number {
    const row = model.rows[Math.min(rowFrom(model, offset), model.rows.length - 1)];
    return row?.past ? 2 * RULER_HEAD : RULER_HEAD;
  }

  // Keeps the thumb inside a comfort band, moving the ruler the least it can.
  // Always instant: in lock-step with the page, and a long smooth scroll of
  // dates in the corner of the eye pulls attention from the feed. Only the
  // chip, clicked while looking at the ruler, animates a short distance.
  function follow(fromChip: boolean): void {
    const element = scroller;
    if (element === null || thumb === null) return;
    const height = element.clientHeight;
    const scrollTop = element.scrollTop;
    const heads = stuckHeight(thumb.top);
    const bandTop = heads + 0.15 * (height - heads);
    const bandBottom = 0.8 * height;
    let target = scrollTop;
    if (fromChip || thumb.top < scrollTop + bandTop) target = thumb.top - bandTop;
    else if (thumb.bottom > scrollTop + bandBottom) target = Math.min(thumb.bottom - bandBottom, thumb.top - bandTop);
    target = Math.min(Math.max(0, target), element.scrollHeight - height);
    if (Math.abs(target - scrollTop) < 0.5) return;
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const smooth = fromChip && Math.abs(target - scrollTop) <= 3 * height && !reduced;
    element.scrollTo({ top: target, behavior: smooth ? "smooth" : "instant" });
  }

  // The fade at the bottom while there is more below, and the chip that says
  // where the thumb went once the reader scrolled the ruler away from it.
  function updateChrome(): void {
    const element = scroller;
    if (element === null) return;
    more = element.scrollTop + element.clientHeight < element.scrollHeight - 1;
    if (thumb === null || model.rows.length === 0) {
      chip = null;
      return;
    }
    const heads = stuckHeight(element.scrollTop + RULER_HEAD);
    const up = thumb.bottom < element.scrollTop + heads;
    const down = thumb.top > element.scrollTop + element.clientHeight - 8;
    if (!up && !down) {
      chip = null;
      return;
    }
    const current = model.rows[Math.max(0, marked.current)];
    chip = { up, top: up ? heads : null, label: current === undefined ? "" : shortLabel(current.iso) };
  }

  // --- stops, focus and keys ----------------------------------------------------

  function setTabStop(button: HTMLElement | null): void {
    if (button === null || button === tabStop) return;
    if (tabStop !== null) tabStop.tabIndex = -1;
    button.tabIndex = 0;
    tabStop = button;
  }

  function stopOf(target: EventTarget | null): HTMLButtonElement | null {
    return target instanceof Element ? target.closest<HTMLButtonElement>(STOPS) : null;
  }

  function monthOf(button: HTMLElement): RulerMonth | undefined {
    return button.dataset.month === undefined ? undefined : model.monthByKey.get(button.dataset.month);
  }

  function rowOf(button: HTMLElement): RulerRow | undefined {
    return button.dataset.iso === undefined ? undefined : model.byISO.get(button.dataset.iso);
  }

  function targetOf(button: HTMLElement): RulerTarget | undefined {
    return (monthOf(button) ?? rowOf(button))?.target;
  }

  async function jump(button: HTMLElement): Promise<void> {
    const target = targetOf(button);
    if (target === undefined) return;
    setTabStop(button);
    lastJump = { button, iso: target === "top" ? null : target };
    await onjump(target);
  }

  function onclick(event: MouseEvent): void {
    const button = stopOf(event.target);
    if (button !== null) void jump(button);
  }

  // Focus stays clear of the stuck headers and the bottom fade.
  function focusStop(button: HTMLElement): void {
    setTabStop(button);
    button.focus({ preventScroll: true });
    const element = scroller;
    if (element === null) return;
    const month = monthOf(button);
    const row = rowOf(button);
    const top = month?.top ?? row?.top ?? 0;
    const height = month === undefined ? RULER_PITCH : RULER_HEAD;
    const heads = month !== undefined ? (month.past ? RULER_HEAD : 0) : row?.past ? 2 * RULER_HEAD : RULER_HEAD;
    if (top < element.scrollTop + heads) element.scrollTop = top - heads;
    else if (top + height > element.scrollTop + element.clientHeight - 40) {
      element.scrollTop = top + height - element.clientHeight + 40;
    }
    showTip(button);
  }

  function onkeydown(event: KeyboardEvent): void {
    const button = stopOf(event.target);
    if (button === null || nav === null) return;
    if (event.key === "Escape") {
      hideTip(false);
      return;
    }
    if (event.key === "Tab") {
      // After a jump, Tab moves into the landed day, as an in-page link would.
      if (!event.shiftKey && lastJump !== null && lastJump.button === button && lastJump.iso !== null) {
        const control = document.querySelector<HTMLElement>(
          `.feed > .day[data-key="${lastJump.iso}"] :is(button, [href], input, [tabindex="0"])`,
        );
        if (control !== null) {
          event.preventDefault();
          lastJump = null;
          control.focus();
        }
      }
      return;
    }
    const stops = [...nav.querySelectorAll<HTMLElement>(STOPS)];
    const position = stops.indexOf(button);
    const month = monthOf(button);
    const row = rowOf(button);
    const months = event.shiftKey ? 12 : 1;
    let next: HTMLElement | undefined;
    switch (event.key) {
      case "ArrowDown":
        next = stops[Math.min(position + 1, stops.length - 1)];
        break;
      case "ArrowUp":
        next = stops[Math.max(position - 1, 0)];
        break;
      case "Home":
        next = buttonAt(0) ?? undefined;
        break;
      case "End":
        next = buttonAt(model.rows.length - 1) ?? undefined;
        break;
      case "PageDown":
      case "PageUp": {
        const delta = event.key === "PageUp" ? months : -months;
        if (month !== undefined) {
          // Months are newest first: an older month is further down.
          const neighbour = model.months[Math.min(Math.max(month.index - delta, 0), model.months.length - 1)]!;
          next = nav.querySelector<HTMLElement>(`.dr-mbtn[data-month="${neighbour.key}"]`) ?? undefined;
        } else if (row !== undefined) {
          next = buttonAt(stepMonth(model, row.index, delta)) ?? undefined;
        }
        break;
      }
      default:
        return;
    }
    event.preventDefault();
    lastJump = null;
    if (next !== undefined) focusStop(next);
  }

  function onfocusin(event: FocusEvent): void {
    const button = stopOf(event.target);
    keyboard = button !== null && button.matches(":focus-visible");
    if (keyboard && button !== null) showTip(button);
  }

  function onfocusout(event: FocusEvent): void {
    if (nav !== null && event.relatedTarget instanceof Node && nav.contains(event.relatedTarget)) return;
    keyboard = false;
    if (!hover) hideTip(false);
  }

  // --- tooltip ----------------------------------------------------------------

  function onpointerenter(): void {
    hover = true;
  }

  function onpointerleave(): void {
    hover = false;
    clearTimeout(tipTimer);
    tipTimer = setTimeout(() => {
      if (!keyboard) hideTip(false);
    }, TIP_HIDE_MS);
  }

  function onpointermove(event: PointerEvent): void {
    const button = stopOf(event.target);
    if (button === null) {
      if (tipWarm) hideTip(true);
      return;
    }
    if (button === tipButton && tip !== null) return;
    tipButton = button;
    clearTimeout(tipTimer);
    if (tipWarm) showTip(button);
    else {
      tipTimer = setTimeout(() => {
        tipWarm = true;
        if (hover && tipButton !== null) showTip(tipButton);
      }, TIP_DELAY_MS);
    }
  }

  function showTip(button: HTMLElement): void {
    clearTimeout(tipTimer);
    tipButton = button;
    const month = monthOf(button);
    const row = rowOf(button);
    if (month !== undefined) {
      const where = whereTo(month.target, false);
      tip = { title: monthYear(month), lines: [{ text: month.recorded ? capitalise(where) : `${t("No entries")} · ${where}` }] };
    } else if (row !== undefined) {
      const date = dateOfISO(row.iso);
      const day = row.year === currentYear ? formats.tipDay.format(date) : formats.full.format(date);
      const lines: { text: string; time?: string; kind?: TimeOffKind }[] = [];
      if (row.count > 0) lines.push({ time: formatDurationShort(row.trackedMs), text: `· ${t("{n} entries", { n: row.count })}` });
      if (row.off !== null) lines.push({ kind: row.off, text: kindLabel(row.off) });
      if (row.count === 0) lines.push({ text: (row.off === null ? `${t("No entries")} · ` : "") + whereTo(row.target, false) });
      tip = { title: row.today ? `${t("Today")}, ${day}` : capitalise(day), lines };
    } else return;
    void tick().then(() => placeTip(button));
  }

  // To the left of the ruler, centred on the row, inside the window.
  function placeTip(button: HTMLElement): void {
    if (tipElement === null || nav === null || !button.isConnected) return;
    const navRect = nav.getBoundingClientRect();
    const rowRect = button.getBoundingClientRect();
    const width = tipElement.offsetWidth;
    const height = tipElement.offsetHeight;
    tipElement.style.left = `${navRect.left - 8 - width}px`;
    const top = rowRect.top + rowRect.height / 2 - height / 2;
    tipElement.style.top = `${Math.min(Math.max(8, top), window.innerHeight - height - 8)}px`;
  }

  function hideTip(keepWarm: boolean): void {
    clearTimeout(tipTimer);
    tip = null;
    tipButton = null;
    if (!keepWarm) tipWarm = false;
  }

  function onscroll(): void {
    updateChrome();
    if (tipButton !== null && tip !== null) placeTip(tipButton);
  }

  // The ruler's buttons share one set of listeners on the landmark - a roving
  // tab stop over thousands of rows - attached here rather than as attributes,
  // which would read as a non-interactive element taking input.
  function listen(element: HTMLElement): () => void {
    const listeners: [string, EventListener][] = [
      ["click", onclick as EventListener],
      ["keydown", onkeydown as EventListener],
      ["focusin", onfocusin as EventListener],
      ["focusout", onfocusout as EventListener],
      ["pointerenter", onpointerenter],
      ["pointerleave", onpointerleave],
      ["pointermove", onpointermove as EventListener],
    ];
    for (const [type, listener] of listeners) element.addEventListener(type, listener);
    return () => {
      for (const [type, listener] of listeners) element.removeEventListener(type, listener);
    };
  }

  // The first frame shows where the reader is.
  $effect(() => {
    if (scroller === null) return;
    untrack(() => {
      follow(false);
      updateChrome();
    });
  });

  $effect(() => () => clearTimeout(tipTimer));
</script>

{#if model.rows.length > 0}
  <nav
    class="dr"
    aria-label={t("Days")}
    data-own-scroll
    style:--dr-wd-w={weekdayWidth === null ? undefined : `${weekdayWidth}px`}
    bind:this={nav}
    {@attach listen}
  >
    <div class="dr-scroll" class:more bind:this={scroller} {onscroll}>
      <div class="dr-track">
        <div class="dr-spine" style:height="{model.spineEnd}px"></div>
        {#if thumb !== null}
          <div class="dr-thumb" style:top="{thumb.top.toFixed(1)}px" style:height="{(thumb.bottom - thumb.top).toFixed(1)}px"></div>
        {/if}
        {#each model.years as year, yearIndex (year.year)}
          <div class="dr-year" class:past={year.past}>
            {#if year.past}
              <div class="dr-yhead" aria-hidden="true">{year.year}</div>
            {/if}
            {#each year.months as month, monthIndex (month.key)}
              <section
                class="dr-month"
                class:near={yearIndex === 0 && monthIndex < 2}
                style:contain-intrinsic-size="auto {month.height}px"
              >
                <div class="dr-mhead">
                  <button type="button" class="dr-mbtn" data-month={month.key} tabindex="-1" aria-label={monthLabel(month)}>
                    {monthName(month)}
                  </button>
                </div>
                <ol class="dr-days" aria-label={monthYear(month)}>
                  {#each month.rows as row (row.iso)}
                    <li class="dr-li" data-band={row.band ?? undefined}>
                      <button
                        type="button"
                        class="dr-day"
                        class:mon={row.weekday === 1}
                        class:first={row.date === 1}
                        class:rec={row.count > 0}
                        class:today={row.today}
                        data-iso={row.iso}
                        tabindex="-1"
                        aria-label={dayLabel(row)}
                      >
                        <span class="dr-wd">{formats.weekday.format(dateOfISO(row.iso))}</span>
                        <span class="dr-n">{row.date}</span>
                        {#if row.count > 0}
                          <span class="dr-dur">{formatDurationShort(row.trackedMs)}</span>
                        {/if}
                      </button>
                    </li>
                  {/each}
                </ol>
              </section>
            {/each}
          </div>
        {/each}
      </div>
    </div>
    {#if chip !== null}
      <!-- Decorative for assistive technology: focus lands on the current day anyway. -->
      <button
        type="button"
        class="dr-return"
        class:down={!chip.up}
        style:top={chip.top === null ? undefined : `${chip.top}px`}
        tabindex="-1"
        aria-hidden="true"
        onclick={(event) => {
          event.stopPropagation();
          follow(true);
        }}
      >
        {chip.up ? "▲" : "▼"} {chip.label}
      </button>
    {/if}
  </nav>
  {#if tip !== null}
    <div class="dr-tip" bind:this={tipElement} aria-hidden="true">
      <div class="tt">{tip.title}</div>
      {#each tip.lines as line, index (index)}
        <div class="ts">
          {#if line.kind !== undefined}
            <span class="kind {line.kind}"></span>
          {/if}
          {#if line.time !== undefined}
            <span class="num">{line.time}</span>
          {/if}
          {line.text}
        </div>
      {/each}
    </div>
  {/if}
{/if}

<style>
  .dr {
    /* Geometry. Every size is fixed, so every offset in the ruler is arithmetic
       (lib/ruler.ts): nothing here is measured. */
    --dr-w: 7.875rem;
    --dr-pitch: 1rem;
    --dr-head: 1.5rem;
    --dr-spine-x: 4px;
    --dr-tick-day: 6px;
    --dr-tick-week: 11px;
    --dr-tick-month: 17px;
    --dr-label-x: 1.625rem;
    --dr-wd-w: 1.5rem;
    --dr-font: 0.6875rem;
    --dr-dur-font: 0.65625rem;
    --dr-dur-gap: 8px;
    --dr-pad-r: 4px;

    /* Labels sit on the bands too, so they are a touch brighter than --text-dim. */
    --dr-label: color-mix(in srgb, var(--text-dim) 90%, var(--text));
    --dr-strong: var(--text);
    --dr-today: var(--accent);
    --dr-thumb: var(--accent);
    --dr-spine: color-mix(in srgb, var(--text-dim) 38%, transparent);
    --dr-tick: color-mix(in srgb, var(--text-dim) 75%, transparent);
    --dr-tick-quiet: color-mix(in srgb, var(--text-dim) 42%, transparent);
    --dr-tick-week-c: var(--text-dim);
    --dr-tick-month-c: var(--text);

    /* Fixed in the right margin of the 68rem shell, 12px clear of the cards. */
    position: fixed;
    z-index: 6;
    top: 0.75rem;
    bottom: 0;
    left: calc(50% + 33.75rem);
    width: var(--dr-w);
    display: flex;
    flex-direction: column;
    font-size: var(--dr-font);
    line-height: 1;
  }

  @media (prefers-color-scheme: light) {
    .dr {
      --dr-label: color-mix(in srgb, var(--text-dim) 80%, var(--text));
      --dr-today: color-mix(in srgb, var(--accent) 55%, var(--text));
      --dr-thumb: color-mix(in srgb, var(--accent) 75%, var(--text));
    }
  }

  @media print {
    .dr,
    .dr-tip {
      display: none !important;
    }
  }

  /* The ruler's own scroller: never chains into the page, no scrollbar - the
     fade at the bottom and the wheel are the affordance. */
  .dr-scroll {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-x: hidden;
    overflow-y: auto;
    overscroll-behavior: contain;
    scrollbar-width: none;
    scroll-padding: calc(2 * var(--dr-head)) 0 2.5rem;
  }

  .dr-scroll::-webkit-scrollbar {
    display: none;
  }

  .dr-scroll.more {
    -webkit-mask-image: linear-gradient(to bottom, #000 calc(100% - 2rem), transparent);
    mask-image: linear-gradient(to bottom, #000 calc(100% - 2rem), transparent);
  }

  .dr-track {
    position: relative;
    padding-bottom: 1.5rem;
  }

  /* The spine ends at the oldest day's tick: the start of the history. */
  .dr-spine {
    position: absolute;
    top: 0;
    left: var(--dr-spine-x);
    width: 1px;
    background: var(--dr-spine);
  }

  /* What the feed shows. Above the headers, so a month boundary never cuts it. */
  .dr-thumb {
    position: absolute;
    z-index: 5;
    left: calc(var(--dr-spine-x) - 1px);
    width: 3px;
    border-radius: 2px;
    background: var(--dr-thumb);
  }

  .dr-days {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  /* Off-screen months skip style, layout and paint; their size is exact, so the
     scroll height never changes. The two newest months always render, so the
     first frame is never blank. */
  .dr-month {
    content-visibility: auto;
  }

  .dr-month.near {
    content-visibility: visible;
  }

  /* Headers stick to the top of the ruler, the spine drawn into their
     background so it never breaks under one. */
  .dr-yhead,
  .dr-mhead {
    position: sticky;
    top: 0;
    height: var(--dr-head);
    display: flex;
    align-items: flex-end;
    white-space: nowrap;
    background:
      linear-gradient(var(--dr-spine), var(--dr-spine)) var(--dr-spine-x) 0 / 1px 100% no-repeat,
      var(--bg);
  }

  .dr-mhead {
    z-index: 2;
  }

  .dr-year.past .dr-mhead {
    top: var(--dr-head);
  }

  /* A month header jumps to the month's newest day with entries. */
  .dr-mbtn {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: flex-end;
    width: 100%;
    height: calc(var(--dr-head) - 4px);
    padding: 0 0 0.3rem calc(var(--dr-label-x) - 4px);
    border-radius: 4px;
    font-size: var(--dr-font);
    font-weight: 600;
    line-height: 1;
    color: var(--dr-strong);
    cursor: pointer;
  }

  .dr-mbtn:hover {
    background: var(--hover);
  }

  .dr-mbtn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }

  /* A past year: its own row above its months, a hairline across the ruler. */
  .dr-yhead {
    z-index: 3;
    padding: 0 0 0.3rem calc(var(--dr-label-x) - 4px);
    font-size: 0.75rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    color: var(--dr-strong);
    background:
      linear-gradient(var(--border), var(--border)) var(--dr-spine-x) 0 / 100% 1px no-repeat,
      linear-gradient(var(--dr-spine), var(--dr-spine)) var(--dr-spine-x) 0 / 1px 100% no-repeat,
      var(--bg);
  }

  .dr-li {
    position: relative;
    height: var(--dr-pitch);
  }

  /* Bands exactly as the Reports daily chart draws them (DailyChart.svelte), the
     same values in both themes: sick, else other time off, else the weekend.
     Square ends and no gaps, so a weekend or a vacation reads as one block. */
  .dr-li[data-band]::before {
    content: "";
    position: absolute;
    inset: 0 0 0 calc(var(--dr-spine-x) + 1px);
    background: var(--dr-band);
  }

  .dr-li[data-band="weekend"] {
    --dr-band: rgba(96, 125, 190, 0.13);
  }

  .dr-li[data-band="vacation"] {
    --dr-band: rgba(64, 190, 196, 0.15);
  }

  .dr-li[data-band="dayoff"] {
    --dr-band: rgba(181, 125, 232, 0.17);
  }

  /* The chart's hatch: its 7px tile clips half of the 1.6px stroke, so 0.8px is
     what it shows. */
  .dr-li[data-band="sick"] {
    --dr-band: repeating-linear-gradient(135deg, rgba(224, 82, 82, 0.38) 0 0.8px, transparent 0.8px 7px), rgba(224, 82, 82, 0.1);
  }

  .dr-day {
    all: unset;
    box-sizing: border-box;
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    width: 100%;
    height: 100%;
    padding: 0 var(--dr-pad-r) 0 var(--dr-label-x);
    border-radius: 4px;
    font-size: var(--dr-font);
    line-height: 1;
    white-space: nowrap;
    color: var(--dr-label);
    cursor: pointer;
  }

  /* The tick. Its length is the calendar, nothing else. */
  .dr-day::before {
    content: "";
    position: absolute;
    left: calc(var(--dr-spine-x) + 1px);
    top: calc(50% - 0.5px);
    width: var(--dr-tick-day);
    height: 1px;
    background: var(--dr-tick-quiet);
  }

  .dr-day.rec::before {
    background: var(--dr-tick);
  }

  .dr-day.mon::before {
    width: var(--dr-tick-week);
    background: var(--dr-tick-week-c);
  }

  .dr-day.first::before {
    width: var(--dr-tick-month);
    background: var(--dr-tick-month-c);
  }

  .dr-day.today::before {
    background: var(--dr-today);
  }

  .dr-wd {
    flex: none;
    width: var(--dr-wd-w);
    text-align: right;
  }

  .dr-n {
    flex: none;
    width: 2ch;
    margin-left: 3px;
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  /* The day header's own figure, right-aligned at the edge like the feed's
     totals. Muted, never accented; only on days with entries. */
  .dr-dur {
    flex: none;
    margin-left: auto;
    padding-left: var(--dr-dur-gap);
    font-family: var(--mono);
    font-size: var(--dr-dur-font);
    font-variant-numeric: tabular-nums;
    color: var(--dr-label);
  }

  .dr-day.rec .dr-n {
    color: var(--dr-strong);
  }

  .dr-day.first .dr-n {
    font-weight: 600;
  }

  .dr-day.today .dr-wd,
  .dr-day.today .dr-n {
    color: var(--dr-today);
    font-weight: 600;
  }

  /* Days the feed shows right now. */
  .dr-day:global([data-vis]) .dr-wd,
  .dr-day:global([data-vis]) .dr-n {
    color: var(--dr-strong);
  }

  .dr-day.today:global([data-vis]) .dr-wd,
  .dr-day.today:global([data-vis]) .dr-n {
    color: var(--dr-today);
  }

  .dr-day:hover {
    background: var(--hover);
  }

  .dr-day:hover .dr-wd,
  .dr-day:hover .dr-n,
  .dr-day:hover .dr-dur {
    color: var(--dr-strong);
  }

  .dr-day:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }

  /* Shown when the thumb is scrolled out of the ruler: says where it is. */
  .dr-return {
    position: absolute;
    z-index: 6;
    left: 2px;
    right: 2px;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.2rem;
    height: var(--dr-pitch);
    min-height: 0;
    padding: 0 0.4rem;
    font-size: 0.65625rem;
    font-weight: 600;
    line-height: 1;
    color: var(--dr-today);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 999px;
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.18);
    cursor: pointer;
  }

  .dr-return.down {
    bottom: 0.25rem;
  }

  /* To the left of the ruler, the only side with room. */
  .dr-tip {
    position: fixed;
    z-index: 9;
    top: 0;
    left: 0;
    pointer-events: none;
    padding: 0.4rem 0.65rem 0.45rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
    font-size: 0.8rem;
    line-height: 1.35;
    white-space: nowrap;
  }

  @media (prefers-color-scheme: light) {
    .dr-tip {
      box-shadow: 0 6px 18px rgba(20, 26, 40, 0.12);
    }
  }

  .tt {
    font-weight: 600;
  }

  .ts {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--text-dim);
    font-size: 0.75rem;
  }

  .num {
    font-family: var(--mono);
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }

  .kind {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }

  .kind.vacation {
    background: var(--teal);
  }

  .kind.dayoff {
    background: var(--purple);
  }

  .kind.sick {
    background: var(--danger);
  }

  @media (forced-colors: active) {
    .dr-spine,
    .dr-day::before {
      background: CanvasText;
    }

    .dr-thumb {
      background: Highlight;
    }

    /* Time off keeps a mark; the weekend band goes - the weekday says it. */
    .dr-li[data-band]::before {
      background: none;
    }

    .dr-li[data-band]:not([data-band="weekend"])::before {
      border-left: 3px solid CanvasText;
    }
  }
</style>
