import { useLayoutEffect, useRef } from "react";

const FADE = "24px";

/**
 * useScrollStrip is for a row that scrolls sideways when it runs out of room, such as a strip
 * of tabs on a phone. Two things go wrong with such a row as it stands: the open item can sit
 * past the edge, so the screen does not say where you are, and the items past the edge are cut
 * clean in half with nothing to say there are more.
 *
 * The hook brings the selected item (`aria-current="page"` or Radix's `data-state="active"`)
 * into view whenever `selected` changes, and fades whichever edge still has items beyond it.
 */
export function useScrollStrip<T extends HTMLElement>(selected?: unknown) {
  const ref = useRef<T>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const sync = () => {
      const start = el.scrollLeft > 1;
      const end = el.scrollWidth - el.clientWidth - el.scrollLeft > 1;
      const mask =
        start || end
          ? `linear-gradient(to right, ${start ? "transparent" : "#000"}, #000 ${start ? FADE : "0px"}, #000 calc(100% - ${end ? FADE : "0px"}), ${end ? "transparent" : "#000"})`
          : "";
      el.style.maskImage = mask;
      el.style.webkitMaskImage = mask;
    };
    sync();
    el.addEventListener("scroll", sync, { passive: true });
    const ro = new ResizeObserver(sync);
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", sync);
      ro.disconnect();
    };
  }, []);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || el.scrollWidth <= el.clientWidth) return;
    const item = el.querySelector<HTMLElement>('[aria-current="page"], [data-state="active"]');
    if (!item) return;
    const box = el.getBoundingClientRect();
    const at = item.getBoundingClientRect();
    // Only when some of it is hidden, and then centred, so the tabs either side show too.
    if (at.left >= box.left && at.right <= box.right) return;
    el.scrollLeft += at.left - box.left - (box.width - at.width) / 2;
  }, [selected]);

  return ref;
}
