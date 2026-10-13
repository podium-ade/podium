import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

// eslint-disable-next-line react-refresh/only-export-components -- screens compose links with the same variants
export const buttonVariants = cva(
  [
    "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md",
    "font-bold select-none",
    "transition-[background-color,border-color,filter] duration-150 ease-out",
    "disabled:pointer-events-none disabled:opacity-45",
    "aria-disabled:pointer-events-none aria-disabled:opacity-45",
    "[&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  ],
  {
    variants: {
      variant: {
        default: "border border-accent bg-accent text-primary-foreground hover:border-accent-hover hover:bg-accent-hover",
        destructive: "border border-[#a92d35] bg-[#a92d35] text-white hover:border-[#8f202a] hover:bg-[#8f202a]",
        outline: "border border-line-strong bg-panel text-fg hover:bg-raised",
        secondary: "border border-line-strong bg-panel text-fg hover:bg-raised",
        ghost: "border border-transparent bg-transparent text-fg hover:bg-raised",
        link: "text-fg underline-offset-4 hover:underline",
        subtle: "border border-transparent bg-raised text-fg hover:bg-border",
        danger: "border border-[#a92d35] bg-[#a92d35] text-white hover:border-[#8f202a] hover:bg-[#8f202a]",
      },
      size: {
        default: "min-h-11 px-4 text-base",
        sm: "min-h-9 px-3 text-sm [&_svg]:size-3.5",
        xs: "min-h-8 gap-1 px-2 text-xs [&_svg]:size-3.5",
        lg: "min-h-11 px-5 text-base",
        icon: "size-11",
        "icon-sm": "size-9 [&_svg]:size-4",
        "icon-xs": "size-8 [&_svg]:size-3.5",
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
