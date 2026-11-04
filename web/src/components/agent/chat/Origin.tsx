import { Globe } from "lucide-react";
import github from "../../../assets/marks/github.svg";
import slack from "../../../assets/marks/slack.svg";
import { surfaceName } from "../../../lib/surfaces";
import { cn } from "../../../lib/utils";

const MARKS: Record<string, string> = { github, slack };

/**
 * OriginMark is the small badge saying where a message was said: the Slack or GitHub mark,
 * or a globe for the web chat. With a url it is a link to the message there.
 */
export function OriginMark({ via, url, className }: { via?: string; url?: string; className?: string }) {
  if (!via || via === "bot") return null;
  const src = MARKS[via];
  const label = url ? `Open in ${surfaceName(via)}` : `Said in ${surfaceName(via)}`;
  const mark = src ? (
    <img src={src} alt="" className="size-3.5" />
  ) : (
    <Globe aria-hidden className="size-3.5 text-muted" />
  );
  const box = cn(
    "inline-grid size-5 shrink-0 place-items-center rounded-sm border border-line-strong bg-white",
    className,
  );
  if (!url) {
    return (
      <span data-testid="chat-origin" data-via={via} title={label} aria-label={label} className={box}>
        {mark}
      </span>
    );
  }
  return (
    <a
      data-testid="chat-origin"
      data-via={via}
      href={url}
      target="_blank"
      rel="noreferrer"
      title={label}
      aria-label={label}
      className={cn(box, "hover:border-accent focus-visible:outline-2 focus-visible:outline-accent")}
    >
      {mark}
    </a>
  );
}

/**
 * Avatar is a person's picture, or their initials when there is none. It is decoration:
 * the name is always in the text beside it.
 */
export function Avatar({ name, src, className }: { name: string; src?: string; className?: string }) {
  const initials =
    name
      .split(/[\s._@-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join("") || "?";
  return (
    <span
      aria-hidden
      className={cn(
        "inline-grid size-6 shrink-0 place-items-center overflow-hidden rounded-full border border-border bg-panel text-2xs font-medium text-muted",
        className,
      )}
    >
      {src ? <img src={src} alt="" className="size-full object-cover" /> : initials}
    </span>
  );
}
