import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

// eslint-disable-next-line react-refresh/only-export-components -- links and spans share the variants
export const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center gap-1.5 rounded-sm px-2.5 py-1 text-xs font-bold whitespace-nowrap before:size-1.5 before:shrink-0 before:rounded-[1px] before:bg-current [&_svg]:size-3.5 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-raised text-soft",
        outline: "bg-raised text-soft",
        accent: "bg-run-bg text-run",
        ok: "bg-ok-bg text-ok",
        err: "bg-err-bg text-err",
        warn: "bg-warn-bg text-warn",
        run: "bg-run-bg text-run",
        idle: "bg-idle-bg text-idle",
        lost: "bg-lost-bg text-lost",
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
