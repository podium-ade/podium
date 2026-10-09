import type { ReactNode } from "react";

/**
 * FormActions is the row under an assistant section. Save comes first. A personal
 * voice adds Talk and Delete in the same row. The rule above it matches the rules
 * between fields, so the actions read as the last row of the section.
 */
export function FormActions({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-2 border-t border-border py-5">{children}</div>;
}

/**
 * SectionLead is the sentence above a section's fields. The space under it matches
 * the padding a field row keeps under its own rule, so the first rule is not flush.
 */
export function SectionLead({ children }: { children: ReactNode }) {
  return <p className="mb-5 max-w-2xl text-sm leading-relaxed text-muted">{children}</p>;
}
