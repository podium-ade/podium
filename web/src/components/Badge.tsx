import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type Tone = "ok" | "err" | "warn" | "run" | "idle" | "lost";

const TONE: Record<Tone, string> = {
  ok: "text-ok border-ok/35 bg-ok/12",
  err: "text-err border-err/35 bg-err/12",
  warn: "text-warn border-warn/35 bg-warn/12",
  run: "text-run border-run/35 bg-run/12",
  idle: "text-idle border-idle/35 bg-idle/12",
  lost: "text-lost border-lost/35 bg-lost/12",
};

const FILL: Record<Tone, string> = {
  ok: "bg-ok",
  err: "bg-err",
  warn: "bg-warn",
  run: "bg-run",
  idle: "bg-idle",
  lost: "bg-lost",
};

export function Badge({
  tone,
  children,
  dot = true,
  className,
}: {
  tone: Tone;
  children: ReactNode;
  /** The status dot. Off for badges that already carry their own glyph. */
  dot?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-2xs font-medium whitespace-nowrap",
        TONE[tone],
        className,
      )}
    >
      {dot ? (
        <span aria-hidden className="relative flex size-1.5 shrink-0">
          {/* Running is the one state that is still changing; only it pulses. */}
          {tone === "run" ? (
            <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", FILL[tone])} />
          ) : null}
          <span className={cn("relative size-1.5 rounded-full", FILL[tone])} />
        </span>
      ) : null}
      {children}
    </span>
  );
}

export function Dot({ tone, title }: { tone: Tone; title: string }) {
  return (
    <span
      aria-label={title}
      title={title}
      className={cn("inline-block size-2 shrink-0 rounded-full align-middle", FILL[tone])}
    />
  );
}

export function Chip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-md border border-border bg-raised/70 px-1.5 py-0.5 text-2xs text-muted whitespace-nowrap",
        className,
      )}
    >
      {children}
    </span>
  );
}
