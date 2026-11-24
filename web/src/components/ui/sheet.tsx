import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/**
 * Sheet is a dialog that slides over the page: up from the bottom on a phone, where a thumb
 * reaches it and the list under it can use the whole width, and in from the right on a wider
 * screen, the full height of the window. It is Radix's dialog, so focus, Escape and the
 * overlay behave as every other dialog does.
 */
const Sheet = DialogPrimitive.Root;
const SheetTrigger = DialogPrimitive.Trigger;
const SheetClose = DialogPrimitive.Close;

function SheetContent({ className, children, ...props }: ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-[#070f18]/55",
          "data-[state=open]:animate-in data-[state=open]:fade-in-0",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0",
        )}
      />
      <DialogPrimitive.Content
        data-slot="sheet-content"
        className={cn(
          "fixed z-50 flex flex-col bg-panel shadow-[0_20px_60px_#0005] outline-none",
          // Phone: a bottom sheet, never taller than most of the screen.
          "inset-x-0 bottom-0 max-h-[85dvh] rounded-t-2xl border-t border-border",
          "data-[state=open]:animate-in data-[state=open]:slide-in-from-bottom data-[state=open]:duration-200",
          "data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom data-[state=closed]:duration-150",
          // Wider: a panel along the right edge, the window's height.
          "sm:inset-y-0 sm:right-0 sm:left-auto sm:max-h-none sm:w-[26rem] sm:rounded-none sm:border-t-0 sm:border-l",
          "sm:data-[state=open]:slide-in-from-right sm:data-[state=open]:slide-in-from-bottom-0",
          "sm:data-[state=closed]:slide-out-to-right sm:data-[state=closed]:slide-out-to-bottom-0",
          className,
        )}
        {...props}
      >
        {children}
        <DialogPrimitive.Close
          className={cn(
            "absolute top-3.5 right-3.5 rounded-md p-1.5 text-muted transition-colors",
            "hover:bg-raised hover:text-fg",
            "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          )}
        >
          <X className="size-4" />
          <span className="sr-only">Close</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

function SheetHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="sheet-header"
      className={cn("shrink-0 space-y-1 border-b border-border px-4 pt-4 pb-3 pr-12", className)}
      {...props}
    />
  );
}

function SheetFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="sheet-footer"
      className={cn(
        "flex shrink-0 items-center justify-end gap-2 border-t border-border px-4 pt-3",
        "pb-[max(0.75rem,env(safe-area-inset-bottom))]",
        className,
      )}
      {...props}
    />
  );
}

function SheetTitle({ className, ...props }: ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title className={cn("text-base font-semibold text-fg", className)} {...props} />;
}

function SheetDescription({ className, ...props }: ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description className={cn("text-xs leading-relaxed text-muted", className)} {...props} />
  );
}

export { Sheet, SheetClose, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger };
