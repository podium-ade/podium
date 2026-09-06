import { Inbox } from "lucide-react";

export function Empty({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-dashed border-border bg-panel/40 px-8 py-12 text-center">
      <Inbox className="mb-3 size-8 text-muted" />
      <p className="text-sm font-medium text-fg">{title}</p>
      {hint ? <p className="mt-1 max-w-md text-xs text-muted">{hint}</p> : null}
    </div>
  );
}
