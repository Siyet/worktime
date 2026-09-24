// The DOM side of the Timer feed: which days are mounted, how tall the ones that
// are not would be, and keeping the reader's place while any of that changes.
//
// Two rules keep it from fighting itself:
//  - Only the animation-frame update writes state that changes layout (the
//    window keys, the probe and the gaps). The ResizeObserver callback records
//    heights and corrects the scroll position, nothing else: layout changed from
//    inside that callback makes the browser report "ResizeObserver loop completed
//    with undelivered notifications", and a Svelte flush runs right after it.
//  - Every trigger - scroll, resize, a measured height, new data - only schedules
//    that one update, so nothing reruns itself inside a single flush.
//
// Scroll anchoring is done here by hand rather than left to the browser: Safari
// has none, and Chromium's own would hide every mistake made here from the tests.
// The browser's is switched off for the whole document while the scroller runs,
// so it never corrects the same shift a second time. That has to be on the body:
// Chromium still anchors the page when only html, main or the feed opt out, and
// a running card growing above the reader got corrected twice.
//
// A correction is an instant scroll, and an instant scroll cancels a smooth one in
// progress - Home, a tap on the iOS status bar, a fling. Corrections come from
// remembered heights that went stale: the window changed width, or a sync changed
// a day while it was unmounted. So stale days above the window are re-measured in
// place while the reader is idle, a few per frame, before scrolling ever reaches
// them. Idle means no scroll and no input that starts one: a wheel turn, a fling
// or Home begins on the compositor, and its first scroll event can arrive only
// after the frame that would have mounted a probe. What remains is the moment
// right after a width change: a scroll to the top started before the stale days
// are re-measured - a few hundred milliseconds for a year of history - can still
// stop short, and a second Home finishes it.
import { tick } from "svelte";
import {
  FEED_CHUNK_DAYS,
  dayOffsets,
  daySignature,
  planFeedWindow,
  probeSpan,
  resolveSpan,
  resolveWindow,
  windowKeys,
  type FeedDay,
  type FeedWindowKeys,
} from "./feed";
import type { FeedSpot } from "./ruler";

/** How long after the reader's last scroll or scrolling input the page counts as idle. */
const IDLE_MS = 250;

/** Keys that scroll the page; any other key leaves the reader idle. */
const SCROLL_KEYS = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "]);
/** The scroll keys the scroller pages with itself while running timers are pinned. */
const PAGE_KEYS = new Set(["PageUp", "PageDown", " "]);

/** Input that may start a scroll before the first scroll event reports it. */
const READER_INPUTS = ["wheel", "touchstart", "touchmove", "pointerdown", "keydown"] as const;

/** A page step keeps this much of the previous page on screen, as Chromium's own does. */
const PAGE_OVERLAP = 40;
/** A page key pressed within this long of the last one continues from its target. */
const PAGE_REPEAT_MS = 1000;

/** Controls that take page keys and Space for themselves. */
const KEEPS_PAGE_KEYS = "input, textarea, select, [contenteditable], dialog, [role=dialog], [role=listbox]";
/** Where Space presses a control instead of scrolling the page (a link lets it scroll). */
const PRESSES_SPACE = `button, summary, [role=button], ${KEEPS_PAGE_KEYS}`;
/** A panel that scrolls itself - the day ruler: the wheel and keys there do not scroll the page. */
const OWN_SCROLL = "[data-own-scroll]";
/** A day card's bottom margin, until one is measured: the gap between two cards. */
const DAY_GAP = 16;

/** What of the feed is on screen, for the day ruler. */
export interface FeedVisible {
  /** At the reading line: the strip's bottom while it is stuck, else the window's top. */
  top: FeedSpot;
  bottom: FeedSpot;
  /** The day at the reading line; null above the feed. */
  current: string | null;
}

interface Anchor {
  element: Element;
  /** The day wrapper around it, held instead when a sync rebuilds the row itself. */
  day: Element | null;
  /** Document coordinates, so scrolling between capture and check changes nothing. */
  top: number;
  dayTop: number;
  /** The reading line when it was captured: the pinned strip's bottom, or 0. */
  line: number;
}

interface Measurement {
  height: number;
  /** The feed's width and the day's signature at the time: stale once either moves. */
  width: number;
  signature: string;
}

export interface FeedElements {
  feed: HTMLElement;
  /** Zero-height marker right before the strip's box in the flow. */
  sentinel: () => HTMLElement | null;
  pinned: () => HTMLElement | null;
  /** The full running card above the feed, hidden while the strip is stuck. */
  card?: () => HTMLElement | null;
}

export class FeedScroller {
  keys = $state<FeedWindowKeys>({ first: null, last: null, frontier: null });
  /** Stale days above the window mounted for one frame to be measured again. */
  probe = $state<{ newest: string; oldest: string } | null>(null);
  gapAbove = $state(0);
  gapTop = $state(0);
  gapBottom = $state(0);
  stuck = $state(false);
  /** True while stale days above the window wait to be measured again. */
  measuring = $state(false);
  /** What of the feed is on screen, updated in the frame that plans the window. */
  visible = $state<FeedVisible | null>(null);

  #days: () => FeedDay[];
  /** Last measurement per day; survives the day being unmounted. */
  #heights = new Map<string, Measurement>();
  #signatures: { days: FeedDay[]; byDay: Map<string, string> } | null = null;
  #elements: FeedElements | null = null;
  #anchor: Anchor | null = null;
  #frame = 0;
  #idleTimer: ReturnType<typeof setTimeout> | undefined;
  #readerActiveAt = 0;
  /** The reader's last actual scroll - a scroll event, the wheel or a scrolling key, not a click. */
  #scrolledAt = 0;
  /** Where our own last correction scrolled to, so its scroll event is not the reader's. */
  #ownScrollTarget: number | null = null;
  /** Where the scroller last put the page, unrounded: WebKit keeps whole pixels. */
  #placedAt: number | null = null;
  /** The page's scroll position as a scroll event, a placement or a captured anchor last saw it. */
  #seenScroll: number | null = null;
  #scrollPadding = "";
  /** The padding the stuck card asks for, whether or not it is applied right now. */
  #stuckPadding = "";
  #focusInPinned = false;
  /** The hidden running card, held at its height while the page scrolls. */
  #heldCard: HTMLElement | null = null;
  #releaseTimer: ReturnType<typeof setTimeout> | undefined;
  /** Where the last page key is scrolling to, so a quick repeat continues from there. */
  #page: { target: number; at: number } | null = null;
  #observer: ResizeObserver | null = null;
  #dayGap: number | null = null;

  constructor(days: () => FeedDay[]) {
    this.#days = days;
  }

  /**
   * When the reader last scrolled the page (performance.now), not counting the
   * scroller's own corrections or input inside a panel that scrolls itself.
   */
  get scrolledAt(): number {
    return this.#scrolledAt;
  }

  /** The mounted range, resolved from the keys against the current days. */
  range = $derived.by(() => resolveWindow(this.#days(), this.keys));

  /** The probe as indices, never overlapping the window. */
  probeRange = $derived.by(() => {
    if (this.probe === null) return null;
    const span = resolveSpan(this.#days(), this.probe.newest, this.probe.oldest);
    const last = Math.min(span.last, this.range.first - 1);
    return span.first <= last ? { first: span.first, last } : null;
  });

  start(elements: FeedElements): () => void {
    this.#elements = elements;
    this.#observer = new ResizeObserver(this.#onResize);
    // The body resizes on any layout change anywhere above the reader, including
    // ones outside this page such as the header wrapping.
    this.#observer.observe(document.body);
    for (const day of elements.feed.querySelectorAll<HTMLElement>(":scope > .day")) this.#observer.observe(day);
    const body = document.body.style;
    const anchoring = body.overflowAnchor;
    body.overflowAnchor = "none";
    window.addEventListener("scroll", this.#onScroll, { passive: true });
    for (const type of READER_INPUTS) window.addEventListener(type, this.#onReaderInput, { capture: true, passive: true });
    window.addEventListener("resize", this.schedule);
    window.addEventListener("keydown", this.#onKeydown);
    document.addEventListener("focusin", this.#onFocusChange);
    document.addEventListener("focusout", this.#onFocusChange);
    this.schedule();
    return () => {
      window.removeEventListener("scroll", this.#onScroll);
      for (const type of READER_INPUTS) window.removeEventListener(type, this.#onReaderInput, { capture: true });
      window.removeEventListener("resize", this.schedule);
      window.removeEventListener("keydown", this.#onKeydown);
      document.removeEventListener("focusin", this.#onFocusChange);
      document.removeEventListener("focusout", this.#onFocusChange);
      this.#observer?.disconnect();
      this.#observer = null;
      cancelAnimationFrame(this.#frame);
      this.#frame = 0;
      clearTimeout(this.#idleTimer);
      clearTimeout(this.#releaseTimer);
      this.#releaseCard();
      this.#elements = null;
      this.#setScrollPadding("");
      body.overflowAnchor = anchoring;
    };
  }

  /** Attachment for a day wrapper; a stable function, so it never re-attaches. */
  observeDay = (element: HTMLElement): (() => void) => {
    this.#observer?.observe(element);
    return () => this.#observer?.unobserve(element);
  };

  schedule = (): void => {
    if (this.#frame === 0 && this.#elements !== null) this.#frame = requestAnimationFrame(this.#update);
  };

  /**
   * Mounts a day wherever it is in the feed and scrolls it to the reading line,
   * so a control inside it can take focus. Resolves once the day is on screen.
   */
  async reveal(iso: string): Promise<void> {
    const elements = this.#elements;
    if (elements === null) return;
    const days = this.#days();
    const index = days.findIndex((day) => day.iso === iso);
    if (index < 0) return;
    const current = resolveWindow(days, this.keys);
    if (index < current.first || index > current.last) {
      // Just this day, with the same gaps the planner would leave around it; the
      // next frame plans the rest of the window from the new scroll position.
      const loaded = Math.max(current.loaded, Math.min(days.length, index + FEED_CHUNK_DAYS));
      const offsets = dayOffsets(days.slice(0, loaded).map((day) => this.#heights.get(day.iso)?.height ?? null));
      this.#anchor = null;
      this.keys = windowKeys(days, { first: index, last: index, loaded });
      this.probe = null;
      this.gapAbove = offsets[index]!;
      this.gapTop = 0;
      this.gapBottom = offsets[loaded]! - offsets[index + 1]!;
      await tick();
      // The page may have gone away in the meantime.
      if (this.#elements !== elements) return;
    }
    const day = elements.feed.querySelector<HTMLElement>(`:scope > .day[data-key="${iso}"]`);
    if (day === null) return;
    this.#anchor = null;
    const line = this.#stuckLine();
    this.#place(day.getBoundingClientRect().top + window.scrollY - line);
    this.schedule();
  }

  /** The top of the page: the start form, the running timers and today. */
  revealTop(): void {
    this.#anchor = null;
    this.#place(0);
    this.schedule();
  }

  #place(top: number): void {
    this.#placedAt = top;
    window.scrollTo({ top, behavior: "instant" });
    this.#seenScroll = this.#scrollBase();
  }

  // Where the page is, as far as the next correction is concerned. WebKit drops
  // the fraction of every scroll position, so a correction measured from the
  // page's own scrollY would lose up to a pixel each time, and a day that is
  // remeasured ten times would end up several pixels off. While the page is
  // still where the scroller put it, give or take that fraction, the position
  // it meant counts instead.
  #scrollBase(): number {
    const actual = window.scrollY;
    const placed = this.#placedAt;
    return placed !== null && Number.isInteger(actual) && Math.abs(actual - placed) < 1 ? placed : actual;
  }

  #onScroll = (): void => {
    this.#seenScroll = this.#scrollBase();
    if (this.#page !== null && Math.abs(window.scrollY - this.#page.target) < 1) this.#page = null;
    if (this.#ownScrollTarget !== null && Math.abs(window.scrollY - this.#ownScrollTarget) < 1) {
      this.#ownScrollTarget = null;
    } else {
      this.#readerActiveAt = performance.now();
      this.#scrolledAt = this.#readerActiveAt;
    }
    this.schedule();
  };

  #onReaderInput = (event: Event): void => {
    if (event instanceof KeyboardEvent && !SCROLL_KEYS.has(event.key)) return;
    if (event.target instanceof Element && event.target.closest(OWN_SCROLL) !== null) return;
    // Any other scroll moves the page away from where the last page step was
    // heading, so the next page key starts from wherever the page is.
    if (!(event instanceof KeyboardEvent) || !PAGE_KEYS.has(event.key)) this.#page = null;
    this.#readerActiveAt = performance.now();
    // A press or a tap may still become a scroll, which is enough to hold the
    // probe back, but it is not one: Repeat and Undo are pressed.
    const pressed =
      event.type === "pointerdown" ||
      event.type === "touchstart" ||
      (event instanceof KeyboardEvent &&
        event.key === " " &&
        event.target instanceof Element &&
        event.target.closest(PRESSES_SPACE) !== null);
    if (pressed) return;
    this.#scrolledAt = this.#readerActiveAt;
    // The frame this asks for holds the hidden card before a scroll event
    // would, in case a timer arrives between the two.
    if (this.stuck && this.#heldCard === null) this.schedule();
  };

  // A page step under the stuck strip is a page less the strip, as the scroll
  // padding asks - but WebKit ignores that padding for page keys, and a full page
  // would carry the next unread rows beneath the strip. So the step is taken here,
  // in every engine, whenever the page itself is what the key would scroll. Also
  // before the strip sticks: the step that sticks it must not bury rows either.
  #onKeydown = (event: KeyboardEvent): void => {
    const pinned = this.#elements?.pinned() ?? null;
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || pinned === null) return;
    const direction =
      event.key === "PageDown" || (event.key === " " && !event.shiftKey)
        ? 1
        : event.key === "PageUp" || (event.key === " " && event.shiftKey)
          ? -1
          : 0;
    if (direction === 0) return;
    const active = document.activeElement;
    const nothingFocused = active === null || active === document.body || active === document.documentElement;
    // Space presses a focused control; page keys belong to fields, dialogs and
    // an open overlay of the strip while it can still scroll that way.
    if (!nothingFocused) {
      if (event.key === " " || active.closest(KEEPS_PAGE_KEYS) !== null) return;
      const overlay = pinned.contains(active) ? active.closest("[data-pin-scroll]") : null;
      if (overlay !== null) {
        const room = direction > 0 ? overlay.scrollHeight - overlay.clientHeight - overlay.scrollTop : overlay.scrollTop;
        if (room > 1) return;
      }
    }
    event.preventDefault();
    const visible = window.innerHeight - this.#stuckLine();
    const step = Math.max(visible * 0.875, visible - PAGE_OVERLAP);
    const now = performance.now();
    const from = this.#page !== null && now - this.#page.at < PAGE_REPEAT_MS ? this.#page.target : window.scrollY;
    const bottom = document.documentElement.scrollHeight - window.innerHeight;
    const target = Math.min(bottom, Math.max(0, from + direction * step));
    this.#page = { target, at: now };
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    window.scrollTo({ top: target, behavior: reduced ? "instant" : "smooth" });
  };

  #update = (): void => {
    this.#frame = 0;
    const elements = this.#elements;
    if (elements === null) return;
    const days = this.#days();
    // Released before the correction below, so that it is the correction that
    // absorbs whatever the held card grew or shrank by - and at the latest in the
    // frame the page unsticks, read fresh rather than from the last frame, so the
    // card never shows cut to its old height. When it unsticks because the card
    // is coming into view, the anchor from the stuck frame is dropped: the card
    // takes its new height in view, as any change above the feed does once the
    // strip lets go, because a correction would stop the reader's scroll -
    // Safari's status-bar tap would end at the card instead of the top. A card
    // that went away with its last timer is still corrected for.
    if (this.#heldCard !== null) {
      const unstuck = this.#pinnedRect() === null;
      if (unstuck || this.#quietFor() >= IDLE_MS) {
        if (unstuck && this.#heldCard.isConnected) this.#anchor = null;
        this.#releaseCard();
      }
    }
    // A change made outside a frame - a sync merge, Stop in the pinned strip - is
    // already laid out by now, and the ResizeObserver only reports it after this
    // callback. Correct against the old anchor before measuring anything.
    this.#keepAnchor(true);
    const feed = elements.feed;
    const width = feed.clientWidth;
    const feedTop = feed.getBoundingClientRect().top;
    const current = resolveWindow(days, this.keys);
    const heightOf = (day: FeedDay) => this.#heights.get(day.iso)?.height ?? null;
    const heights = days.slice(0, current.loaded).map(heightOf);
    // The viewport in the terms of the offsets about to be planned with, which
    // are not always the ones the page was laid out with: measuring a day moves
    // the estimate every unmeasured day above it stands in for, and after a far
    // jump that is a thousand days moving a few pixels each. Planned from the page
    // alone, the window would leave the day the reader is at; this way the gaps
    // written from the new offsets move that day, and the anchor takes it back.
    const drift = this.#drift(feed, days, dayOffsets(heights), feedTop);
    const next = planFeedWindow({
      window: current,
      total: days.length,
      heights,
      viewTop: drift - feedTop,
      viewBottom: drift + window.innerHeight - feedTop,
    });
    // Gaps come from the heights of the next window: days that are about to be
    // mounted leave the gap in the same flush that mounts them.
    const offsets = dayOffsets(days.slice(0, next.loaded).map(heightOf));
    const stale = this.#staleSpan(days, next.first, width);
    const probe = stale === null ? null : this.#whenIdle(stale);
    const gapAbove = offsets[probe?.first ?? next.first] ?? 0;
    const gapTop = probe === null ? 0 : (offsets[next.first] ?? 0) - (offsets[probe.last + 1] ?? 0);
    const gapBottom = (offsets[next.loaded] ?? 0) - (offsets[next.last + 1] ?? 0);
    this.#updateVisible(feed, days, offsets, next.loaded, feedTop - drift);

    const keys = windowKeys(days, next);
    if (keys.first !== this.keys.first || keys.last !== this.keys.last || keys.frontier !== this.keys.frontier) {
      this.keys = keys;
    }
    const probeKeys = probe === null ? null : { newest: days[probe.first]!.iso, oldest: days[probe.last]!.iso };
    if (probeKeys?.newest !== this.probe?.newest || probeKeys?.oldest !== this.probe?.oldest) this.probe = probeKeys;
    if (gapAbove !== this.gapAbove) this.gapAbove = gapAbove;
    if (gapTop !== this.gapTop) this.gapTop = gapTop;
    if (gapBottom !== this.gapBottom) this.gapBottom = gapBottom;
    if ((stale !== null) !== this.measuring) this.measuring = stale !== null;
    const line = this.#updatePinned();
    this.#holdCard();
    // Still the layout from before the writes above: Svelte applies them in a
    // microtask after this callback, so the ResizeObserver compares the mounts and
    // unmounts they cause against this position.
    this.#anchor = this.#findAnchor(feed, line);
    this.#seenScroll = this.#scrollBase();
  };

  // Where the reading line and the window's bottom fall in the feed, from the
  // same offsets the window is planned with, so days that are not mounted count
  // at the height their gap gives them. Written only when it changed.
  #updateVisible(feed: HTMLElement, days: FeedDay[], offsets: number[], loaded: number, feedTop: number): void {
    const scrollY = window.scrollY;
    const feedDocTop = feedTop + scrollY;
    const gap = this.#measureDayGap(feed);
    const spot = (docY: number): FeedSpot => {
      const offset = docY - feedDocTop;
      if (offset < 0 || loaded === 0) {
        return { iso: null, fraction: feedDocTop > 0 ? Math.min(1, Math.max(0, docY / feedDocTop)) : 1, gap: false, next: days[0]?.iso ?? null };
      }
      let low = 0;
      let high = loaded - 1;
      while (low < high) {
        const middle = Math.ceil((low + high) / 2);
        if ((offsets[middle] ?? 0) <= offset) low = middle;
        else high = middle - 1;
      }
      const start = offsets[low] ?? 0;
      const height = (offsets[low + 1] ?? start) - start;
      const card = Math.max(0, height - gap);
      const into = offset - start;
      const next = days[low + 1]?.iso ?? null;
      if (into < card) return { iso: days[low]!.iso, fraction: into / card, gap: false, next };
      const fraction = height > card ? Math.min(1, (into - card) / (height - card)) : 1;
      return { iso: days[low]!.iso, fraction, gap: true, next };
    };
    const top = spot(scrollY + this.#readingLine());
    const bottom = spot(scrollY + window.innerHeight);
    // In the gap after a card the reader is looking at the next one: a jump puts
    // a card's top within a pixel of the line, on either side of it.
    const current = top.gap && top.next !== null ? top.next : top.iso;
    const visible: FeedVisible = { top, bottom, current };
    if (this.visible === null || !sameVisible(this.visible, visible)) this.visible = visible;
  }

  // How far a mounted day's planned offset is from where the page has it: the
  // day at the reading line, or else the first one mounted.
  #drift(feed: HTMLElement, days: FeedDay[], offsets: number[], feedTop: number): number {
    const anchored = this.#anchor?.day;
    const day = anchored?.isConnected ? anchored : feed.querySelector(":scope > .day");
    const key = day instanceof HTMLElement ? day.dataset.key : undefined;
    if (day == null || key === undefined) return 0;
    const index = resolveSpan(days, key, key).first;
    if (days[index]?.iso !== key || index >= offsets.length - 1) return 0;
    return offsets[index]! - (day.getBoundingClientRect().top - feedTop);
  }

  // A day wrapper holds its card's bottom margin; the difference is the gap
  // between two cards. Measured once, on the first mounted day.
  #measureDayGap(feed: HTMLElement): number {
    if (this.#dayGap !== null) return this.#dayGap;
    const card = feed.querySelector<HTMLElement>(":scope > .day > .card");
    if (card === null) return DAY_GAP;
    const wrapper = card.parentElement!.getBoundingClientRect().height;
    this.#dayGap = Math.max(0, wrapper - card.getBoundingClientRect().height);
    return this.#dayGap;
  }

  // The next stale days above the window, or null once there are none.
  #staleSpan(days: FeedDay[], first: number, width: number): { first: number; last: number } | null {
    const signatures = this.#daySignatures(days);
    return probeSpan(first, (index) => {
      const day = days[index]!;
      const measured = this.#heights.get(day.iso);
      return measured === undefined || measured.width !== width || measured.signature !== signatures.get(day.iso);
    });
  }

  // A probe is mounted for one frame only - the ResizeObserver has measured it by
  // the next - and only while the reader is idle, because mounting it is exactly a
  // stale remount: the correction it causes would cancel a scroll in progress.
  #whenIdle(span: { first: number; last: number }): { first: number; last: number } | null {
    const quietFor = performance.now() - this.#readerActiveAt;
    if (quietFor >= IDLE_MS) return span;
    // Come back once the reader stops; no scroll event will ask again.
    clearTimeout(this.#idleTimer);
    this.#idleTimer = setTimeout(this.schedule, IDLE_MS - quietFor);
    return null;
  }

  #daySignatures(days: FeedDay[]): Map<string, string> {
    if (this.#signatures?.days !== days) {
      this.#signatures = { days, byDay: new Map(days.map((day) => [day.iso, daySignature(day)])) };
    }
    return this.#signatures.byDay;
  }

  // The first row whose bottom is below the reading line - the top of the
  // viewport, or the bottom of the pinned strip while it is stuck: what the reader
  // is looking at. Only while that line is inside the feed: above it the start
  // form and the running card are on screen, and a change there is meant to push
  // the feed down in plain view, as it always has.
  #findAnchor(feed: HTMLElement, line: number): Anchor | null {
    if (feed.getBoundingClientRect().top >= line) return null;
    for (const day of feed.querySelectorAll<HTMLElement>(":scope > .day")) {
      const dayRect = day.getBoundingClientRect();
      if (dayRect.bottom <= line) continue;
      for (const row of day.querySelectorAll("[data-anchor]")) {
        const rect = row.getBoundingClientRect();
        if (rect.bottom > line) {
          return { element: row, day, top: rect.top + window.scrollY, dayTop: dayRect.top + window.scrollY, line };
        }
      }
    }
    return null;
  }

  #onResize = (entries: ResizeObserverEntry[]): void => {
    const days = this.#elements === null ? [] : this.#days();
    const signatures = this.#daySignatures(days);
    const width = this.#elements?.feed.clientWidth ?? 0;
    for (const entry of entries) {
      const target = entry.target as HTMLElement;
      const key = target.dataset.key;
      if (key === undefined || !target.isConnected) continue;
      this.#heights.set(key, {
        height: entry.borderBoxSize[0]?.blockSize ?? target.getBoundingClientRect().height,
        width,
        signature: signatures.get(key) ?? "",
      });
    }
    this.#keepAnchor(false);
    this.schedule();
  };

  // Runs after layout and before paint, so the jump it undoes is never shown.
  // The anchor stays where it was on screen - unless the pinned strip grew over
  // it, in which case it moves down with the strip's bottom edge instead of being
  // buried: repeating a task deep in the feed must not hide the row just clicked.
  // Only while the page is not being scrolled, though: that move is a scroll, and
  // a timer arriving by sync mid-fling would cut the fling short. Then the strip
  // simply covers one more line. The click on Repeat or Undo that started the
  // timer is no scroll, so it does not count.
  #keepAnchor(absorb: boolean): void {
    const anchor = this.#anchor;
    if (anchor === null) return;
    let shift: number;
    if (anchor.element.isConnected) {
      shift = anchor.element.getBoundingClientRect().top + window.scrollY - anchor.top;
    } else if (anchor.day?.isConnected) {
      shift = anchor.day.getBoundingClientRect().top + window.scrollY - anchor.dayTop;
    } else {
      return;
    }
    const line = this.#readingLine();
    const still = performance.now() - this.#scrolledAt >= IDLE_MS;
    // While the page scrolls, the held card - hidden above the reader - takes the
    // shift instead of the page: it gives up exactly what was added above, a row
    // landing in today's day, say, so the reader's row stays put without a
    // scroll that would stop theirs. Once scrolling is quiet the card takes its
    // real height and the page the net correction. The card only ever shrinks:
    // growth above the reader bigger than the whole card is left to the scroll,
    // and so is anything above it shrinking - a taller card would pull the strip's
    // stick point down with it, and a scroll that ends near there would unstick the
    // strip, snap the card back and jump the reader's row. So is a shift found
    // inside the ResizeObserver callback: resizing the card there resizes the
    // observed body again, a loop. A sync merge lands outside a frame, so the
    // frame's own pass sees it first.
    const held = this.#heldCard;
    if (absorb && !still && held !== null && held.isConnected) {
      const height = parseFloat(held.style.height) || 0;
      const absorbed = Math.max(Math.min(shift, height), 0);
      if (absorbed >= 0.5) {
        held.style.height = `${height - absorbed}px`;
        shift -= absorbed;
      }
    }
    const correction = shift - (still ? Math.max(0, line - anchor.line) : 0);
    anchor.top += shift;
    anchor.dayTop += shift;
    anchor.line = line;
    if (Math.abs(correction) < 0.5) return;
    // The correction cancels a smooth page step; the next key starts afresh.
    this.#page = null;
    const target = this.#correctionBase() + correction;
    this.#ownScrollTarget = target;
    this.#place(target);
  }

  // Where a correction starts from. A layout that shortens the page under the
  // reader - gaps rewritten after a far jump - has the browser pull the scroll up
  // to the page's new end before any scroll event reports it. The end moved, not
  // the reader, so that pull is part of what the correction undoes: it starts
  // from where the page was.
  #correctionBase(): number {
    const base = this.#scrollBase();
    const seen = this.#seenScroll;
    if (seen === null || base >= seen - 0.5) return base;
    const bottom = document.documentElement.scrollHeight - window.innerHeight;
    return base >= bottom - 1 ? seen : base;
  }

  #pinnedRect(): DOMRect | null {
    const pinned = this.#elements?.pinned() ?? null;
    const sentinel = this.#elements?.sentinel() ?? null;
    if (pinned === null || sentinel === null) return null;
    const rect = pinned.getBoundingClientRect();
    return rect.top - sentinel.getBoundingClientRect().top > 0.5 ? rect : null;
  }

  #readingLine(): number {
    return this.#pinnedRect()?.bottom ?? 0;
  }

  // Where the pinned strip's bottom edge is once it sticks, stuck yet or not: its
  // sticky offset (the safe area) plus its height.
  #stuckLine(): number {
    const pinned = this.#elements?.pinned() ?? null;
    if (pinned === null) return 0;
    return (parseFloat(getComputedStyle(pinned).top) || 0) + pinned.offsetHeight;
  }

  #quietFor(): number {
    return performance.now() - this.#scrolledAt;
  }

  // While the strip is stuck the full card above the reader is invisible, yet a
  // timer arriving by sync still resizes it, and correcting for that is an
  // instant scroll that would stop a fling or a smooth scroll dead. So while the
  // page moves, the card keeps the height it had; it takes its real one, and the
  // reader's row its correction, once scrolling has been quiet for IDLE_MS.
  // Holding changes no layout, so it is safe at any point in a frame.
  #holdCard(): void {
    const card = this.#elements?.card?.() ?? null;
    if (card !== this.#heldCard) this.#releaseCard();
    if (card === null || !this.stuck) return;
    const quiet = this.#quietFor();
    if (quiet >= IDLE_MS) return;
    if (this.#heldCard === null) {
      card.style.height = `${card.getBoundingClientRect().height}px`;
      card.style.overflow = "clip";
      this.#heldCard = card;
    }
    clearTimeout(this.#releaseTimer);
    this.#releaseTimer = setTimeout(this.schedule, IDLE_MS - quiet);
  }

  #releaseCard(): void {
    const card = this.#heldCard;
    if (card === null) return;
    this.#heldCard = null;
    card.style.removeProperty("height");
    card.style.removeProperty("overflow");
    this.schedule();
  }

  // Returns the reading line: the pinned strip's bottom while it is stuck. The
  // pinned element is the strip's zero-height box, so its rect's bottom is its
  // top, which is where the strip ends.
  #updatePinned(): number {
    const stuckRect = this.#pinnedRect();
    const stuck = stuckRect !== null;
    if (stuck !== this.stuck) this.stuck = stuck;
    this.#stuckPadding = stuck ? `${Math.ceil(stuckRect.bottom)}px` : "";
    // WebKit fires no focusout when the focused control is removed - a Stop that
    // stopped its own timer - so every frame reads where focus actually is.
    const pinned = this.#elements?.pinned() ?? null;
    this.#focusInPinned = pinned !== null && pinned.contains(document.activeElement);
    this.#setScrollPadding(this.#focusInPinned ? "" : this.#stuckPadding);
    return stuckRect?.bottom ?? 0;
  }

  // While focus is inside the pinned strip the padding is lifted. Its controls sit
  // in the padded strip by design, and WebKit scrolls the page to "reveal" a
  // focused Stop there even with the negative scroll margin that settles it for
  // Chromium. Focus events fire before the browser scrolls the focused element
  // into view, so lifting the padding here is in time for that scroll.
  #onFocusChange = (event: FocusEvent): void => {
    const pinned = this.#elements?.pinned() ?? null;
    const focused = event.type === "focusin" ? event.target : event.relatedTarget;
    this.#focusInPinned = pinned !== null && focused instanceof Node && pinned.contains(focused);
    this.#setScrollPadding(this.#focusInPinned ? "" : this.#stuckPadding);
  };

  // The padding keeps PageDown and focus scrolling from moving rows under the
  // stuck strip. --pinned-offset lets the strip's own controls cancel it out: they
  // sit inside the padded band by design, and without the negative scroll margin
  // they get (see PinnedStrip) focusing Stop would scroll the page to "reveal" it.
  #setScrollPadding(padding: string): void {
    if (padding === this.#scrollPadding) return;
    this.#scrollPadding = padding;
    const root = document.documentElement.style;
    root.scrollPaddingTop = padding;
    if (padding === "") root.removeProperty("--pinned-offset");
    else root.setProperty("--pinned-offset", padding);
  }
}

function sameSpot(left: FeedSpot, right: FeedSpot): boolean {
  return (
    left.iso === right.iso &&
    left.gap === right.gap &&
    left.next === right.next &&
    Math.abs(left.fraction - right.fraction) < 1e-4
  );
}

function sameVisible(left: FeedVisible, right: FeedVisible): boolean {
  return left.current === right.current && sameSpot(left.top, right.top) && sameSpot(left.bottom, right.bottom);
}
