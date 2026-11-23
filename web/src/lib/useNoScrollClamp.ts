import { useEffect, useLayoutEffect, useRef } from "react";

/** scrollerOf is the nearest ancestor that scrolls vertically, or the document's. */
function scrollerOf(el: HTMLElement): HTMLElement {
  for (let p = el.parentElement; p; p = p.parentElement) {
    if (/(auto|scroll)/.test(getComputedStyle(p).overflowY)) return p;
  }
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement;
}

/**
 * useNoScrollClamp keeps the view where it was when content at the end of the page gets
 * shorter. Left alone, a page that becomes shorter than the scroll position is clamped by
 * the browser: everything on screen jumps down to meet the new bottom.
 *
 * It returns the ref for the element at the end of the page that shrinks. Each time `change`
 * changes, if the browser clamped, that element gets exactly the padding it took to
 * stay put and the scroll is put back. The padding is below the view, so nothing on screen
 * moves; as the reader scrolls up it is given back as soon as it is out of sight, so no empty
 * space is ever left to look at. Nothing is reserved while no clamp happens.
 */
export function useNoScrollClamp<T extends HTMLElement>(change: unknown) {
  const el = useRef<T>(null);
  const scroller = useRef<HTMLElement | null>(null);
  const last = useRef(0);
  const held = useRef(0);

  const attached = useRef<{ node: HTMLElement; detach: () => void } | null>(null);

  // The element can mount after this hook does (it lives in one tab of the page), and can be
  // replaced, so the scroll listener follows it rather than being set up once.
  useEffect(() => {
    const node = el.current;
    if (attached.current?.node === node) return;
    attached.current?.detach();
    attached.current = null;
    held.current = 0;
    scroller.current = null;
    if (!node) return;
    const sc = scrollerOf(node);
    scroller.current = sc;
    last.current = sc.scrollTop;
    const onScroll = () => {
      // Give back whatever of the held space has gone below the bottom of the view.
      if (held.current > 0) {
        const below = sc.scrollHeight - sc.scrollTop - sc.clientHeight;
        const give = Math.min(held.current, below);
        if (give > 0) {
          held.current -= give;
          node.style.paddingBottom = held.current > 0 ? `${held.current}px` : "";
        }
      }
      last.current = sc.scrollTop;
    };
    sc.addEventListener("scroll", onScroll, { passive: true });
    attached.current = { node, detach: () => sc.removeEventListener("scroll", onScroll) };
  });
  useEffect(
    () => () => {
      attached.current?.detach();
      attached.current = null;
    },
    [],
  );

  useLayoutEffect(() => {
    const node = el.current;
    const sc = scroller.current;
    if (!node || !sc) return;
    const lost = last.current - sc.scrollTop;
    if (lost > 0) {
      held.current += lost;
      node.style.paddingBottom = `${held.current}px`;
      sc.scrollTop = last.current;
    }
  }, [change]);
  return el;
}
