import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ChevronDown,
  History,
  Pause,
  Pencil,
  Play,
  Repeat,
  ShieldCheck,
  Square,
  Trash2,
  Zap,
} from "lucide-react";
import { useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type Routine,
} from "../../gen/podium/agent/v1/agent_pb";
import {
  isLinear,
  isSystem,
  money,
  routineEditPath,
  routineKey,
  routinesKey,
  routineStatus,
  runsText,
} from "../../lib/routines";
import { agent, errorMessage } from "../../lib/client";
import { relative, toDate } from "../../lib/format";
import { cn } from "../../lib/utils";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "../ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { useBrand } from "../../lib/brand";

/**
 * RoutineCard sits at the top of a routine's chat: when it runs, how it has been doing, and the
 * controls. Its details — the instruction, the standing notes and their history — open over the
 * chat rather than pushing it down, so the conversation never jumps. A personal routine is its
 * owner's alone; a system routine's card is shown to the admins who run it, marked as such.
 */
export function RoutineCard({ chatId }: { chatId: string }) {
  const brand = useBrand();
  const qc = useQueryClient();
  const toast = useToast();
  const routine = useQuery({
    queryKey: routineKey(chatId),
    queryFn: async () => (await agent.getRoutine({ chatId })).routine ?? null,
    // The next run and whether one is going move on their own.
    refetchInterval: 20_000,
  });
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const refresh = (r?: Routine) => {
    if (r) qc.setQueryData(routineKey(chatId), r);
    void qc.invalidateQueries({ queryKey: routinesKey });
  };
  const act = useMutation({
    mutationFn: async (
      what: "pause" | "resume" | "run" | "stop" | "accept" | "dismiss",
    ) => {
      const r = routine.data;
      if (!r) return undefined;
      switch (what) {
        case "pause":
          return (await agent.pauseRoutine({ id: r.id })).routine;
        case "resume":
          return (await agent.resumeRoutine({ id: r.id })).routine;
        case "run":
          await agent.runRoutineNow({ id: r.id });
          return undefined;
        case "stop":
          return (await agent.cancelRoutineRun({ id: r.id })).routine;
        case "accept":
          return (await agent.resolveRoutineNote({ id: r.id, accept: true }))
            .routine;
        case "dismiss":
          return (await agent.resolveRoutineNote({ id: r.id, accept: false }))
            .routine;
      }
    },
    onSuccess: (r, what) => {
      refresh(r);
      if (what === "run") {
        toast(
          "Running now. Its answer appears here if it finds something.",
          "ok",
        );
        void qc.invalidateQueries({ queryKey: routineKey(chatId) });
      }
    },
    onError: (err) => toast(errorMessage(err)),
  });
  const remove = useMutation({
    mutationFn: async () => {
      if (routine.data) await agent.deleteRoutine({ id: routine.data.id });
    },
    onSuccess: () => {
      setConfirmDelete(false);
      qc.setQueryData(routineKey(chatId), null);
      void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      void qc.invalidateQueries({ queryKey: routinesKey });
      toast("Routine deleted. This chat stays.", "ok");
    },
    onError: (err) => toast(errorMessage(err)),
  });

  // The details lie over the chat, so a click anywhere else, or Escape, puts them away.
  const cardRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      const t = e.target as Node | null;
      // A dialog opened from the card (Edit, Delete) is not "away".
      if (
        t &&
        !cardRef.current?.contains(t) &&
        !(t as Element).closest?.("[role=dialog]")
      )
        setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  // One height whatever is in it — 16rem — unless the card sits so low (a phone, where the chat
  // list is above it) that 16rem would run off the screen; then the room below the card.
  const [detailsHeight, setDetailsHeight] = useState(256);
  useLayoutEffect(() => {
    if (!open || !cardRef.current) return;
    const below = window.innerHeight - cardRef.current.getBoundingClientRect().bottom - 12;
    setDetailsHeight(Math.max(140, Math.min(256, Math.floor(below))));
  }, [open]);

  const r = routine.data;
  const history = useQuery({
    queryKey: ["agent", "routine-revisions", r?.id ?? ""],
    queryFn: async () => (await agent.listRoutineRevisions({ id: r?.id ?? "" })).revisions,
    enabled: open && Boolean(r),
  });
  if (!r) return null;
  const busy = act.isPending;
  const system = isSystem(r);
  const linear = isLinear(r);

  return (
    <Collapsible open={open} onOpenChange={setOpen} asChild>
      <section
        ref={cardRef}
        data-testid="routine-card"
        aria-label={`Routine ${r.name}`}
        // relative + z-20: the details hang below the card, over the transcript and under any
        // popover or dialog (z-50).
        className="relative z-20 mx-4 mt-3 rounded-lg border border-border bg-panel text-sm transition-[border-radius] data-[state=open]:rounded-b-none sm:mx-6"
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
          <div className="min-w-[min(100%,16rem)] flex-1 space-y-0.5">
            <p className="flex min-w-0 items-start gap-2 font-semibold text-fg">
              <Repeat
                className="mt-0.5 size-4 shrink-0 text-muted"
                aria-hidden
              />
              <span className="min-w-0">
                {runsText(r)}
                {!linear && r.timezone && r.timezone !== "UTC" ? (
                  <span className="font-normal text-faint">
                    {" "}
                    · {r.timezone}
                  </span>
                ) : null}
              </span>
              {system ? <SystemBadge className="ml-0.5 shrink-0" /> : null}
            </p>
            <p
              data-testid="routine-status"
              className="pl-6 text-xs text-muted tabular-nums"
            >
              {system ? `Runs as ${brand} · ` : ""}
              {routineStatus(r)}
              {r.lastRunAt ? ` · last ran ${relative(r.lastRunAt)}` : ""}
              {r.idleCount > 0
                ? ` · ${r.idleCount} quiet ${r.idleCount === 1 ? "run" : "runs"} since the last answer`
                : ""}
              {` · ${money(r.spendTodayUsd)} in 24h`}
            </p>
          </div>
          <span className="flex items-center gap-1">
            {r.running ? (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => act.mutate("stop")}
              >
                <Square aria-hidden />
                Stop run
              </Button>
            ) : (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => act.mutate("run")}
              >
                <Zap aria-hidden />
                Run now
              </Button>
            )}
            {r.enabled ? (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => act.mutate("pause")}
              >
                <Pause aria-hidden />
                Pause
              </Button>
            ) : (
              <Button
                size="xs"
                variant="outline"
                disabled={busy}
                onClick={() => act.mutate("resume")}
              >
                <Play aria-hidden />
                Resume
              </Button>
            )}
            <Button
              size="icon-xs"
              variant="ghost"
              aria-label="Edit routine"
              onClick={() => void navigate(routineEditPath(r.id))}
            >
              <Pencil aria-hidden />
            </Button>
            <CollapsibleTrigger asChild>
              <Button
                size="icon-xs"
                variant="ghost"
                aria-label={open ? "Hide details" : "Show details"}
              >
                <ChevronDown
                  aria-hidden
                  className={cn(
                    "transition-transform duration-200",
                    open && "rotate-180",
                  )}
                />
              </Button>
            </CollapsibleTrigger>
          </span>
        </div>

        {!r.enabled && r.pausedReason ? (
          <p
            data-testid="routine-paused-reason"
            className="border-t border-border px-3 py-2 text-xs text-warn"
          >
            {r.pausedReason}
          </p>
        ) : null}

        {r.proposedNote ? (
          <div
            data-testid="routine-proposed-note"
            className="flex flex-wrap items-center gap-2 border-t border-border px-3 py-2 text-xs"
          >
            {/* The agent suggests; only this click adds it, word for word, to the notes every run is given. */}
            <span className="text-muted">Suggested standing note:</span>
            <span className="min-w-[min(100%,14rem)] flex-1 font-medium text-fg">
              {r.proposedNote}
            </span>
            <span className="flex items-center gap-1">
              <Button
                size="xs"
                disabled={busy}
                onClick={() => act.mutate("accept")}
              >
                Add to notes
              </Button>
              <Button
                size="xs"
                variant="ghost"
                disabled={busy}
                onClick={() => act.mutate("dismiss")}
              >
                Dismiss
              </Button>
            </span>
          </div>
        ) : null}

        <CollapsibleContent
          data-testid="routine-details"
          className={cn(
            "absolute -inset-x-px top-full overflow-hidden rounded-b-lg border border-t-0 border-border bg-panel shadow-lg",
            "data-[state=open]:animate-collapsible-down data-[state=closed]:animate-collapsible-up",
          )}
        >
          <div className="flex flex-col border-t border-border text-xs" style={{ height: detailsHeight }}>
            <div className="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto px-3 py-3">
              <div>
                <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">
                  Instruction
                </h3>
                <p className="whitespace-pre-wrap text-fg">{r.instruction}</p>
              </div>
              <div>
                <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">
                  Standing notes
                </h3>
                {r.notes ? (
                  <p className="whitespace-pre-wrap text-fg">{r.notes}</p>
                ) : (
                  <p className="text-muted">
                    None. Add rules every run should follow under Edit.
                  </p>
                )}
              </div>
              <div>
                <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">
                  Daily spend cap
                </h3>
                <p className="text-fg tabular-nums">
                  {r.dailyCapUsd > 0 ? `${money(r.dailyCapUsd)} a day, ${money(r.spendTodayUsd)} spent in the last 24h` : "None"}
                </p>
              </div>
              <RunList routineId={r.id} />
              <RevisionList revisions={history.data} loading={history.isPending} />
            </div>
            <div className="flex shrink-0 justify-end border-t border-border px-2 py-1.5">
              <Button
                size="xs"
                variant="ghost"
                onClick={() => setConfirmDelete(true)}
              >
                <Trash2 aria-hidden />
                Delete routine
              </Button>
            </div>
          </div>
        </CollapsibleContent>

        <Dialog
          open={confirmDelete}
          onOpenChange={(o) => !o && setConfirmDelete(false)}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Delete {r.name}?</DialogTitle>
              <DialogDescription>
                It stops running and forgets what it kept. This chat and
                everything the routine said in it stay.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirmDelete(false)}>
                Keep it
              </Button>
              <Button
                variant="destructive"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                Delete routine
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </section>
    </Collapsible>
  );
}

/** runsKey is where a routine's recent runs are cached, quiet ones included. */
const runsKey = (routineId: string) => ["agent", "routine-runs", routineId];

/** useRecentRuns is a routine's latest runs, quiet ones included, newest first. */
function useRecentRuns(routineId: string, enabled = true) {
  return useQuery({
    queryKey: runsKey(routineId),
    queryFn: async () =>
      (await agent.listRoutineRuns({ id: routineId, includeIdle: true, page: { limit: 10 } })).runs,
    enabled: enabled && routineId !== "",
    refetchInterval: 30_000,
  });
}

const RUN_WORDS: Record<string, string> = {
  running: "Running",
  done: "Answered",
  idle: "Nothing to do",
  failed: "Failed",
  skipped_overlap: "Skipped, busy",
  skipped_paused: "Skipped, over its cap",
};

/**
 * RunList is what the routine's last runs did, quiet ones included: when each ran, how it
 * ended, and the line it reported — the run list a reader would otherwise scroll the chat for.
 */
function RunList({ routineId }: { routineId: string }) {
  const runs = useRecentRuns(routineId);
  return (
    <div data-testid="routine-runs">
      <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">Recent runs</h3>
      {runs.isPending ? (
        <p className="text-muted">Loading…</p>
      ) : !runs.data || runs.data.length === 0 ? (
        <p className="text-muted">It has not run yet.</p>
      ) : (
        <ol className="m-0 grid list-none gap-1 p-0">
          {runs.data.map((run) => (
            <li key={run.id} className="flex min-w-0 items-baseline gap-2">
              <time className="w-14 shrink-0 text-faint tabular-nums" title={run.createdAt ? toDate(run.createdAt)?.toLocaleString() : undefined}>
                {relative(run.createdAt)}
              </time>
              <span
                className={cn(
                  "shrink-0 font-medium",
                  run.status === "failed" ? "text-err" : run.status === "done" ? "text-fg" : "text-muted",
                )}
              >
                {RUN_WORDS[run.status] ?? run.status}
              </span>
              {run.summary ? <span className="min-w-0 truncate text-muted" title={run.summary}>{run.summary}</span> : null}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/**
 * QuietRuns says, in a routine's empty chat, that it has been checking: how many times and what
 * the latest check found. Without it a routine that has found nothing yet looks like it never ran.
 */
export function QuietRuns({ routineId }: { routineId: string }) {
  const runs = useRecentRuns(routineId);
  const settled = (runs.data ?? []).filter((r) => r.status !== "skipped_overlap" && r.status !== "skipped_paused");
  const latest = settled[0];
  if (!latest) return null;
  return (
    <p data-testid="routine-quiet-runs" className="mx-auto mt-2 max-w-md rounded-md border border-border bg-panel px-3 py-2 text-xs text-muted">
      Checked {settled.length === 10 ? "10+" : settled.length} {settled.length === 1 ? "time" : "times"} · last{" "}
      {relative(latest.createdAt)}
      {latest.summary ? <span className="text-fg"> — {latest.summary}</span> : null}
    </p>
  );
}

type Revision = Awaited<ReturnType<typeof agent.listRoutineRevisions>>["revisions"][number];

/**
 * RevisionList is what the routine has been told over time: each change to its instruction,
 * notes or schedule, who made it and when. Newest first; the last is the routine as made.
 */
function RevisionList({ revisions, loading }: { revisions?: Revision[]; loading: boolean }) {
  return (
    <div data-testid="routine-history">
      <h3 className="mb-1 flex items-center gap-1.5 font-semibold text-muted uppercase tracking-[0.08em]">
        <History className="size-3.5" aria-hidden />
        History
      </h3>
      {loading ? (
        <p className="text-muted">Loading…</p>
      ) : !revisions || revisions.length === 0 ? (
        <p className="text-muted">No changes recorded.</p>
      ) : (
        <ol className="m-0 grid list-none gap-1 p-0">
          {revisions.map((v, i) => {
            const older = revisions[i + 1];
            const changed = older
              ? [
                  v.instruction !== older.instruction && "instruction",
                  v.notes !== older.notes && "notes",
                  (v.schedule !== older.schedule || v.timezone !== older.timezone) && "schedule",
                ].filter(Boolean)
              : [];
            return (
              <li key={String(v.id)}>
                <details className="group rounded-sm">
                  <summary className="flex cursor-pointer list-none items-baseline gap-1.5 rounded-sm py-0.5 text-fg outline-none hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                    <ChevronDown className="size-3 shrink-0 -rotate-90 self-center text-faint transition-transform group-open:rotate-0" aria-hidden />
                    <span className="min-w-0 truncate">
                      {older ? `Changed the ${changed.join(" and ") || "routine"}` : "Set up"}
                    </span>
                    <span className="shrink-0 text-faint">
                      {v.changedBy || "someone"} · {relative(v.changedAt)}
                    </span>
                  </summary>
                  <p className="mt-1 mb-1.5 ml-4.5 whitespace-pre-wrap border-l-2 border-line-strong pl-2 text-muted">
                    {v.instruction}
                    {v.notes ? `\n\n${v.notes}` : ""}
                  </p>
                </details>
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}

/** SystemBadge marks a routine that runs as nobody, wherever one is listed, so it is never taken for somebody's own. */
export function SystemBadge({ className }: { className?: string }) {
  return (
    <span
      data-testid="routine-system-badge"
      title="A system routine: admins set it up, and it runs as Podium"
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-sm border border-line-strong bg-raised px-1.5 py-0.5 text-[11px] font-semibold tracking-[0.02em] text-muted",
        className,
      )}
    >
      <ShieldCheck className="size-3" aria-hidden />
      System
    </span>
  );
}
