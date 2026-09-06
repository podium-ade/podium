import type { ReactNode } from "react";
import { Label } from "../ui/label";
import { cn } from "@/lib/utils";

/**
 * A labelled control that knows whether it is wrong.
 *
 * The render prop hands back the wiring — `id`, `aria-invalid`, `aria-describedby` — so a
 * field cannot be added without its label and its error being connected to it.
 */
export function Field({
  id,
  label,
  hint,
  required,
  problems = [],
  className,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  required?: boolean;
  problems?: string[];
  className?: string;
  children: (control: {
    id: string;
    "aria-invalid"?: true;
    "aria-describedby"?: string;
  }) => ReactNode;
}) {
  const invalid = problems.length > 0;
  const errorId = `${id}-problem`;
  return (
    <div className={cn("space-y-1.5", className)}>
      <div className="flex items-baseline gap-2">
        <Label htmlFor={id}>{label}</Label>
        {required ? <span className="text-2xs text-faint">required</span> : null}
      </div>
      {children({
        id,
        "aria-invalid": invalid || undefined,
        "aria-describedby": invalid ? errorId : undefined,
      })}
      {invalid ? (
        <FieldProblems id={errorId} problems={problems} />
      ) : hint ? (
        <p className="text-2xs leading-relaxed text-muted">{hint}</p>
      ) : null}
    </div>
  );
}

/** The same message shape for problems that belong to a group rather than one control. */
export function FieldProblems({ id, problems }: { id?: string; problems: string[] }) {
  if (problems.length === 0) return null;
  return (
    <div id={id} className="space-y-0.5">
      {problems.map((p) => (
        <p key={p} className="text-2xs leading-relaxed break-words text-err">
          {p}
        </p>
      ))}
    </div>
  );
}
