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
        "flex flex-col items-center px-5 py-12 text-center",
        className,
      )}
    >
      <span className="mb-3 grid size-12 place-items-center rounded-md border border-line-strong bg-raised text-soft">
        <Icon className="size-5" />
      </span>
      <p className="text-lg font-bold text-fg">{title}</p>
      {hint ? <p className="mt-2 max-w-[58ch] text-base leading-relaxed text-soft">{hint}</p> : null}
      {action ? <div className="mt-5 flex flex-wrap justify-center gap-2">{action}</div> : null}
    </div>
  );
}
