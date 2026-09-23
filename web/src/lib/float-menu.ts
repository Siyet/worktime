// A row's popover (the project or tags menu) is positioned absolutely below its
// trigger. Inside the Timer page's pinned card of running timers that is not
// enough: the card is sticky and scrolls its rows once they outgrow its height
// cap, and a scrolling card clips everything positioned inside it. There the menu
// is lifted out of the clip by switching it to a fixed position at the exact spot
// its CSS placed it, and it follows the trigger while open. Menus anywhere else
// keep their plain absolute placement and scroll with the page as before.

/** Svelte attachment for a popover element whose CSS positions it absolutely. */
export function floatOutOfClip(menu: HTMLElement): () => void {
  const anchor = menu.parentElement;
  let offsetTop = 0;
  let offsetLeft = 0;
  let floating = false;

  const place = (): void => {
    if (!floating || anchor === null) return;
    const box = anchor.getBoundingClientRect();
    menu.style.top = `${box.top + offsetTop}px`;
    menu.style.left = `${box.left + offsetLeft}px`;
  };

  // Decides afresh from the CSS placement, so a resize across a breakpoint where
  // the menu becomes a fixed bottom sheet hands it back to the stylesheet.
  const decide = (): void => {
    menu.style.removeProperty("position");
    menu.style.removeProperty("top");
    menu.style.removeProperty("left");
    floating = anchor !== null && getComputedStyle(menu).position === "absolute" && confined(anchor);
    if (!floating || anchor === null) return;
    const box = anchor.getBoundingClientRect();
    const own = menu.getBoundingClientRect();
    offsetTop = own.top - box.top;
    offsetLeft = own.left - box.left;
    menu.style.position = "fixed";
    place();
  };

  decide();
  // Capture sees the card's own scroll as well as the page's.
  window.addEventListener("scroll", place, { capture: true, passive: true });
  window.addEventListener("resize", decide);
  return () => {
    window.removeEventListener("scroll", place, { capture: true });
    window.removeEventListener("resize", decide);
  };
}

/** Whether an ancestor may clip the element: a sticky or fixed box, or a scroller. */
function confined(element: HTMLElement): boolean {
  for (let node = element.parentElement; node !== null && node !== document.body; node = node.parentElement) {
    const style = getComputedStyle(node);
    if (style.position === "sticky" || style.position === "fixed") return true;
    if (style.overflowX !== "visible" || style.overflowY !== "visible") return true;
  }
  return false;
}
