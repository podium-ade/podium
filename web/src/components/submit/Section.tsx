import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Badge, Chip } from "../Badge";
import { Card } from "../ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import { cn } from "@/lib/utils";

/**
 * One group of optional spec fields, shut until it has something in it.
 *
 * The header carries what is inside so a closed section still answers "did I set that?" —
 * a summary chip when it holds something, a problem count when what it holds is wrong.
 */
export function Section({
  title,
  description,
  summary,
  problems,
  open,
  onOpenChange,
  children,
}: {
  title: string;
  description: string;
  /** What the group holds right now, for the closed state. */
  summary?: ReactNode;
  problems: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <Card className={cn(problems > 0 && "border-err/40")}>
      <Collapsible open={open} onOpenChange={onOpenChange}>
        <CollapsibleTrigger className="flex w-full items-center gap-3 rounded-xl px-5 py-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/45">
          <ChevronRight
            aria-hidden
            className={cn(
              "size-3.5 shrink-0 text-faint transition-transform duration-150 ease-out",
              open && "rotate-90",
            )}
          />
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium text-fg">{title}</span>
            <span className="block truncate text-2xs text-faint">{description}</span>
          </span>
          {problems > 0 ? (
            <Badge tone="err">
              {problems} {problems === 1 ? "problem" : "problems"}
            </Badge>
          ) : summary ? (
            <Chip className="max-w-56 truncate">{summary}</Chip>
          ) : (
            <span className="text-2xs text-faint">optional</span>
          )}
        </CollapsibleTrigger>
        <CollapsibleContent className="data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:slide-in-from-top-1">
          <div className="border-t border-hairline px-5 py-4">{children}</div>
        </CollapsibleContent>
      </Collapsible>
    </Card>
  );
}
