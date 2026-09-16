import { useState } from "react";
import { cn } from "@/lib/utils";

/**
 * Avatar is a round photo when we have one (Google Workspace), otherwise the first
 * letter of the label. Google's avatar CDN rejects a Referer from localhost, so the
 * image is loaded with no-referrer.
 */
export function Avatar({
  src,
  label,
  size = "sm",
  className,
}: {
  src?: string;
  label: string;
  size?: "sm" | "md";
  className?: string;
}) {
  const [failed, setFailed] = useState(false);
  const initial = (label.trim().slice(0, 1) || "?").toUpperCase();
  const dim = size === "md" ? "size-9 text-xs" : "size-7 text-2xs";

  if (src && !failed) {
    return (
      <img
        src={src}
        alt=""
        referrerPolicy="no-referrer"
        onError={() => setFailed(true)}
        className={cn("shrink-0 rounded-full object-cover", dim, className)}
      />
    );
  }

  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center rounded-full bg-raised font-semibold text-muted uppercase ring-1 ring-border",
        dim,
        className,
      )}
    >
      {initial}
    </span>
  );
}
