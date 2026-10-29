import { cn } from "../lib/utils";

/**
 * PodiumMark is the product mark: a live terminal session with more queued behind it. On the dark
 * shell it is the inverted mark, the theme's ink tile with the terminal in the raised grey. The
 * wordmark beside it names the product, so the mark has no label.
 */
export function PodiumMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" aria-hidden="true" className={cn("shrink-0", className)}>
      <rect width="64" height="64" rx="8" className="fill-fg" />
      <rect x="20" y="12" width="24" height="4" rx="2" opacity=".45" className="fill-raised" />
      <rect x="16" y="18" width="32" height="4" rx="2" opacity=".7" className="fill-raised" />
      <rect x="12" y="24" width="40" height="28" rx="4" className="fill-raised" />
      <path
        d="M18.4 33l6 5-6 5"
        fill="none"
        strokeWidth="2.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="stroke-fg"
      />
      <g className="fill-fg">
        <rect x="28.8" y="38.5" width="5" height="4.5" rx="1.2" />
        <rect x="35.4" y="38.5" width="5" height="4.5" rx="1.2" opacity=".6" />
        <rect x="42" y="38.5" width="5" height="4.5" rx="1.2" opacity=".3" />
      </g>
    </svg>
  );
}
