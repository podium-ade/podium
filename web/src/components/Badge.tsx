import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type Tone = "ok" | "err" | "warn" | "run" | "idle" | "lost";

const TONE: Record<Tone, string> = {
  ok: "bg-ok-bg text-ok",
  err: "bg-err-bg text-err",
  warn: "bg-warn-bg text-warn",
  run: "bg-run-bg text-run",
  idle: "bg-idle-bg text-soft",
  lost: "bg-lost-bg text-lost",
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
        "inline-flex w-fit items-center gap-1.5 rounded-sm px-2.5 py-1 text-xs font-bold whitespace-nowrap",
        TONE[tone],
        className,
      )}
    >
      {dot ? (
        <span aria-hidden className="relative flex size-1.5 shrink-0">
          {/* Running is the one state that is still changing; only it pulses. */}
          {tone === "run" ? (
            <span className={cn("absolute inset-0 animate-ping rounded-[1px] opacity-60", FILL[tone])} />
          ) : null}
          <span className={cn("relative size-1.5 rounded-[1px]", FILL[tone])} />
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
        "inline-flex w-fit items-center gap-1 rounded-sm bg-raised px-2.5 py-1 text-xs font-bold text-soft whitespace-nowrap",
        className,
      )}
    >
      {children}
    </span>
  );
}
