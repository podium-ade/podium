import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Table({
  className,
  containerClassName,
  ...props
}: ComponentProps<"table"> & { containerClassName?: string }) {
  return (
    <div
      data-slot="table-container"
      className={cn(
        "relative w-full overflow-x-auto rounded-xl border border-border bg-card shadow-xs",
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
}

function TableHeader({ className, ...props }: ComponentProps<"tbody">) {
  return (
    <thead
      data-slot="table-header"
      className={cn(
        "text-2xs tracking-wide text-faint uppercase",
        "[&_th]:sticky [&_th]:top-0 [&_th]:z-10 [&_th]:bg-panel [&_th]:border-b [&_th]:border-border",
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
        "group/row transition-colors duration-100 hover:bg-raised/45",
        "data-[state=selected]:bg-accent/8",
        className,
      )}
      {...props}
    />
  );
}

/*
 * A pinned trailing column, for the one cell that must stay reachable when a wide table
 * scrolls: the row's actions. It cannot simply inherit the row's translucent hover, because a
 * sticky cell paints over the columns sliding under it and would show them through — so the
 * hover colour is the same blend, mixed down to an opaque one.
 */
const PINNED = [
  "sticky right-0 z-20",
  "before:absolute before:inset-y-0 before:-left-4 before:w-4 before:pointer-events-none",
  "before:bg-gradient-to-r before:from-transparent before:to-bg/45",
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
          "bg-card group-hover/row:bg-[color-mix(in_oklab,var(--color-raised)_45%,var(--color-card))]",
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
