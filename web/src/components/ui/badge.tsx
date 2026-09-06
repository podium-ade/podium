import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

// eslint-disable-next-line react-refresh/only-export-components -- links and spans share the variants
export const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center gap-1 rounded-md border px-1.5 py-0.5 text-2xs font-medium whitespace-nowrap [&_svg]:size-3 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "border-border bg-raised text-fg",
        outline: "border-border bg-transparent text-muted",
        accent: "border-accent/35 bg-accent/12 text-accent",
        ok: "border-ok/35 bg-ok/12 text-ok",
        err: "border-err/35 bg-err/12 text-err",
        warn: "border-warn/35 bg-warn/12 text-warn",
        run: "border-run/35 bg-run/12 text-run",
        idle: "border-idle/35 bg-idle/12 text-idle",
        lost: "border-lost/35 bg-lost/12 text-lost",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

function Badge({
  className,
  variant,
  asChild = false,
  ...props
}: ComponentProps<"span"> & VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "span";
  return <Comp data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge };
