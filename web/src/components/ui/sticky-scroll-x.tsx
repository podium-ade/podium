import { useLayoutEffect, useRef, type RefObject } from "react";
import { cn } from "@/lib/utils";

/** More than a subpixel of content hidden sideways. */
function overflows(el: HTMLElement): boolean {
  return el.scrollWidth - el.clientWidth > 1;
}

/**
 * StickyScrollX is a horizontal scrollbar for a long, wide scroller whose own bar would be
 * drawn at its bottom, a screenful or more below the rows being read. It is a thin strip the
 * scroller's width, sticking to the bottom of the viewport while any of the scroller is in
 * view, and scrolling it scrolls the scroller (and the other way round).
 *
 * The target's own bar should be hidden (`scroll-x-strip-target`) so two never show at once.
 * The strip is only drawn while the target overflows sideways. It is a duplicate of the
 * target's own scrolling, which the keyboard still reaches, so it is hidden from assistive
 * technology and never takes focus.
 *
 * Sticky is relative to the nearest scrolling ancestor, so nothing between the strip and the
 * page's scroller may clip with overflow: hidden (overflow: clip is fine).
 */
export function StickyScrollX({
  target,
  className,
}: {
  target: RefObject<HTMLElement | null>;
  className?: string;
}) {
  const strip = useRef<HTMLDivElement>(null);
  const track = useRef<HTMLDivElement>(null);

  // hidden is set here, not as a prop, so a re-render never puts back what measure() decided.
  useLayoutEffect(() => {
    const el = target.current;
    const bar = strip.current;
    const inner = track.current;
    if (!el || !bar || !inner) return;

    // Each side copies the other only when they differ, so a copied value firing a scroll
    // event on the far side finds them equal and stops there: no loop.
    const copy = (from: HTMLElement, to: HTMLElement) => {
      if (Math.abs(to.scrollLeft - from.scrollLeft) >= 1) to.scrollLeft = from.scrollLeft;
    };
    const fromTarget = () => copy(el, bar);
    const fromStrip = () => copy(bar, el);

    const measure = () => {
      const on = overflows(el);
      bar.hidden = !on;
      if (!on) return;
      // The strip spans the target's padding box, inside its border, so both scroll the
      // same distance for the same scrollLeft.
      bar.style.width = `${el.clientWidth}px`;
      bar.style.marginLeft = `${el.clientLeft}px`;
      inner.style.width = `${el.scrollWidth}px`;
      fromTarget();
    };

    measure();
    el.addEventListener("scroll", fromTarget, { passive: true });
    bar.addEventListener("scroll", fromStrip, { passive: true });
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    for (const child of Array.from(el.children)) ro.observe(child);
    return () => {
      el.removeEventListener("scroll", fromTarget);
      bar.removeEventListener("scroll", fromStrip);
      ro.disconnect();
    };
  }, [target]);

  return (
    <div
      ref={strip}
      aria-hidden
      tabIndex={-1}
      data-slot="sticky-scroll-x"
      className={cn("scroll-x-visible sticky bottom-0 z-40 overflow-y-hidden", className)}
    >
      <div ref={track} className="h-px" />
    </div>
  );
}
