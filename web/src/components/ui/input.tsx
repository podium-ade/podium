import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Input({ className, type, ...props }: ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "flex h-9 w-full min-w-0 rounded-md border border-input bg-bg px-3 py-1 text-sm text-fg shadow-xs",
        "transition-[border-color,box-shadow] duration-150 ease-out",
        "file:mr-3 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-fg",
        "placeholder:text-faint",
        "hover:border-muted/45",
        "outline-none focus-visible:border-accent/60 focus-visible:ring-2 focus-visible:ring-ring/35",
        "aria-invalid:border-err/70 aria-invalid:focus-visible:ring-err/30",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "[&::-webkit-search-cancel-button]:hidden",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
