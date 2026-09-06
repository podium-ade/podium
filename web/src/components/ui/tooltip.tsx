import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/utils";

function TooltipProvider({
  delayDuration = 250,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Provider>) {
  return <TooltipPrimitive.Provider delayDuration={delayDuration} {...props} />;
}

const TooltipRoot = TooltipPrimitive.Root;
const TooltipTrigger = TooltipPrimitive.Trigger;

function TooltipContent({
  className,
  sideOffset = 6,
  children,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          "z-50 max-w-xs rounded-md border border-border bg-popover px-2 py-1 text-2xs leading-relaxed text-fg shadow-md",
          "data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0 data-[state=delayed-open]:zoom-in-95",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0",
          className,
        )}
        {...props}
      >
        {children}
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  );
}

/**
 * The common case in one element: wrap a trigger, give it a label.
 *
 * It carries its own Provider so a component can be tooltipped wherever it is mounted —
 * including in a test that renders it on its own. Radix allows nested providers, and the app
 * still installs one at the root so every tooltip shares the same open delay.
 */
function Tooltip({
  label,
  children,
  side,
  ...props
}: { label: ReactNode; children: ReactNode; side?: "top" | "right" | "bottom" | "left" } & Omit<
  ComponentProps<typeof TooltipPrimitive.Root>,
  "children"
>) {
  if (!label) return <>{children}</>;
  return (
    <TooltipProvider>
      <TooltipRoot {...props}>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent side={side}>{label}</TooltipContent>
      </TooltipRoot>
    </TooltipProvider>
  );
}

export { Tooltip, TooltipContent, TooltipProvider, TooltipRoot, TooltipTrigger };
