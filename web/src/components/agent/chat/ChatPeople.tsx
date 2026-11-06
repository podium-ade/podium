import type { ChatPerson, ChatSurface } from "../../../gen/podium/agent/v1/agent_pb";
import { surfaceName } from "../../../lib/surfaces";
import { Avatar, OriginMark } from "./Origin";

/** MAX_FACES is how many avatars the row draws before it says "+n". */
const MAX_FACES = 6;

/**
 * ChatPeople is a conversation's header strip: who is in it, and where else it lives. It is
 * drawn only for a conversation with more than one surface or more than one person — a web
 * chat with its one owner has nothing to say here.
 *
 * Each person is an avatar with their name and surface on hover; somebody who is not in
 * Podium is marked so. A surface is a chip that opens the conversation there when Podium
 * knows the link.
 */
export function ChatPeople({ people, surfaces }: { people: ChatPerson[]; surfaces: ChatSurface[] }) {
  if (people.length === 0 && surfaces.length === 0) return null;
  const shown = people.slice(0, MAX_FACES);
  const more = people.length - shown.length;
  return (
    <div
      data-testid="chat-people"
      className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border px-5 py-2"
    >
      {surfaces.length > 0 ? (
        <ul className="flex flex-wrap items-center gap-2" aria-label="Where this conversation lives">
          {surfaces.map((s) => (
            <li key={`${s.kind}:${s.label}`}>
              <SurfaceChip surface={s} />
            </li>
          ))}
        </ul>
      ) : null}
      {people.length > 0 ? (
        <ul className="flex items-center -space-x-1.5" aria-label="People in this conversation">
          {shown.map((p, i) => {
            const label = `${p.name}${p.login ? "" : " (not in Podium)"} · ${surfaceName(p.via)}`;
            return (
              <li
                key={`${p.login || p.name}:${i}`}
                data-testid="chat-person"
                data-external={p.login ? "false" : "true"}
                title={label}
                aria-label={label}
                className="rounded-full ring-2 ring-bg"
              >
                <Avatar name={p.name} src={p.avatar} className={p.login ? "" : "opacity-70"} />
              </li>
            );
          })}
          {more > 0 ? (
            <li className="pl-3 text-2xs text-faint" data-testid="chat-people-more">
              +{more}
            </li>
          ) : null}
        </ul>
      ) : null}
    </div>
  );
}

function SurfaceChip({ surface }: { surface: ChatSurface }) {
  const body = (
    <>
      <OriginMark via={surface.kind} className="size-4 border-0 bg-transparent" />
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
