import type { ReactNode } from "react";
import { ChevronLeft } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";

export function PageHeader({
  title,
  description,
  actions,
  meta,
  back,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  /** A row of chips or counters that sits under the title. */
  meta?: ReactNode;
  /** Where the up arrow goes on a detail screen. */
  back?: { to: string; label: string };
  className?: string;
}) {
  return (
    <header className={cn("space-y-3", className)}>
      {back ? (
        <Link
          to={back.to}
          className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 py-0.5 text-xs text-muted transition-colors hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <ChevronLeft className="size-3.5" />
          {back.label}
        </Link>
      ) : null}
      <div className="flex items-start justify-between gap-x-4">
        <div className="min-w-0 flex-1 space-y-1.5">
          <h1 className="text-xl leading-tight font-semibold tracking-tight text-fg">{title}</h1>
          {description ? (
            <p className="max-w-2xl text-sm leading-relaxed text-muted">{description}</p>
          ) : null}
        </div>
        {actions ? (
          <div className="flex shrink-0 items-center gap-2">{actions}</div>
        ) : null}
      </div>
      {meta ? <div className="flex flex-wrap items-center gap-2">{meta}</div> : null}
    </header>
  );
}
