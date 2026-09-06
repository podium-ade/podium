import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import type { LucideIcon } from "lucide-react";
import {
  ChevronRight,
  Hash,
  History,
  MessageSquare,
  SquareKanban,
  Terminal,
  X,
} from "lucide-react";
import type { Session, Turn } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, conversationLabel, relative, taskDuration, turnCost } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Badge, Chip, type Tone } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { Button } from "../ui/button";
import { Separator } from "../ui/separator";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../ui/table";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";

/** How often the list refreshes while the tab is open. A turn takes minutes, not seconds. */
const POLL_MS = 10_000;

/** One glyph and one colour per source, so a row is placed before it is read. */
const KIND: Record<string, { icon: LucideIcon; tone: Tone; label: string }> = {
  chat: { icon: MessageSquare, tone: "ok", label: "chat" },
  slack: { icon: Hash, tone: "run", label: "slack" },
  linear: { icon: SquareKanban, tone: "lost", label: "linear" },
  dev: { icon: Terminal, tone: "warn", label: "dev" },
};

function kindOf(sourceKind: string) {
  return KIND[sourceKind] ?? { icon: MessageSquare, tone: "idle" as Tone, label: sourceKind };
}

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

  const rows = sessions.data?.sessions ?? [];
  const open = rows.find((s) => s.id === openID);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        description="Every conversation the conductor has taken part in — from the web chat, from Slack and from Linear — and the turns it spent inside each one."
        meta={
          rows.length > 0 ? (
            <>
              <Chip>
                {rows.length} {rows.length === 1 ? "session" : "sessions"}
              </Chip>
              <Chip>refreshed every {POLL_MS / 1000}s</Chip>
            </>
          ) : null
        }
      />

      {sessions.isPending ? <TableSkeleton rows={5} cols={6} /> : null}

      {sessions.isError ? (
        isAgentUnreachable(sessions.error) ? (
          <ConductorDown
            what="Could not list sessions"
            onRetry={() => void sessions.refetch()}
            retrying={sessions.isFetching}
          />
        ) : (
          <Empty
            icon={History}
            title="Could not list sessions"
            hint={errorMessage(sessions.error)}
            action={
              <Button variant="outline" size="sm" onClick={() => void sessions.refetch()}>
                Try again
              </Button>
            }
          />
        )
      ) : null}

      {sessions.isSuccess && rows.length === 0 ? (
        <Empty
          icon={History}
          title="No sessions yet."
          hint="Mention the bot in Slack to start one, or open Chat and ask it something — a session is recorded the first time it answers."
        />
      ) : null}

      {rows.length > 0 ? (
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Source</TableHead>
              <TableHead>Conversation</TableHead>
              <TableHead>Skill</TableHead>
              <TableHead>Profile</TableHead>
              <TableHead>Started</TableHead>
              <TableHead>Last turn</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((s) => {
              const kind = kindOf(s.sourceKind);
              return (
                <TableRow
                  key={s.id}
                  data-testid="session-row"
                  tabIndex={0}
                  aria-label={`Turns of ${conversationLabel(s)}`}
                  data-state={s.id === openID ? "selected" : undefined}
                  onClick={() => setOpenID(s.id)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      setOpenID(s.id);
                    }
                  }}
                  className="cursor-pointer outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
                >
                  <TableCell>
                    <Badge tone={kind.tone} dot={false}>
                      <kind.icon aria-hidden className="size-3" />
                      {kind.label}
                    </Badge>
                  </TableCell>
                  <TableCell className="max-w-[22rem]">
                    <div className="truncate font-mono text-xs text-fg" title={s.sourceKey}>
                      {conversationLabel(s)}
                    </div>
                    <div className="truncate font-mono text-2xs text-faint">{s.id}</div>
                  </TableCell>
                  <TableCell>
                    <Chip>{s.skill}</Chip>
                  </TableCell>
                  <TableCell className="text-xs text-muted">{s.profile || "—"}</TableCell>
                  <TableCell
                    className="text-xs whitespace-nowrap text-faint"
                    title={absolute(s.createdAt)}
                  >
                    {relative(s.createdAt)}
                  </TableCell>
                  <TableCell
                    className="text-xs whitespace-nowrap text-fg tabular"
                    title={absolute(s.lastTurnAt)}
                  >
                    {relative(s.lastTurnAt)}
                  </TableCell>
                  <TableCell className="w-0">
                    <ChevronRight
                      aria-hidden
                      className="size-4 text-faint transition-colors group-hover/row:text-muted"
                    />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      ) : null}

      {open ? <TurnsDrawer session={open} onClose={() => setOpenID(undefined)} /> : null}
    </div>
  );
}

/**
 * TurnsDrawer is one session's history. A turn is an event, not a row of a spreadsheet —
 * it has a moment, a duration and an outcome — so it is drawn as a timeline against a rail
 * rather than as a second table nested inside the first.
 */
function TurnsDrawer({ session, onClose }: { session: Session; onClose: () => void }) {
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  // The drawer is the thing that just opened, so the keyboard goes into it rather than
  // staying on a row behind an overlay.
  useEffect(() => {
    panelRef.current?.focus();
  }, [session.id]);

  const turns = useQuery({
    queryKey: ["agent", "turns", session.id],
    queryFn: () => agent.listTurns({ sessionId: session.id }),
    refetchInterval: POLL_MS,
  });

  const kind = kindOf(session.sourceKind);
  const rows = turns.data?.turns ?? [];
  const spent = rows.reduce((sum, t) => sum + (t.costUsd ?? 0), 0);

  return (
    <>
      <div
        aria-hidden
        onClick={onClose}
        className="fixed inset-0 z-40 bg-black/55 backdrop-blur-[2px] animate-in fade-in-0 duration-150"
      />
      <div
        ref={panelRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={`Turns of ${conversationLabel(session)}`}
        className="fixed inset-y-0 right-0 z-50 flex w-full max-w-xl flex-col border-l border-border bg-panel shadow-lg animate-in slide-in-from-right-4 fade-in-0 duration-200"
      >
        <header className="shrink-0 space-y-3 border-b border-border px-5 py-4">
          <div className="flex items-start gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-raised/60">
              <kind.icon aria-hidden className="size-4 text-muted" />
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate font-mono text-sm text-fg" title={session.sourceKey}>
                {conversationLabel(session)}
              </p>
              <p className="mt-1 flex flex-wrap items-center gap-1.5">
                <Badge tone={kind.tone} dot={false}>
                  <kind.icon aria-hidden className="size-3" />
                  {kind.label}
                </Badge>
                <Chip>{session.skill}</Chip>
                <Chip>{session.profile || "no profile"}</Chip>
              </p>
            </div>
            <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
              <X />
            </Button>
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-faint">
            <span>
              <span className="tabular text-muted">{rows.length}</span>{" "}
              {rows.length === 1 ? "turn" : "turns"}
            </span>
            <Separator orientation="vertical" className="h-3" />
            <span>
              <span className="tabular text-muted">${spent.toFixed(4)}</span> spent
            </span>
            <Separator orientation="vertical" className="h-3" />
            <span title={absolute(session.createdAt)}>started {relative(session.createdAt)}</span>
          </div>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          {turns.isPending ? <TableSkeleton rows={3} cols={3} /> : null}
          {turns.isError ? (
            isAgentUnreachable(turns.error) ? (
              <ConductorDown
                what="Could not list turns"
                onRetry={() => void turns.refetch()}
                retrying={turns.isFetching}
              />
            ) : (
              <Empty icon={History} title="Could not list turns" hint={errorMessage(turns.error)} />
            )
          ) : null}
          {turns.isSuccess && rows.length === 0 ? (
            <Empty
              icon={History}
              title="No turns in this session yet."
              hint="The session exists, but nothing has been asked in it since it was created."
            />
          ) : null}

          {rows.length > 0 ? (
            <ol className="relative space-y-3 border-l border-border pl-6">
              {rows.map((t) => (
                <TurnItem key={t.id} turn={t} />
              ))}
            </ol>
          ) : null}
        </div>
      </div>
    </>
  );
}

function TurnItem({ turn: t }: { turn: Turn }) {
  const tone = TURN_TONE[t.status] ?? "idle";
  return (
    <li data-testid="turn-row" className="relative">
      {/* The rail marker. It repeats the badge's colour, which is why it carries no meaning
          of its own and is hidden from a screen reader. */}
      <span
        aria-hidden
        className={cn(
          "absolute top-3 -left-[1.9rem] size-2.5 rounded-full ring-4 ring-panel",
          tone === "ok" && "bg-ok",
          tone === "err" && "bg-err",
          tone === "warn" && "bg-warn",
          tone === "run" && "bg-run",
          tone === "lost" && "bg-lost",
          tone === "idle" && "bg-idle",
        )}
      />
      <div className="rounded-lg border border-border bg-card px-3 py-2.5 text-xs shadow-xs">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={tone}>{t.status}</Badge>
          <span className="text-muted" title={absolute(t.startedAt)}>
            {relative(t.startedAt)}
          </span>
          {t.taskId ? (
            <Tooltip label={`Open task ${t.taskId}`}>
              <Link
                to={`/tasks/${t.taskId}`}
                className="ml-auto max-w-[12rem] truncate font-mono text-2xs text-accent hover:underline"
              >
                {t.taskId}
              </Link>
            </Tooltip>
          ) : (
            // A turn whose task was never created has nothing to link to, and saying so
            // is the point: there are no logs to go and look for.
            <span className="ml-auto text-2xs text-faint">no task</span>
          )}
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-hairline pt-2 text-2xs text-faint">
          <span>
            ran for{" "}
            <span className="tabular text-muted">{taskDuration(t.startedAt, t.finishedAt)}</span>
          </span>
          <span aria-hidden>·</span>
          <span className="tabular text-muted">
            {t.numTurns === undefined ? "— model turns" : `${t.numTurns} model turns`}
          </span>
          <span aria-hidden>·</span>
          <span>
            cost <span className="tabular text-muted">{turnCost(t)}</span>
          </span>
        </div>
        {t.finalText ? (
          <p className="mt-2 line-clamp-4 border-t border-hairline pt-2 whitespace-pre-wrap text-fg">
            {t.finalText}
          </p>
        ) : null}
      </div>
    </li>
  );
}
