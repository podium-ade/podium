import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";
import { useScrollStrip } from "../../hooks/useScrollStrip";

function Tabs({ className, ...props }: ComponentProps<typeof TabsPrimitive.Root>) {
  return (
    <TabsPrimitive.Root data-slot="tabs" className={cn("flex flex-col gap-0", className)} {...props} />
  );
}

function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  // Radix sets data-state on the triggers itself, so the strip re-checks which one is open on
  // every render rather than on a value it is never given.
  const strip = useScrollStrip<HTMLDivElement>({});
  return (
    <TabsPrimitive.List
      ref={strip}
      data-slot="tabs-list"
      className={cn(
        "flex w-full items-center gap-2 overflow-x-auto border-b border-border",
        className,
      )}
      {...props}
    />
  );
}

function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        // The open tab's bar is a 3px bottom border. An equal transparent one on top keeps
        // the label and icon on the trigger's centre line; with the bar alone they sat 1.5px
        // high, which shows in a fixed-height header such as the desktop panel's.
        "inline-flex min-h-11 shrink-0 items-center justify-center gap-1.5 border-y-[3px] border-transparent px-3.5 text-sm font-semibold whitespace-nowrap",
        "text-soft transition-colors duration-150 ease-out hover:bg-raised hover:text-fg",
        "data-[state=active]:border-b-accent data-[state=active]:text-fg",
        "disabled:pointer-events-none disabled:opacity-45",
        "[&_svg]:size-3.5 [&_svg]:shrink-0",
        className,
      )}
      {...props}
    />
  );
}

function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("flex-1 pt-4 outline-none", className)}
      {...props}
    />
  );
}

export { Tabs, TabsList, TabsTrigger, TabsContent };
