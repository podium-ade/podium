import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

// eslint-disable-next-line react-refresh/only-export-components -- screens compose links with the same variants
export const buttonVariants = cva(
  [
    "inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-md",
    "text-sm font-medium select-none",
    "transition-[background-color,border-color,color,box-shadow,opacity] duration-150 ease-out",
    "outline-none focus-visible:ring-2 focus-visible:ring-ring/55 focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
    "disabled:pointer-events-none disabled:opacity-45",
    "aria-disabled:pointer-events-none aria-disabled:opacity-45",
    "[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  ],
  {
    variants: {
      variant: {
        default:
          "bg-primary text-primary-foreground shadow-xs hover:bg-primary/88 active:bg-primary/80",
        destructive:
          "bg-destructive text-destructive-foreground shadow-xs hover:bg-destructive/88 active:bg-destructive/80",
        outline:
          "border border-border bg-panel text-fg shadow-xs hover:border-border hover:bg-raised active:bg-raised/80",
        secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/75 active:bg-secondary/60",
        ghost: "text-muted hover:bg-raised hover:text-fg active:bg-raised/70",
        link: "text-primary underline-offset-4 hover:underline",
        subtle: "bg-accent/12 text-accent hover:bg-accent/20 active:bg-accent/25",
        danger:
          "border border-err/35 bg-err/10 text-err hover:border-err/50 hover:bg-err/18 active:bg-err/24",
      },
      size: {
        default: "h-9 px-3.5",
        sm: "h-8 rounded-md px-2.5 text-xs [&_svg]:size-3.5",
        xs: "h-7 gap-1 rounded-md px-2 text-2xs [&_svg]:size-3.5",
        lg: "h-10 rounded-lg px-5",
        icon: "size-9",
        "icon-sm": "size-8 [&_svg]:size-3.5",
        "icon-xs": "size-7 [&_svg]:size-3.5",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface ButtonProps
  extends ComponentProps<"button">,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

function Button({ className, variant, size, asChild = false, ...props }: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return <Comp data-slot="button" className={cn(buttonVariants({ variant, size, className }))} {...props} />;
}

export { Button };
