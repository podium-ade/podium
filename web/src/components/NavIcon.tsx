import { cn } from "../lib/utils";

/**
 * Side-menu marks from the design shell. Each path is the product-nav icon
 * for that page, drawn in a 24 viewBox at 18px with a 1.8 stroke.
 */
export const NAV_ICON_PATHS = {
  chat: "M4 5h16v11H9l-5 4V5Z M7 9h10 M7 12h7",
  sessions: "M4 4v5h5 M4.8 9a8 8 0 1 1-.6 5 M12 7v5l3 2",
  memory:
    "M8 5a3 3 0 0 0-3 3 3 3 0 0 0 0 5 3 3 0 0 0 3 5c1.3 0 2.2-.7 3-1.6V6.6C10.2 5.7 9.3 5 8 5Zm8 0a3 3 0 0 1 3 3 3 3 0 0 1 0 5 3 3 0 0 1-3 5c-1.3 0-2.2-.7-3-1.6V6.6c.8-.9 1.7-1.6 3-1.6Z",
  assistants:
    "M8 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6Zm8 0a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM3 20v-3a5 5 0 0 1 10 0v3H3Zm10-7a5 5 0 0 1 8 4v3h-5",
  tasks: "M8 4h12v16H4V8l4-4Zm0 0v4H4m5 5 2 2 4-4m-6 7h7",
  nodes: "M12 4v5M6 15l6-6 6 6M3 15h6v5H3v-5Zm12 0h6v5h-6v-5ZM9 3h6v6H9V3Z",
  playbooks: "M5 4h13a2 2 0 0 1 2 2v14H7a3 3 0 0 1-3-3V6a2 2 0 0 1 1-2Zm2 0v16m3-11h7m-7 4h5",
  skills:
    "m12 2 1.8 6.2L20 10l-6.2 1.8L12 18l-1.8-6.2L4 10l6.2-1.8L12 2Zm7 15 .6 2.4L22 20l-2.4.6L19 23l-.6-2.4L16 20l2.4-.6L19 17Z",
  mcp: "M9 3v6M15 3v6M7 9h10v4a5 5 0 0 1-10 0V9Zm5 9v3",
  channels: "M4 6h16v12H4V6Zm0 2 8 6 8-6",
  secrets: "M5 11h14v10H5V11Zm3 0V8a4 4 0 0 1 8 0v3m-4 4v2",
  registries: "M4 5h16v5H4V5Zm0 9h16v5H4v-5Zm3-6h1m-1 9h1",
  usage: "M4 20V12h4v8H4Zm6 0V4h4v16h-4Zm6 0v-9h4v9h-4Z",
  settings:
    "M12 3v2m0 14v2M3 12h2m14 0h2M5.6 5.6 7 7m10 10 1.4 1.4M18.4 5.6 17 7M7 17l-1.4 1.4M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z",
} as const;

export type NavIconName = keyof typeof NAV_ICON_PATHS;

const stroke = {
  viewBox: "0 0 24 24",
  "aria-hidden": true,
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.8,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
};

export function NavIcon({ name, className }: { name: NavIconName; className?: string }) {
  return (
    <svg {...stroke} className={cn("block size-[18px] shrink-0", className)}>
      <path d={NAV_ICON_PATHS[name]} />
    </svg>
  );
}

/** Collapse control from the design symbol `#shell-collapse`. */
export function CollapseIcon({ className }: { className?: string }) {
  return (
    <svg {...stroke} className={cn("block size-[18px] shrink-0", className)}>
      <rect x="3" y="4" width="18" height="16" rx="1" />
      <path d="M9 4v16m7-11-3 3 3 3" />
    </svg>
  );
}
