// The DOM side of the Timer feed: which days are mounted, how tall the ones that
// are not would be, and keeping the reader's place while any of that changes.
//
// Two rules keep it from fighting itself:
//  - Only the animation-frame update writes state that changes layout (the
//    window keys and the two gaps). The ResizeObserver callback records heights
//    and corrects the scroll position, nothing else: layout changed from inside
//    that callback makes the browser report "ResizeObserver loop completed with
//    undelivered notifications", and a Svelte flush runs right after it.
//  - Every trigger - scroll, resize, a measured height, new data - only schedules
//    that one update, so nothing reruns itself inside a single flush.
//
// Scroll anchoring is done here by hand rather than left to the browser: Safari
// has none, and the tests only run Chromium, which would hide every mistake.
// .feed carries overflow-anchor: none so the browser never corrects the same
// shift a second time.
import { dayOffsets, planFeedWindow, resolveWindow, windowKeys, type FeedDay, type FeedWindowKeys } from "./feed";

interface Anchor {
  element: Element;
  /** Document coordinate, so scrolling between capture and check changes nothing. */
  top: number;
}

export interface FeedElements {
  feed: HTMLElement;
  /** Zero-height marker at the pinned card's place in the flow. */
  sentinel: () => HTMLElement | null;
  pinned: () => HTMLElement | null;
}

export class FeedScroller {
  keys = $state<FeedWindowKeys>({ first: null, last: null, frontier: null });
  gapTop = $state(0);
  gapBottom = $state(0);
  stuck = $state(false);

  #days: () => FeedDay[];
  /** Last measured height per day; survives the day being unmounted. */
  #heights = new Map<string, number>();
  #elements: FeedElements | null = null;
  #anchor: Anchor | null = null;
  #frame = 0;
  #scrollPadding = "";
  #observer: ResizeObserver | null = null;

  constructor(days: () => FeedDay[]) {
    this.#days = days;
  }

  /** The mounted range, resolved from the keys against the current days. */
  range = $derived.by(() => resolveWindow(this.#days(), this.keys));

  start(elements: FeedElements): () => void {
    this.#elements = elements;
    this.#observer = new ResizeObserver(this.#onResize);
    // The body resizes on any layout change anywhere above the reader, including
    // ones outside this page such as the header wrapping.
    this.#observer.observe(document.body);
    for (const day of elements.feed.querySelectorAll<HTMLElement>(":scope > .day")) this.#observer.observe(day);
    window.addEventListener("scroll", this.schedule, { passive: true });
    window.addEventListener("resize", this.schedule);
    this.schedule();
    return () => {
      window.removeEventListener("scroll", this.schedule);
      window.removeEventListener("resize", this.schedule);
      this.#observer?.disconnect();
      this.#observer = null;
      cancelAnimationFrame(this.#frame);
      this.#frame = 0;
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

  #update = (): void => {
    this.#frame = 0;
    const elements = this.#elements;
    if (elements === null) return;
    const days = this.#days();
    // A change made outside a frame - a sync merge, Stop in the pinned card - is
    // already laid out by now, and the ResizeObserver only reports it after this
    // callback. Correct against the old anchor before measuring anything.
    this.#keepAnchor();
    const feedTop = elements.feed.getBoundingClientRect().top;
    const current = resolveWindow(days, this.keys);
    const heights = days.slice(0, current.loaded).map((day) => this.#heights.get(day.iso) ?? null);
    const next = planFeedWindow({
      window: current,
      total: days.length,
      heights,
      viewTop: -feedTop,
      viewBottom: window.innerHeight - feedTop,
    });
    // Gaps come from the heights of the next window: days that are about to be
    // mounted leave the gap in the same flush that mounts them.
    const nextHeights =
      next.loaded === heights.length
        ? heights
        : days.slice(0, next.loaded).map((day) => this.#heights.get(day.iso) ?? null);
    const offsets = dayOffsets(nextHeights);
    const gapTop = offsets[next.first] ?? 0;
    const gapBottom = (offsets[next.loaded] ?? 0) - (offsets[next.last + 1] ?? 0);
    const keys = windowKeys(days, next);
    if (keys.first !== this.keys.first || keys.last !== this.keys.last || keys.frontier !== this.keys.frontier) {
      this.keys = keys;
    }
    if (gapTop !== this.gapTop) this.gapTop = gapTop;
    if (gapBottom !== this.gapBottom) this.gapBottom = gapBottom;
    const readingLine = this.#updatePinned(elements);
    // Still the layout from before the writes above: Svelte applies them in a
    // microtask after this callback, so the ResizeObserver compares the mounts and
    // unmounts they cause against this position.
    this.#anchor = this.#findAnchor(elements.feed, readingLine);
  };

  // The first row whose bottom is below the reading line - the top of the
  // viewport, or the bottom of the pinned card while it is stuck: what the reader
  // is looking at. Only while that line is inside the feed: above it the start
  // form and the running card are on screen, and a change there is meant to push
  // the feed down in plain view, as it always has.
  #findAnchor(feed: HTMLElement, readingLine: number): Anchor | null {
    if (feed.getBoundingClientRect().top >= readingLine) return null;
    for (const day of feed.querySelectorAll<HTMLElement>(":scope > .day")) {
      if (day.getBoundingClientRect().bottom <= readingLine) continue;
      for (const row of day.querySelectorAll("[data-anchor]")) {
        const rect = row.getBoundingClientRect();
        if (rect.bottom > readingLine) return { element: row, top: rect.top + window.scrollY };
      }
    }
    return null;
  }

  #onResize = (entries: ResizeObserverEntry[]): void => {
    for (const entry of entries) {
      const target = entry.target as HTMLElement;
      const key = target.dataset.key;
      if (key === undefined || !target.isConnected) continue;
      this.#heights.set(key, entry.borderBoxSize[0]?.blockSize ?? target.getBoundingClientRect().height);
    }
    this.#keepAnchor();
    this.schedule();
  };

  // Runs after layout and before paint, so the jump it undoes is never shown.
  #keepAnchor(): void {
    const anchor = this.#anchor;
    if (anchor === null || !anchor.element.isConnected) return;
    const top = anchor.element.getBoundingClientRect().top + window.scrollY;
    const shift = top - anchor.top;
    if (Math.abs(shift) < 0.5) return;
    window.scrollTo({ top: window.scrollY + shift, behavior: "instant" });
    anchor.top = top;
  }

  // Returns the reading line: the pinned card's bottom while it is stuck.
  #updatePinned(elements: FeedElements): number {
    const pinned = elements.pinned();
    const sentinel = elements.sentinel();
    let stuck = false;
    let readingLine = 0;
    if (pinned !== null && sentinel !== null) {
      const rect = pinned.getBoundingClientRect();
      stuck = rect.top - sentinel.getBoundingClientRect().top > 0.5;
      if (stuck) readingLine = rect.bottom;
    }
    if (stuck !== this.stuck) this.stuck = stuck;
    this.#setScrollPadding(stuck ? `${Math.ceil(readingLine)}px` : "");
    return readingLine;
  }

  // The padding keeps PageDown and focus scrolling from moving rows under the
  // stuck card. --pinned-offset lets the card's own controls cancel it out: they
  // sit inside the padded strip by design, and without the negative scroll margin
  // it gets (see TimerPage) focusing Stop would scroll the page to "reveal" it.
  #setScrollPadding(padding: string): void {
    if (padding === this.#scrollPadding) return;
    this.#scrollPadding = padding;
    const root = document.documentElement.style;
    root.scrollPaddingTop = padding;
    if (padding === "") root.removeProperty("--pinned-offset");
    else root.setProperty("--pinned-offset", padding);
  }
}
