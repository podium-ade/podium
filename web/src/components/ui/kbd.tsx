import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/** A keycap. Shortcuts are only discoverable if the UI shows them. */
function Kbd({ className, ...props }: ComponentProps<"kbd">) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded border border-border bg-raised px-1.5",
        "font-sans text-2xs font-medium text-muted",
        className,
      )}
      {...props}
    />
  );
}

export { Kbd };
