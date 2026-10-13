import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "flex min-h-[106px] w-full rounded-md border border-line-strong bg-panel px-3 py-2 text-base text-fg",
        "transition-[border-color] duration-150 ease-out",
        "placeholder:text-muted hover:border-muted",
        "aria-invalid:border-err",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
