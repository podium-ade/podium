import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "flex min-h-20 w-full rounded-md border border-input bg-bg px-3 py-2 text-sm text-fg shadow-xs",
        "transition-[border-color,box-shadow] duration-150 ease-out",
        "placeholder:text-faint hover:border-muted/45",
        "outline-none focus-visible:border-accent/60 focus-visible:ring-2 focus-visible:ring-ring/35",
        "aria-invalid:border-err/70 aria-invalid:focus-visible:ring-err/30",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
