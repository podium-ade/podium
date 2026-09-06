import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const alertVariants = cva("relative w-full rounded-lg border px-3 py-2.5 text-xs", {
  variants: {
    variant: {
      default: "border-border bg-card text-fg",
      warn: "border-warn/40 bg-warn/10 text-warn",
      destructive: "border-err/50 bg-err/10 text-err",
      lost: "border-lost/50 bg-lost/10 text-lost",
      info: "border-idle/40 bg-idle/10 text-fg",
    },
  },
  defaultVariants: { variant: "default" },
});

function Alert({
  className,
  variant,
  ...props
}: HTMLAttributes<HTMLDivElement> & VariantProps<typeof alertVariants>) {
  return <div role="status" className={cn(alertVariants({ variant }), className)} {...props} />;
}

export { Alert };
