import { cva, type VariantProps } from "class-variance-authority";
import { AlertTriangle, CheckCircle2, Info, OctagonAlert, Unplug } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/utils";

const alertVariants = cva(
  "relative flex w-full items-start gap-2.5 rounded-lg border px-3 py-2.5 text-xs leading-relaxed [&>svg]:mt-px [&>svg]:size-4 [&>svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "border-border bg-raised/60 text-fg [&>svg]:text-muted",
        warn: "border-warn/35 bg-warn/10 text-warn",
        destructive: "border-err/40 bg-err/10 text-err",
        lost: "border-lost/40 bg-lost/10 text-lost",
        info: "border-accent/30 bg-accent/8 text-fg [&>svg]:text-accent",
        success: "border-ok/35 bg-ok/10 text-ok",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

const ICON = {
  default: Info,
  warn: AlertTriangle,
  destructive: OctagonAlert,
  lost: Unplug,
  info: Info,
  success: CheckCircle2,
} as const;

interface AlertProps
  extends Omit<ComponentProps<"div">, "title">,
    VariantProps<typeof alertVariants> {
  /** Set false for a bare alert with no leading glyph. */
  icon?: boolean;
  title?: ReactNode;
}

function Alert({ className, variant, icon = true, title, children, ...props }: AlertProps) {
  const Icon = ICON[variant ?? "default"];
  return (
    <div
      role="status"
      data-slot="alert"
      className={cn(alertVariants({ variant }), className)}
      {...props}
    >
      {icon ? <Icon aria-hidden /> : null}
      <div className="min-w-0 flex-1 space-y-0.5">
        {title ? <p className="font-medium">{title}</p> : null}
        {children ? <div className={cn(title && "opacity-90")}>{children}</div> : null}
      </div>
    </div>
  );
}

export { Alert };
