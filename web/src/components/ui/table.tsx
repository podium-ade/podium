import { useLayoutEffect, useRef, type ComponentProps, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { StickyScrollX } from "./sticky-scroll-x";

/** More than a subpixel of table still hidden past the sticky trailing column. */
function canScrollEnd(el: HTMLElement): boolean {
  return el.scrollWidth - el.clientWidth - el.scrollLeft > 1;
}

function Table({
  className,
  containerClassName,
  stickyScrollbar,
  footer,
  ...props
}: ComponentProps<"table"> & {
  containerClassName?: string;
  /**
   * footer is a bar inside the table's card, under the last row and across the card's full
   * width, so it never scrolls sideways with a wide table: where a paged table's Pager goes.
   */
  footer?: ReactNode;
  /**
   * stickyScrollbar is for a table long enough that its own horizontal bar sits screens below
   * the rows being read: the bar is replaced by one stuck to the bottom of the viewport.
   */
  stickyScrollbar?: boolean;
}) {
  const scroller = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const sync = () => el.toggleAttribute("data-overflow-end", canScrollEnd(el));
    sync();
    el.addEventListener("scroll", sync, { passive: true });
    const ro = new ResizeObserver(sync);
    ro.observe(el);
    const table = el.querySelector("table");
    if (table) ro.observe(table);
    return () => {
      el.removeEventListener("scroll", sync);
      ro.disconnect();
    };
  }, []);

  const table = (
    <div
      ref={scroller}
      data-slot="table-container"
      className={cn(
        "group/table relative w-full overflow-x-auto bg-card",
        // With a footer the card is the frame around both, so the scroller draws no edge.
        footer == null && "rounded-lg border border-border",
        stickyScrollbar && "scroll-x-strip-target",
        containerClassName,
      )}
    >
      <table
        data-slot="table"
        className={cn("w-full caption-bottom border-separate border-spacing-0 text-sm", className)}
        {...props}
      />
    </div>
  );
  if (!stickyScrollbar && footer == null) return table;
  // The wrapper is the strip's containing block: it sticks while any of the table is in view.
  // With a footer it is also the card, so the footer sits inside the same border as the rows.
  return (
    <div
      data-slot="table-frame"
      className={cn("relative", footer != null && "rounded-lg border border-border bg-card [overflow:clip]")}
    >
      {table}
      {stickyScrollbar ? (
        <StickyScrollX target={scroller} className={footer == null ? "-mt-px rounded-b-lg" : undefined} />
      ) : null}
      {footer != null ? (
        <div data-slot="table-footer" className="border-t border-border px-3 py-2.5">
          {footer}
        </div>
      ) : null}
    </div>
  );
}

function TableHeader({ className, ...props }: ComponentProps<"tbody">) {
  return (
    <thead
      data-slot="table-header"
      className={cn(
        "text-xs font-bold tracking-[0.08em] text-muted uppercase",
        "[&_th]:sticky [&_th]:top-0 [&_th]:z-10 [&_th]:bg-raised [&_th]:border-b [&_th]:border-border",
        className,
      )}
      {...props}
    />
  );
}

function TableBody({ className, ...props }: ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      className={cn("[&_tr:last-child_td]:border-b-0", className)}
      {...props}
    />
  );
}

function TableRow({ className, ...props }: ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "group/row transition-colors duration-100 hover:bg-raised",
        "data-[state=selected]:bg-raised",
        className,
      )}
      {...props}
    />
  );
}

/*
 * A pinned trailing column, for the one cell that must stay reachable when a wide table
 * scrolls: the row's actions. A sticky cell paints over the columns sliding under it, so it
 * needs its own opaque background rather than the row's; it takes the row's hover and
 * selected colour (raised, which is opaque) so the highlight runs the full width of the row.
 *
 * The edge fade is only while something is still sliding under the column. Once the scroller
 * is at the end, the column is in its own place and a gradient would be a shadow on empty air.
 */
const PINNED = [
  "sticky right-0 z-20",
  "before:pointer-events-none before:absolute before:inset-y-0 before:-left-4 before:w-4",
  "before:bg-gradient-to-r before:from-transparent before:to-bg/45",
  "before:opacity-0 before:transition-opacity",
  "group-data-[overflow-end]/table:before:opacity-100",
];

function TableHead({ className, pinned, ...props }: ComponentProps<"th"> & { pinned?: boolean }) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "px-3 py-2 text-left align-middle font-medium whitespace-nowrap",
        "[&:has([role=checkbox])]:w-0 [&:has([role=checkbox])]:pr-0",
        pinned && [...PINNED, "z-30"],
        className,
      )}
      {...props}
    />
  );
}

function TableCell({ className, pinned, ...props }: ComponentProps<"td"> & { pinned?: boolean }) {
  return (
    <td
      data-slot="table-cell"
      className={cn(
        "border-b border-hairline px-3 py-2.5 align-middle",
        pinned && [
          ...PINNED,
          // The same fade as the row's, or the cell lands a beat before the rest of it.
          "bg-card transition-colors duration-100 group-hover/row:bg-raised group-data-[state=selected]/row:bg-raised",
        ],
        className,
      )}
      {...props}
    />
  );
}

function TableCaption({ className, ...props }: ComponentProps<"caption">) {
  return (
    <caption data-slot="table-caption" className={cn("mt-3 text-xs text-muted", className)} {...props} />
  );
}

export { Table, TableHeader, TableBody, TableHead, TableRow, TableCell, TableCaption };
