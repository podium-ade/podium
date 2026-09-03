import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import type { Session } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../../lib/client";
import { absolute, conversationLabel, relative, turnCost } from "../../lib/format";
import { Badge, Chip, type Tone } from "../Badge";
import { Empty } from "../Empty";
import { TableSkeleton } from "../Skeleton";

/** How often the list refreshes while the tab is open. A turn takes minutes, not seconds. */
const POLL_MS = 10_000;

const KIND_TONE: Record<string, Tone> = {
  slack: "run",
  linear: "lost",
  chat: "ok",
  dev: "warn",
};

const TURN_TONE: Record<string, Tone> = {
  running: "run",
  succeeded: "ok",
  failed: "err",
  cancelled: "warn",
  timeout: "warn",
  lost: "lost",
};

/**
 * SessionsTable is the conversations the bot has taken part in, and the turns inside one.
 *
 * Everything here comes from the conductor's read RPCs; nothing is derived from a source's
 * own API, because the conductor deliberately keeps no copy of a Slack channel name. The ref
 * shown is what the source computed and the conductor stores verbatim.
 */
export function SessionsTable() {
  const [openID, setOpenID] = useState<string>();

  const sessions = useQuery({
    queryKey: ["agent", "sessions"],
    queryFn: () => agent.listSessions({}),
    refetchInterval: POLL_MS,
  });

  if (sessions.isPending) return <TableSkeleton rows={5} cols={5} />;
  if (sessions.isError) {
    return <Empty title="Could not list sessions" hint={errorMessage(sessions.error)} />;
  }

  const rows = sessions.data.sessions;
  if (rows.length === 0) {
    return <Empty title="No sessions yet." hint="Mention the bot in Slack to start one." />;
  }

  const open = rows.find((s) => s.id === openID);

  return (
    <>
      <div className="overflow-x-auto rounded border border-border">
        <table className="w-full text-left text-sm">
          <thead className="bg-raised text-xs text-muted">
            <tr>
              <th className="px-3 py-2 font-medium">Source</th>
              <th className="px-3 py-2 font-medium">Conversation</th>
              <th className="px-3 py-2 font-medium">Skill</th>
              <th className="px-3 py-2 font-medium">Started</th>
              <th className="px-3 py-2 font-medium">Last turn</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr
                key={s.id}
                data-testid="session-row"
                tabIndex={0}
                onClick={() => setOpenID(s.id)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    setOpenID(s.id);
                  }
                }}
                className="cursor-pointer border-t border-border hover:bg-raised focus-visible:ring-1 focus-visible:ring-accent"
              >
                <td className="px-3 py-2">
                  <Badge tone={KIND_TONE[s.sourceKind] ?? "idle"}>{s.sourceKind}</Badge>
                </td>
                <td className="px-3 py-2 font-mono text-xs">{conversationLabel(s)}</td>
                <td className="px-3 py-2">
                  <Chip>{s.skill}</Chip>
                </td>
                <td className="px-3 py-2 text-xs text-muted" title={absolute(s.createdAt)}>
                  {relative(s.createdAt)}
                </td>
                <td className="px-3 py-2 text-xs text-muted" title={absolute(s.lastTurnAt)}>
                  {relative(s.lastTurnAt)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open ? <TurnsDrawer session={open} onClose={() => setOpenID(undefined)} /> : null}
    </>
  );
}

function TurnsDrawer({ session, onClose }: { session: Session; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const turns = useQuery({
    queryKey: ["agent", "turns", session.id],
    queryFn: () => agent.listTurns({ sessionId: session.id }),
    refetchInterval: POLL_MS,
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Turns of ${conversationLabel(session)}`}
      className="fixed inset-y-0 right-0 z-40 flex w-full max-w-xl flex-col border-l border-border bg-panel shadow-2xl"
    >
      <header className="flex items-center gap-3 border-b border-border px-4 py-3">
        <Badge tone={KIND_TONE[session.sourceKind] ?? "idle"}>{session.sourceKind}</Badge>
        <span className="truncate font-mono text-xs">{conversationLabel(session)}</span>
        <Chip>{session.skill}</Chip>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="ml-auto rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
        >
          Close
        </button>
      </header>
      <div className="flex-1 overflow-y-auto p-4">
        {turns.isPending ? <TableSkeleton rows={3} cols={4} /> : null}
        {turns.isError ? (
          <Empty title="Could not list turns" hint={errorMessage(turns.error)} />
        ) : null}
        {turns.data?.turns.length === 0 ? <Empty title="No turns in this session yet." /> : null}
        <ul className="space-y-3">
          {turns.data?.turns.map((t) => (
            <li
              key={t.id}
              data-testid="turn-row"
              className="rounded border border-border bg-raised px-3 py-2 text-xs"
            >
              <div className="flex flex-wrap items-center gap-2">
                <Badge tone={TURN_TONE[t.status] ?? "idle"}>{t.status}</Badge>
                <span className="text-muted" title={absolute(t.startedAt)}>
                  {relative(t.startedAt)}
                </span>
                {t.finishedAt ? (
                  <span className="text-muted">→ {absolute(t.finishedAt)}</span>
                ) : null}
                {t.taskId ? (
                  <Link
                    to={`/tasks/${t.taskId}`}
                    className="ml-auto font-mono text-accent hover:underline"
                  >
                    {t.taskId}
                  </Link>
                ) : (
                  // A turn whose task was never created has nothing to link to, and saying so
                  // is the point: there are no logs to go and look for.
                  <span className="ml-auto text-muted">no task</span>
                )}
              </div>
              <div className="mt-1 flex flex-wrap gap-3 text-muted">
                <span>{turnCost(t)}</span>
                <span>{t.numTurns === undefined ? "—" : `${t.numTurns} model turns`}</span>
              </div>
              {t.finalText ? (
                <p className="mt-2 line-clamp-4 whitespace-pre-wrap text-fg">{t.finalText}</p>
              ) : null}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
