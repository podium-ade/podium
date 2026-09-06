import type { CSSProperties } from "react";
import { cn } from "@/lib/utils";

export function Skeleton({ className = "", style }: { className?: string; style?: CSSProperties }) {
  return <div style={style} className={cn("animate-shimmer rounded-md bg-raised", className)} />;
}

/** A table-shaped placeholder, framed like the table it stands in for. */
export function TableSkeleton({ rows = 6, cols = 5 }: { rows?: number; cols?: number }) {
  return (
    <div
      aria-busy="true"
      aria-label="Loading"
      className="overflow-hidden rounded-xl border border-border bg-card"
    >
      <div className="flex gap-4 border-b border-border bg-panel px-3 py-2.5">
        {Array.from({ length: cols }, (_, c) => (
          <Skeleton key={c} className="h-3 flex-1" />
        ))}
      </div>
      {Array.from({ length: rows }, (_, r) => (
        <div
          key={r}
          className="flex items-center gap-4 border-b border-hairline px-3 py-3 last:border-0"
        >
          {Array.from({ length: cols }, (_, c) => (
            <Skeleton
              key={c}
              className="h-3.5 flex-1"
              // A little variation reads as data rather than as a broken grid.
              style={{ opacity: 1 - ((r + c) % 3) * 0.18 }}
            />
          ))}
        </div>
      ))}
    </div>
  );
}
