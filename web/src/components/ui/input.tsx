import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Input({ className, type, ...props }: ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "flex min-h-11 w-full min-w-0 items-center rounded-md border border-line-strong bg-panel px-3 py-2 text-base text-fg",
        "transition-[border-color] duration-150 ease-out",
        // Native file buttons size themselves from the UA stylesheet and sit off the
        // field's midline; h-7 fills the content box under py-1.
        "file:mr-3 file:inline-flex file:h-7 file:items-center file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-fg",
        "placeholder:text-muted",
        "hover:border-muted",
        "aria-invalid:border-err",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "[&::-webkit-search-cancel-button]:hidden",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
