import type { ReactNode } from "react";
import { Plug, Plus } from "lucide-react";
import { cn } from "../../lib/utils";

/**
 * McpMark is the product glyph on the add-server picker and on a row that matches a known
 * endpoint. Decorative: the product is always named in text beside it. Marks are inline SVG
 * so this screen never fetches a third-party icon URL.
 */
export function McpMark({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  const glyph = GLYPH[name] ?? GLYPH.unknown;
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex size-8 shrink-0 items-center justify-center rounded-lg",
        glyph.bg,
        className,
      )}
    >
      {glyph.node}
    </span>
  );
}

const GLYPH: Record<string, { bg: string; node: ReactNode }> = {
  linear: {
    bg: "bg-[#5E6AD2]",
    node: (
      <svg viewBox="0 0 16 16" className="size-4 text-white" fill="currentColor">
        <path d="M2.2 11.3 11.3 2.2A1.2 1.2 0 0 1 13 2.2l.8.8a1.2 1.2 0 0 1 0 1.7L4.7 13.8a1.2 1.2 0 0 1-1.7 0l-.8-.8a1.2 1.2 0 0 1 0-1.7Z" />
      </svg>
    ),
  },
  notion: {
    bg: "bg-fg",
    node: (
      <svg viewBox="0 0 16 16" className="size-4 text-bg" fill="currentColor">
        <path d="M4.2 2.4h6.1l1.5 11.2H9.4L8.2 6.1 6.6 13.6H3.1L4.2 2.4Zm.8 1.6-.8 8.4h1.1l1.7-7.7h.6l1.8 7.7h1.2l-.9-8.4H5Z" />
      </svg>
    ),
  },
  sentry: {
    bg: "bg-[#362D59]",
    node: (
      <svg viewBox="0 0 16 16" className="size-4 text-[#F6B93B]" fill="currentColor">
        <path d="M8.9 2.2 14.6 12a1.2 1.2 0 0 1-1 1.8H9.2L8 11.4h2.3L8 6.2 4.2 13.8H2.4A1.2 1.2 0 0 1 1.4 12L7.1 2.2a1.2 1.2 0 0 1 1.8 0Z" />
      </svg>
    ),
  },
  github: {
    bg: "bg-[#24292F]",
    node: (
      <svg viewBox="0 0 16 16" className="size-4 text-white" fill="currentColor">
        <path d="M8 1.5A6.5 6.5 0 0 0 1.5 8c0 2.87 1.86 5.3 4.44 6.16.32.06.44-.14.44-.31v-1.1c-1.8.39-2.18-.87-2.18-.87-.3-.75-.73-.95-.73-.95-.6-.4.04-.4.04-.4.66.05 1.01.68 1.01.68.59 1 1.54.71 1.92.54.06-.43.23-.71.42-.88-1.46-.16-3-.73-3-3.25 0-.72.26-1.3.68-1.76-.07-.17-.3-.85.06-1.77 0 0 .55-.18 1.81.67A6.2 6.2 0 0 1 8 4.7c.56 0 1.12.08 1.65.22 1.26-.85 1.81-.67 1.81-.67.36.92.13 1.6.06 1.77.42.46.68 1.04.68 1.76 0 2.53-1.54 3.09-3.01 3.25.24.2.45.61.45 1.23v1.82c0 .17.12.37.44.31A6.51 6.51 0 0 0 14.5 8 6.5 6.5 0 0 0 8 1.5Z" />
      </svg>
    ),
  },
  custom: {
    bg: "bg-raised",
    node: <Plus className="size-4 text-muted" />,
  },
  unknown: {
    bg: "bg-raised",
    node: <Plug className="size-4 text-muted" />,
  },
};
