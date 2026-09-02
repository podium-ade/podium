import type { ReactNode } from "react";

export type Tone = "ok" | "err" | "warn" | "run" | "idle";

const TONE: Record<Tone, string> = {
  ok: "text-ok border-ok/40 bg-ok/10",
  err: "text-err border-err/40 bg-err/10",
  warn: "text-warn border-warn/40 bg-warn/10",
  run: "text-run border-run/40 bg-run/10",
  idle: "text-idle border-idle/40 bg-idle/10",
};

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span
      className={`inline-block rounded border px-1.5 py-0.5 text-xs font-medium ${TONE[tone]}`}
    >
      {children}
    </span>
  );
}

export function Dot({ tone, title }: { tone: Tone; title: string }) {
  const fill: Record<Tone, string> = {
    ok: "bg-ok",
    err: "bg-err",
    warn: "bg-warn",
    run: "bg-run",
    idle: "bg-idle",
  };
  return (
    <span
      aria-label={title}
      title={title}
      className={`inline-block size-2 rounded-full align-middle ${fill[tone]}`}
    />
  );
}

export function Chip({ children }: { children: ReactNode }) {
  return (
    <span className="inline-block rounded bg-raised px-1.5 py-0.5 text-xs text-muted">
      {children}
    </span>
  );
}
