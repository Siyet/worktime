<!-- The day ruler: every calendar day from today back to the first recorded one,
     at the right edge of wide screens, scrolled on its own - a tick per day
     against the edge, like the outline of a Notion page. A click jumps the feed
     to the day; the ticks of the days the feed shows light up in the accent, and
     the ruler follows them, except while the reader is using the ruler itself.
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
    /** When the reader last scrolled the page (performance.now). */
    scrolledAt: () => number;
    /** Jumps the feed; resolves once the target is on screen. `takeFocus` moves focus there. */
    onjump: (target: RulerTarget, takeFocus: boolean) => Promise<void> | void;
  }

  let { days, timeOff, todayISO, visible, scrolledAt, onjump }: Props = $props();

  /** Hover intent before the first tooltip; after that it follows the pointer. */
  const TIP_DELAY_MS = 250;
  const TIP_HIDE_MS = 120;
  const STOPS = ".dr-day, .dr-mbtn";

  // Each model is built against the last one, so the rows that stayed the same
  // are the same objects and do not re-render.
  let lastModel: RulerModel | undefined;
  const model = $derived.by(() => (lastModel = buildRuler(days, expandTimeOff(timeOff), todayISO, lastModel)));
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

  // What the feed shows, in track px: the span its lit ticks cover. Drawn by the
  // ticks alone, so only the ruler's scrolling and the chip read it.
  let shown: { top: number; bottom: number } | null = null;
  let more = $state(false);
  /** `cover` hides the part of a row between the stuck headers and the chip. */
  let chip = $state<{ up: boolean; top: number | null; cover: number; label: string } | null>(null);
  let tip = $state<{ title: string; lines: { text: string; time?: string; kind?: TimeOffKind }[] } | null>(null);

  // Plain fields: the reader's use of the ruler and the marks on its rows. The
  // marks are attributes set by hand, which Svelte never renders; a model
  // rebuild clears and sets them again.
  let hover = false;
  let keyboard = false;
  /** When the reader left the ruler: it stays where they left it until the page scrolls. */
  let parkedAt: number | null = null;
  let pointer: { x: number; y: number } | null = null;
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
    if (row.count > 0) label += `, ${formatDurationShort(row.clockMs)}${trackedNote(row, ", ")}, ${entriesLabel(row.count)}`;
    if (row.off !== null) label += `, ${kindLabel(row.off)}`;
    if (row.count === 0 && row.off === null) label += `, ${t("no entries")}`;
    return label;
  }

  // The header's second figure, where parallel work makes it differ from the first.
  function trackedNote(row: RulerRow, lead: string): string {
    if (row.trackedMs === row.clockMs) return "";
    return lead + t("{tracked} tracked - work that ran in parallel is counted once", { tracked: formatDurationShort(row.trackedMs) });
  }

  function entriesLabel(count: number): string {
    return count === 1 ? t("1 entry") : t("{n} entries", { n: count });
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
      // Every read of the scroller, and its scroll, before the marks change any
      // style: one layout a frame, not one per read.
      const view = viewOf();
      if (range === null || current.rows.length === 0) {
        shown = null;
      } else {
        const top = rulerOffset(current, range.top);
        const bottom = Math.max(top + 4, rulerOffset(current, range.bottom));
        shown = { top, bottom };
        if (view !== null && following()) view.scrollTop = follow(false, view);
        const first = rowFrom(current, top);
        const at = range.current === null ? 0 : (current.byISO.get(range.current)?.index ?? 0);
        mark(first, Math.max(first, rowUntil(current, bottom)), at);
      }
      if (view !== null) updateChrome(view);
    });
  });

  interface View {
    scrollTop: number;
    height: number;
    scrollHeight: number;
  }

  function viewOf(): View | null {
    const element = scroller;
    return element === null ? null : { scrollTop: element.scrollTop, height: element.clientHeight, scrollHeight: element.scrollHeight };
  }

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

  // Following pauses while the reader uses the ruler, and once they leave it
  // resumes with the first page scroll: until then the ruler stays where they
  // left it, whatever a sync or midnight changes.
  function following(): boolean {
    if (hover || keyboard) return false;
    if (parkedAt !== null && scrolledAt() <= parkedAt) return false;
    parkedAt = null;
    return true;
  }

  // Headers stuck at the top of the ruler at a track offset: a month's, and a
  // past year's above it.
  function stuckHeight(offset: number): number {
    const row = model.rows[Math.min(rowFrom(model, offset), model.rows.length - 1)];
    return row?.past ? 2 * RULER_HEAD : RULER_HEAD;
  }

  // Keeps the lit ticks inside a comfort band, moving the ruler the least it can.
  // Always instant: in lock-step with the page, and a long smooth scroll of
  // dates in the corner of the eye pulls attention from the feed. Only the
  // chip, clicked while looking at the ruler, animates a short distance.
  // Returns where the ruler is scrolled to.
  function follow(fromChip: boolean, view: View | null = viewOf()): number {
    const element = scroller;
    if (element === null || view === null || shown === null) return view?.scrollTop ?? 0;
    const { height, scrollTop } = view;
    const heads = stuckHeight(shown.top);
    const bandTop = heads + 0.15 * (height - heads);
    const bandBottom = 0.8 * height;
    let target = scrollTop;
    if (fromChip || shown.top < scrollTop + bandTop) target = shown.top - bandTop;
    else if (shown.bottom > scrollTop + bandBottom) target = Math.min(shown.bottom - bandBottom, shown.top - bandTop);
    target = Math.min(Math.max(0, target), view.scrollHeight - height);
    if (Math.abs(target - scrollTop) < 0.5) return scrollTop;
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const smooth = fromChip && Math.abs(target - scrollTop) <= 3 * height && !reduced;
    element.scrollTo({ top: target, behavior: smooth ? "smooth" : "instant" });
    // A smooth scroll is still where it was; its scroll events update the chrome.
    return smooth ? scrollTop : target;
  }

  // The fade at the bottom while there is more below, and the chip that says
  // where the lit ticks went once the reader scrolled the ruler away from them. The
  // chip is for the pointer: it would cover the row keyboard focus moves to,
  // and focus lands on the current day anyway.
  function updateChrome(view: View | null = viewOf()): void {
    if (view === null) return;
    const { scrollTop, height } = view;
    const hasMore = scrollTop + height < view.scrollHeight - 1;
    if (hasMore !== more) more = hasMore;
    if (shown === null || model.rows.length === 0 || keyboard) {
      chip = null;
      return;
    }
    const heads = stuckHeight(scrollTop + RULER_HEAD);
    const up = shown.bottom < scrollTop + heads;
    const down = shown.top > scrollTop + height - 8;
    if (!up && !down) {
      chip = null;
      return;
    }
    // Up, over the first whole row under the stuck headers, the part of a row
    // (and any header) above it covered, so it never shows half a label.
    let top: number | null = null;
    let cover = 0;
    if (up) {
      const line = scrollTop + heads;
      const row = model.rows[rowFrom(model, line)]!;
      const next = model.rows[row.index + 1];
      const whole = row.top >= line - 0.5 || next === undefined ? row : next;
      top = Math.max(heads, whole.top - scrollTop);
      // The last row of a month is under its header, which the month's end
      // pushes up: then the next month's header shows, uncovered, above the chip.
      if (whole === next && next.top === row.top + RULER_PITCH) cover = top - heads;
    }
    const current = model.rows[Math.max(0, marked.current)];
    const label = current === undefined ? "" : shortLabel(current.iso);
    if (chip !== null && chip.up === up && chip.top === top && chip.cover === cover && chip.label === label) return;
    chip = { up, top, cover, label };
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

  async function jump(button: HTMLElement, takeFocus: boolean): Promise<void> {
    const target = targetOf(button);
    if (target === undefined) return;
    setTabStop(button);
    lastJump = { button, iso: target === "top" ? null : target };
    await onjump(target, takeFocus);
  }

  // A mouse click (detail counts the clicks; Enter and Space give 0) from
  // outside the ruler takes focus to where it lands. Keyboard focus in the ruler
  // stays there, and Tab moves on into the landed day.
  function onclick(event: MouseEvent): void {
    const button = stopOf(event.target);
    if (button !== null) void jump(button, event.detail > 0 && !(nav?.contains(document.activeElement) ?? false));
  }

  // A click jumps without taking focus, as in Safari, so the page keys go on
  // scrolling the page; once the reader is in the ruler with the keyboard, a
  // click moves focus along. The decorative chip never takes it.
  function onmousedown(event: MouseEvent): void {
    if (!(event.target instanceof Element)) return;
    const chipped = event.target.closest(".dr-return") !== null;
    if (chipped || (stopOf(event.target) !== null && !nav?.contains(document.activeElement))) event.preventDefault();
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
    updateChrome();
  }

  function onfocusout(event: FocusEvent): void {
    if (nav !== null && event.relatedTarget instanceof Node && nav.contains(event.relatedTarget)) return;
    keyboard = false;
    parkedAt = performance.now();
    if (!hover) hideTip(false);
    updateChrome();
  }

  // --- tooltip ----------------------------------------------------------------

  function onpointerenter(): void {
    hover = true;
    // Back within the hide delay: the tooltip stays.
    clearTimeout(tipTimer);
  }

  function onpointerleave(): void {
    hover = false;
    pointer = null;
    parkedAt = performance.now();
    clearTimeout(tipTimer);
    tipTimer = setTimeout(() => {
      if (!keyboard) hideTip(false);
    }, TIP_HIDE_MS);
  }

  function onpointermove(event: PointerEvent): void {
    // WebKit reports a resting pointer as moving when the rows scroll under it;
    // while the keyboard pages, the tooltip is the focused row's.
    if (keyboard && pointer !== null && pointer.x === event.clientX && pointer.y === event.clientY) return;
    pointer = { x: event.clientX, y: event.clientY };
    pointAt(stopOf(event.target));
  }

  function pointAt(button: HTMLElement | null): void {
    if (button === null) {
      clearTimeout(tipTimer);
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
      if (row.count > 0) lines.push({ time: formatDurationShort(row.clockMs), text: `· ${entriesLabel(row.count)}` });
      if (row.clockMs !== row.trackedMs) lines.push({ text: trackedNote(row, "") });
      if (row.off !== null) lines.push({ kind: row.off, text: kindLabel(row.off) });
      if (row.count === 0) {
        const where = whereTo(row.target, false);
        lines.push({ text: row.off === null ? `${t("No entries")} · ${where}` : capitalise(where) });
      }
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
    // Chromium sends no pointermove when the rows scroll under a resting pointer.
    if (hover && !keyboard && pointer !== null) pointAt(stopOf(document.elementFromPoint(pointer.x, pointer.y)));
    if (tipButton !== null && tip !== null) placeTip(tipButton);
  }

  // The ruler's buttons share one set of listeners on the landmark - a roving
  // tab stop over thousands of rows - attached here rather than as attributes,
  // which would read as a non-interactive element taking input.
  function listen(element: HTMLElement): () => void {
    const listeners: [string, EventListener][] = [
      ["click", onclick as EventListener],
      ["mousedown", onmousedown as EventListener],
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
                          <span class="dr-dur">{formatDurationShort(row.clockMs)}</span>
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
        style:--dr-cover="{chip.cover}px"
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
       (lib/ruler.ts): nothing here is measured. Rows and headers are px, the
       model's RULER_PITCH and RULER_HEAD, whatever the browser's font size; the
       type is rem and follows it. */
    --dr-w: min(11rem, calc(50% - 33.75rem));
    --dr-pitch: 20px;
    --dr-head: 28px;
    /* Ticks: 2px bars against the right edge, a Monday longer than a day and the
       1st longer still; a hovered one grows by --dr-tick-grow. The labels keep
       clear of the longest a tick can get. */
    --dr-tick-h: 2px;
    --dr-tick-day: 12px;
    --dr-tick-week: 20px;
    --dr-tick-month: 28px;
    --dr-tick-grow: 10px;
    --dr-ticks: calc(var(--dr-tick-month) + var(--dr-tick-grow) + 6px);
    --dr-label-x: 0.375rem;
    --dr-wd-w: 1.75rem;
    --dr-font: 0.75rem;
    --dr-dur-font: 0.6875rem;
    --dr-dur-gap: 8px;

    /* Labels sit on the bands too, so they are a touch brighter than --text-dim. */
    --dr-label: color-mix(in srgb, var(--text-dim) 90%, var(--text));
    --dr-strong: var(--text);
    --dr-today: var(--accent);
    --dr-lit: var(--accent);
    --dr-tick: var(--text);

    /* Fixed against the right edge of the window, as wide as the margin beside
       the 68rem shell allows, 12px clear of the cards. */
    position: fixed;
    z-index: 6;
    top: 0.75rem;
    bottom: 0;
    right: 0;
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
      --dr-lit: color-mix(in srgb, var(--accent) 60%, var(--text));
    }
  }

  @media print {
    .dr,
    .dr-tip {
      display: none !important;
    }
  }

  /* The ruler's own scroller: never chains into the page, no scrollbar - the
     fade at the bottom and the wheel are the affordance. The rows under the
     reader are kept in place by hand when midnight adds a day at the top, so
     the browser's own scroll anchoring would move them twice. */
  .dr-scroll {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-x: hidden;
    overflow-y: auto;
    overflow-anchor: none;
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

  /* Headers stick to the top of the ruler. */
  .dr-yhead,
  .dr-mhead {
    position: sticky;
    top: 0;
    height: var(--dr-head);
    display: flex;
    align-items: flex-end;
    white-space: nowrap;
    background: var(--bg);
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
    padding: 0 0 0.35rem calc(var(--dr-label-x) - 4px);
    border-radius: 4px;
    font-size: var(--dr-font);
    font-weight: 600;
    line-height: 1;
    color: var(--dr-strong);
    cursor: pointer;
  }

  .dr-mbtn:hover {
    color: var(--dr-lit);
  }

  .dr-mbtn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }

  /* A past year: its own row above its months, a hairline across the ruler. */
  .dr-yhead {
    z-index: 3;
    padding: 0 0 0.35rem calc(var(--dr-label-x) - 4px);
    font-size: 0.8125rem;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    color: var(--dr-strong);
    background:
      linear-gradient(var(--border), var(--border)) 0 0 / 100% 1px no-repeat,
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
    inset: 0;
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
    --dr-len: var(--dr-tick-day);
    --dr-op: 0.22;
    all: unset;
    box-sizing: border-box;
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    width: 100%;
    height: 100%;
    padding: 0 var(--dr-ticks) 0 var(--dr-label-x);
    border-radius: 4px;
    font-size: var(--dr-font);
    line-height: 1;
    white-space: nowrap;
    color: var(--dr-label);
    cursor: pointer;
  }

  /* The tick, flush with the right edge of the window. Its length is the
     calendar, its strength whether the day has entries; the days the feed shows
     light up in the accent, and the one at the reading line glows. */
  .dr-day::before {
    content: "";
    position: absolute;
    right: 0;
    top: calc(50% - var(--dr-tick-h) / 2);
    width: var(--dr-len);
    height: var(--dr-tick-h);
    border-radius: 1px 0 0 1px;
    background: var(--dr-tick);
    opacity: var(--dr-op);
    transition:
      width 180ms ease,
      opacity 180ms ease,
      background-color 180ms ease,
      box-shadow 180ms ease;
  }

  .dr-day.rec {
    --dr-op: 0.45;
  }

  .dr-day.mon {
    --dr-len: var(--dr-tick-week);
  }

  .dr-day.first {
    --dr-len: var(--dr-tick-month);
  }

  .dr-day:global([data-vis])::before {
    background: var(--dr-lit);
    opacity: 0.8;
  }

  .dr-day:global([aria-current])::before {
    opacity: 1;
    box-shadow: 0 0 4px var(--dr-lit);
  }

  /* The row under the pointer, or keyboard focus: its tick reaches out and
     comes up to full strength. */
  .dr-day:hover::before,
  .dr-day:focus-visible::before {
    width: calc(var(--dr-len) + var(--dr-tick-grow));
    opacity: 1;
  }

  @media (prefers-reduced-motion: reduce) {
    .dr-day::before {
      transition: none;
    }
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

  /* The day header's first figure, right-aligned against the ticks like the
     feed's totals. Muted, never accented; only on days with entries. */
  .dr-dur {
    flex: none;
    margin-left: auto;
    padding-left: var(--dr-dur-gap);
    font-family: var(--mono);
    font-size: var(--dr-dur-font);
    font-variant-numeric: tabular-nums;
    color: var(--dr-label);
  }

  /* Between 85 and 88rem the margin holds the dates and the ticks but not the
     times: they stay in the tooltip. */
  @media (width < 88rem) {
    .dr-dur {
      display: none;
    }
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

  .dr-day:hover .dr-wd,
  .dr-day:hover .dr-n,
  .dr-day:hover .dr-dur {
    color: var(--dr-strong);
  }

  .dr-day:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }

  /* Shown when the lit ticks are scrolled out of the ruler: says where they are. */
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

  /* The part of a row between the stuck headers and the chip. */
  .dr-return::before {
    content: "";
    position: absolute;
    left: -3px;
    right: -3px;
    bottom: calc(100% + 1px);
    height: var(--dr-cover, 0px);
    background: var(--bg);
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
    /* Opacity and the glow are the ordinary marks; here every tick is solid
       and the day at the reading line reaches out instead. */
    .dr-day::before {
      background: CanvasText;
      opacity: 1;
    }

    .dr-day:global([data-vis])::before {
      background: Highlight;
    }

    .dr-day:global([aria-current])::before {
      width: calc(var(--dr-len) + var(--dr-tick-grow));
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
