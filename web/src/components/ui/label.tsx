import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Label({ className, ...props }: ComponentProps<"label">) {
  return (
    <label
      data-slot="label"
      className={cn(
        "flex items-center gap-1.5 text-xs font-medium text-muted select-none",
        "peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
        "group-data-[disabled=true]:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Label };
