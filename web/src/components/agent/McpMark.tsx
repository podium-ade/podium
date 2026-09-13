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
        "inline-flex size-8 shrink-0 items-center justify-center rounded-lg [&_svg]:size-[58%]",
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
      <svg viewBox="0 0 24 24" fill="currentColor" className="text-white">
        <path d="M2.886 4.18A11.982 11.982 0 0 1 11.99 0C18.624 0 24 5.376 24 12.009c0 3.64-1.62 6.903-4.18 9.105L2.887 4.18ZM1.817 5.626l16.556 16.556c-.524.33-1.075.62-1.65.866L.951 7.277c.247-.575.537-1.126.866-1.65ZM.322 9.163l14.515 14.515c-.71.172-1.443.282-2.195.322L0 11.358a12 12 0 0 1 .322-2.195Zm-.17 4.862 9.823 9.824a12.02 12.02 0 0 1-9.824-9.824Z" />
      </svg>
    ),
  },
  notion: {
    bg: "bg-fg",
    node: (
      <svg viewBox="0 0 16 16" fill="currentColor" className="text-bg">
        <path d="M4.2 2.4h6.1l1.5 11.2H9.4L8.2 6.1 6.6 13.6H3.1L4.2 2.4Zm.8 1.6-.8 8.4h1.1l1.7-7.7h.6l1.8 7.7h1.2l-.9-8.4H5Z" />
      </svg>
    ),
  },
  sentry: {
    bg: "bg-[#362D59]",
    node: (
      <svg viewBox="0 0 16 16" fill="currentColor" className="text-[#F6B93B]">
        <path d="M8.9 2.2 14.6 12a1.2 1.2 0 0 1-1 1.8H9.2L8 11.4h2.3L8 6.2 4.2 13.8H2.4A1.2 1.2 0 0 1 1.4 12L7.1 2.2a1.2 1.2 0 0 1 1.8 0Z" />
      </svg>
    ),
  },
  github: {
    bg: "bg-[#24292F]",
    node: (
      <svg viewBox="0 0 16 16" fill="currentColor" className="text-white">
        <path d="M8 1.5A6.5 6.5 0 0 0 1.5 8c0 2.87 1.86 5.3 4.44 6.16.32.06.44-.14.44-.31v-1.1c-1.8.39-2.18-.87-2.18-.87-.3-.75-.73-.95-.73-.95-.6-.4.04-.4.04-.4.66.05 1.01.68 1.01.68.59 1 1.54.71 1.92.54.06-.43.23-.71.42-.88-1.46-.16-3-.73-3-3.25 0-.72.26-1.3.68-1.76-.07-.17-.3-.85.06-1.77 0 0 .55-.18 1.81.67A6.2 6.2 0 0 1 8 4.7c.56 0 1.12.08 1.65.22 1.26-.85 1.81-.67 1.81-.67.36.92.13 1.6.06 1.77.42.46.68 1.04.68 1.76 0 2.53-1.54 3.09-3.01 3.25.24.2.45.61.45 1.23v1.82c0 .17.12.37.44.31A6.51 6.51 0 0 0 14.5 8 6.5 6.5 0 0 0 8 1.5Z" />
      </svg>
    ),
  },
  slack: {
    bg: "bg-[#4A154B]",
    node: (
      <svg viewBox="0 0 24 24" fill="currentColor" className="text-white">
        <path d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52zM6.313 15.165a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313zM8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834zM8.834 6.313a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312zM18.956 8.834a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834zM17.688 8.834a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.165 0a2.528 2.528 0 0 1 2.523 2.522v6.312zM15.165 18.956a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.165 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52zM15.165 17.688a2.527 2.527 0 0 1-2.52-2.523 2.526 2.526 0 0 1 2.52-2.52h6.313A2.527 2.527 0 0 1 24 15.165a2.528 2.528 0 0 1-2.522 2.523h-6.313z" />
      </svg>
    ),
  },
  stripe: {
    bg: "bg-[#635BFF]",
    node: (
      <svg viewBox="0 0 24 24" fill="currentColor" className="text-white">
        <path d="M13.976 9.15c-2.172-.806-3.356-1.426-3.356-2.409 0-.831.683-1.305 1.901-1.305 2.227 0 4.515.858 6.09 1.631l.89-5.494C18.252.975 15.697 0 12.165 0 9.667 0 7.589.654 6.104 1.872 4.56 3.147 3.757 4.992 3.757 7.218c0 4.039 2.467 5.76 6.476 7.219 2.585.92 3.445 1.574 3.445 2.583 0 .98-.84 1.545-2.354 1.545-1.875 0-4.965-.921-6.99-2.109l-.9 5.555C5.175 22.99 8.385 24 11.714 24c2.641 0 4.843-.624 6.328-1.813 1.664-1.305 2.525-3.236 2.525-5.732 0-4.128-2.524-5.851-6.594-7.305h.003z" />
      </svg>
    ),
  },
  figma: {
    bg: "bg-[#1E1E1E]",
    node: (
      <svg viewBox="0 0 24 24">
        <path fill="#F24E1E" d="M8 24a4 4 0 0 0 4-4v-4H8a4 4 0 1 0 0 8z" />
        <path fill="#FF7262" d="M8 16h4v-4H8a4 4 0 1 0 0 4z" />
        <path fill="#A259FF" d="M8 8h4V0H8a4 4 0 1 0 0 8z" />
        <path fill="#1ABCFE" d="M16 8a4 4 0 1 0 0-8h-4v8h4z" />
        <circle fill="#0ACF83" cx="16" cy="12" r="4" />
      </svg>
    ),
  },
  custom: {
    bg: "bg-raised",
    node: <Plus className="text-muted" />,
  },
  unknown: {
    bg: "bg-raised",
    node: <Plug className="text-muted" />,
  },
};
