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
// has none, and the tests only run Chromium, which would hide every mistake.
// .feed carries overflow-anchor: none so the browser never corrects the same
// shift a second time.
//
// A correction is an instant scroll, and an instant scroll cancels a smooth one in
// progress - Home, a tap on the iOS status bar, a fling. Corrections come from
// remembered heights that went stale: the window changed width, or a sync changed
// a day while it was unmounted. So stale days above the window are re-measured in
// place while the reader is idle, a few per frame, before scrolling ever reaches
// them.
import {
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

/** How long after the reader's last scroll the page counts as idle. */
const IDLE_MS = 250;

interface Anchor {
  element: Element;
  /** The day wrapper around it, held instead when a sync rebuilds the row itself. */
  day: Element | null;
  /** Document coordinates, so scrolling between capture and check changes nothing. */
  top: number;
  dayTop: number;
  /** The reading line when it was captured: the pinned card's bottom, or 0. */
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
  /** Zero-height marker at the pinned card's place in the flow. */
  sentinel: () => HTMLElement | null;
  pinned: () => HTMLElement | null;
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

  #days: () => FeedDay[];
  /** Last measurement per day; survives the day being unmounted. */
  #heights = new Map<string, Measurement>();
  #signatures: { days: FeedDay[]; byDay: Map<string, string> } | null = null;
  #elements: FeedElements | null = null;
  #anchor: Anchor | null = null;
  #frame = 0;
  #idleTimer: ReturnType<typeof setTimeout> | undefined;
  #lastReaderScroll = 0;
  /** Where our own last correction scrolled to, so its scroll event is not the reader's. */
  #ownScrollTarget: number | null = null;
  #scrollPadding = "";
  /** The padding the stuck card asks for, whether or not it is applied right now. */
  #stuckPadding = "";
  #focusInPinned = false;
  #observer: ResizeObserver | null = null;

  constructor(days: () => FeedDay[]) {
    this.#days = days;
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
    window.addEventListener("scroll", this.#onScroll, { passive: true });
    window.addEventListener("resize", this.schedule);
    document.addEventListener("focusin", this.#onFocusChange);
    document.addEventListener("focusout", this.#onFocusChange);
    this.schedule();
    return () => {
      window.removeEventListener("scroll", this.#onScroll);
      window.removeEventListener("resize", this.schedule);
      document.removeEventListener("focusin", this.#onFocusChange);
      document.removeEventListener("focusout", this.#onFocusChange);
      this.#observer?.disconnect();
      this.#observer = null;
      cancelAnimationFrame(this.#frame);
      this.#frame = 0;
      clearTimeout(this.#idleTimer);
      this.#elements = null;
      this.#setScrollPadding("");
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

  #onScroll = (): void => {
    if (this.#ownScrollTarget !== null && Math.abs(window.scrollY - this.#ownScrollTarget) < 1) {
      this.#ownScrollTarget = null;
    } else {
      this.#lastReaderScroll = performance.now();
    }
    this.schedule();
  };

  #update = (): void => {
    this.#frame = 0;
    const elements = this.#elements;
    if (elements === null) return;
    const days = this.#days();
    // A change made outside a frame - a sync merge, Stop in the pinned card - is
    // already laid out by now, and the ResizeObserver only reports it after this
    // callback. Correct against the old anchor before measuring anything.
    this.#keepAnchor();
    const feed = elements.feed;
    const width = feed.clientWidth;
    const feedTop = feed.getBoundingClientRect().top;
    const current = resolveWindow(days, this.keys);
    const heightOf = (day: FeedDay) => this.#heights.get(day.iso)?.height ?? null;
    const next = planFeedWindow({
      window: current,
      total: days.length,
      heights: days.slice(0, current.loaded).map(heightOf),
      viewTop: -feedTop,
      viewBottom: window.innerHeight - feedTop,
    });
    // Gaps come from the heights of the next window: days that are about to be
    // mounted leave the gap in the same flush that mounts them.
    const offsets = dayOffsets(days.slice(0, next.loaded).map(heightOf));
    const stale = this.#staleSpan(days, next.first, width);
    const probe = stale === null ? null : this.#whenIdle(stale);
    const gapAbove = offsets[probe?.first ?? next.first] ?? 0;
    const gapTop = probe === null ? 0 : (offsets[next.first] ?? 0) - (offsets[probe.last + 1] ?? 0);
    const gapBottom = (offsets[next.loaded] ?? 0) - (offsets[next.last + 1] ?? 0);

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
    // Still the layout from before the writes above: Svelte applies them in a
    // microtask after this callback, so the ResizeObserver compares the mounts and
    // unmounts they cause against this position.
    this.#anchor = this.#findAnchor(feed, line);
  };

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
    const quietFor = performance.now() - this.#lastReaderScroll;
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
  // viewport, or the bottom of the pinned card while it is stuck: what the reader
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
    this.#keepAnchor();
    this.schedule();
  };

  // Runs after layout and before paint, so the jump it undoes is never shown.
  // The anchor stays where it was on screen - unless the pinned card grew over it,
  // in which case it moves down with the card's bottom edge instead of being
  // buried: repeating a task deep in the feed must not hide the row just clicked.
  #keepAnchor(): void {
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
    const correction = shift - Math.max(0, line - anchor.line);
    anchor.top += shift;
    anchor.dayTop += shift;
    anchor.line = line;
    if (Math.abs(correction) < 0.5) return;
    const target = window.scrollY + correction;
    this.#ownScrollTarget = target;
    window.scrollTo({ top: target, behavior: "instant" });
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

  // Returns the reading line: the pinned card's bottom while it is stuck.
  #updatePinned(): number {
    const stuckRect = this.#pinnedRect();
    const stuck = stuckRect !== null;
    if (stuck !== this.stuck) this.stuck = stuck;
    this.#stuckPadding = stuck ? `${Math.ceil(stuckRect.bottom)}px` : "";
    this.#setScrollPadding(this.#focusInPinned ? "" : this.#stuckPadding);
    return stuckRect?.bottom ?? 0;
  }

  // While focus is inside the pinned card the padding is lifted. Its controls sit
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
  // stuck card. --pinned-offset lets the card's own controls cancel it out: they
  // sit inside the padded strip by design, and without the negative scroll margin
  // they get (see TimerPage) focusing Stop would scroll the page to "reveal" it.
  #setScrollPadding(padding: string): void {
    if (padding === this.#scrollPadding) return;
    this.#scrollPadding = padding;
    const root = document.documentElement.style;
    root.scrollPaddingTop = padding;
    if (padding === "") root.removeProperty("--pinned-offset");
    else root.setProperty("--pinned-offset", padding);
  }
}
