export function Empty({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="rounded border border-dashed border-border p-8 text-center">
      <p className="text-sm text-fg">{title}</p>
      {hint ? <p className="mt-1 text-xs text-muted">{hint}</p> : null}
    </div>
  );
}
