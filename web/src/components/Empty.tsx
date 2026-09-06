import type { LucideIcon } from "lucide-react";
import { Inbox } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function Empty({
  title,
  hint,
  icon: Icon = Inbox,
  action,
  className,
}: {
  title: string;
  hint?: ReactNode;
  icon?: LucideIcon;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center rounded-xl border border-dashed border-border bg-panel/35 px-8 py-14 text-center",
        className,
      )}
    >
      <span className="mb-4 grid size-11 place-items-center rounded-xl border border-border bg-raised/60 text-muted">
        <Icon className="size-5" />
      </span>
      <p className="text-sm font-medium text-fg">{title}</p>
      {hint ? <p className="mt-1.5 max-w-md text-xs leading-relaxed text-muted">{hint}</p> : null}
      {action ? <div className="mt-5 flex flex-wrap justify-center gap-2">{action}</div> : null}
    </div>
  );
}
