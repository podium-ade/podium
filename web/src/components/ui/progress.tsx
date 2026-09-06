import * as ProgressPrimitive from "@radix-ui/react-progress";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

const indicatorVariants = cva("h-full w-full flex-1 transition-transform duration-300 ease-out", {
  variants: {
    tone: {
      accent: "bg-accent",
      ok: "bg-ok",
      warn: "bg-warn",
      err: "bg-err",
      idle: "bg-idle",
    },
  },
  defaultVariants: { tone: "accent" },
});

function Progress({
  className,
  value,
  tone,
  ...props
}: ComponentProps<typeof ProgressPrimitive.Root> & VariantProps<typeof indicatorVariants>) {
  return (
    <ProgressPrimitive.Root
      data-slot="progress"
      className={cn("relative h-1.5 w-full overflow-hidden rounded-full bg-raised", className)}
      value={value}
      {...props}
    >
      <ProgressPrimitive.Indicator
        data-slot="progress-indicator"
        className={indicatorVariants({ tone })}
        style={{ transform: `translateX(-${100 - (value ?? 0)}%)` }}
      />
    </ProgressPrimitive.Root>
  );
}

export { Progress };
