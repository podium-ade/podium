import type { ChatPerson, ChatSurface } from "../../../gen/podium/agent/v1/agent_pb";
import { surfaceName } from "../../../lib/surfaces";
import { cn } from "../../../lib/utils";
import { Avatar, OriginMark } from "./Origin";

/** MAX_FACES is how many avatars the row draws before it says "+n". */
const MAX_FACES = 6;

/**
 * ChatSurfaces is where else a conversation lives, as chips at the start of its header bar.
 * A chip opens the conversation there when Podium knows the link.
 */
export function ChatSurfaces({ surfaces }: { surfaces: ChatSurface[] }) {
  if (surfaces.length === 0) return null;
  return (
    <ul
      className="flex min-w-0 shrink animate-in items-center gap-2 overflow-hidden fade-in-0 duration-300"
      aria-label="Where this conversation lives"
    >
      {surfaces.map((s) => (
        <li key={`${s.kind}:${s.label}`} className="flex min-w-0">
          <SurfaceChip surface={s} />
        </li>
      ))}
    </ul>
  );
}

/**
 * ChatPeople is who is in a conversation, as overlapping faces at the end of its header bar.
 * Each face has the person's name and surface on hover; somebody who is not in Podium is
 * marked so. A web chat with its one owner has nobody else to show and draws nothing.
 */
export function ChatPeople({ people }: { people: ChatPerson[] }) {
  if (people.length === 0) return null;
  const shown = people.slice(0, MAX_FACES);
  const more = people.length - shown.length;
  return (
    <ul
      data-testid="chat-people"
      className="flex shrink-0 animate-in items-center -space-x-1.5 fade-in-0 duration-300"
      aria-label="People in this conversation"
    >
      {shown.map((p, i) => {
        const label = `${p.name}${p.login ? "" : " (not in Podium)"} · ${surfaceName(p.via)}`;
        return (
          <li
            key={`${p.login || p.name}:${i}`}
            data-testid="chat-person"
            data-external={p.login ? "false" : "true"}
            title={label}
            aria-label={label}
            className="flex rounded-full ring-2 ring-bg"
          >
            <Avatar
              name={p.name}
              src={p.avatar}
              login={p.login || undefined}
              className={cn("size-7", p.login ? "" : "opacity-70")}
            />
          </li>
        );
      })}
      {more > 0 ? (
        <li className="pl-3 text-2xs text-faint" data-testid="chat-people-more">
          +{more}
        </li>
      ) : null}
    </ul>
  );
}

function SurfaceChip({ surface }: { surface: ChatSurface }) {
  const body = (
    <>
      {/* The marks are drawn for a white tile; on a transparent one GitHub's black mark
          disappears in the dark theme. */}
      <OriginMark via={surface.kind} className="size-4 rounded-full border-0" />
      <span className="truncate">{surface.label}</span>
    </>
  );
  const cls =
    "inline-flex max-w-64 items-center gap-1.5 rounded-full border border-border bg-panel px-2 py-0.5 text-xs text-muted";
  if (surface.url) {
    return (
      <a
        data-testid="chat-surface"
        href={surface.url}
        target="_blank"
        rel="noreferrer"
        className={`${cls} hover:border-accent hover:text-fg`}
      >
        {body}
      </a>
    );
  }
  return (
    <span data-testid="chat-surface" className={cls}>
      {body}
    </span>
  );
}
