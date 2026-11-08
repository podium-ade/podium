import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
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
  User,
  Wrench,
  X,
  Zap,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type Routine,
  type RoutineDraft,
} from "../../gen/podium/agent/v1/agent_pb";
import {
  browserZone,
  CI_FIXER,
  draftOf,
  isSystem,
  money,
  parseRepo,
  routineKey,
  routinesKey,
  routineStatus,
  type Scope,
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
import { Input } from "../ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../ui/select";
import { Textarea } from "../ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { DEFAULT_REPEAT, toCron } from "../../lib/schedule";
import { ScheduleField } from "./ScheduleField";
import { TimezonePicker } from "./TimezonePicker";

/**
 * RoutineCard sits at the top of a routine's chat: when it runs, how it has been doing, and the
 * controls. Its details — the instruction, the standing notes and their history — open over the
 * chat rather than pushing it down, so the conversation never jumps. A personal routine is its
 * owner's alone; a system routine's card is shown to the admins who run it, marked as such.
 */
export function RoutineCard({ chatId }: { chatId: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const routine = useQuery({
    queryKey: routineKey(chatId),
    queryFn: async () => (await agent.getRoutine({ chatId })).routine ?? null,
    // The next run and whether one is going move on their own.
    refetchInterval: 20_000,
  });
  const [editing, setEditing] = useState(false);
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
                Runs {r.scheduleText}
                {r.timezone && r.timezone !== "UTC" ? (
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
              {system ? "Runs as Podium · " : ""}
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
              onClick={() => setEditing(true)}
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
              {system ? (
                <div>
                  <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">
                    Repositories
                  </h3>
                  {r.repos.length > 0 ? (
                    <p className="flex flex-wrap gap-1">
                      {r.repos.map((repo) => (
                        <span key={repo} className="rounded-sm bg-raised px-1.5 py-0.5 font-mono text-fg">
                          {repo}
                        </span>
                      ))}
                    </p>
                  ) : (
                    <p className="text-muted">None, so it reads nothing on GitHub. Add them under Edit.</p>
                  )}
                </div>
              ) : null}
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

        {editing ? (
          <RoutineDialog
            open
            title="Edit routine"
            initial={draftOf(r)}
            lockAssistant
            scope={system ? "system" : "personal"}
            onClose={() => setEditing(false)}
            onSave={async (draft) => {
              const res = await agent.updateRoutine({ id: r.id, draft });
              refresh(res.routine);
              void qc.invalidateQueries({ queryKey: ["agent", "routine-revisions", r.id] });
              setEditing(false);
              toast("Routine saved.", "ok");
            }}
          />
        ) : null}

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

/** DEFAULT_ASSISTANT stands for "no personal assistant" in the picker, which cannot hold "". */
const DEFAULT_ASSISTANT = "podium";

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

const SCOPES: { scope: Scope; label: string; hint: string }[] = [
  {
    scope: "personal",
    label: "Me",
    hint: "Runs as you: your model key, your connections, your GitHub account. Only you see it.",
  },
  {
    scope: "system",
    label: "Podium (system)",
    hint: "Runs as nobody, with the company’s credentials. Reads GitHub as the Podium App, only in the repositories you list. Admins see it; nobody writes in its chat.",
  },
];

/**
 * RoutineDialog is the one editor, for a new routine and for an existing one. The server checks the schedule — its cron, its timezone and the
 * floor — and its refusal is shown as it says it.
 *
 * An admin making a routine chooses who it runs as: themselves, or Podium (a system routine).
 * The choice is made once; editing shows it and does not offer it again.
 */
export function RoutineDialog({
  open,
  title,
  description,
  initial,
  lockAssistant,
  scope: fixedScope,
  canChooseScope,
  onClose,
  onSave,
}: {
  open: boolean;
  title: string;
  description?: string;
  initial: Partial<RoutineDraft>;
  lockAssistant?: boolean;
  /** scope is the routine's, for one that exists. */
  scope?: Scope;
  /** canChooseScope offers "Runs as": an admin making a new routine. */
  canChooseScope?: boolean;
  onClose: () => void;
  onSave: (draft: RoutineDraft, scope: Scope) => Promise<void>;
}) {
  const [scope, setScope] = useState<Scope>(fixedScope ?? "personal");
  const system = scope === "system";
  const [name, setName] = useState(initial.name ?? "");
  const [instruction, setInstruction] = useState(initial.instruction ?? "");
  const [notes, setNotes] = useState(initial.notes ?? "");
  const [schedule, setSchedule] = useState(
    initial.schedule || toCron(DEFAULT_REPEAT),
  );
  // Remounted by a template, so the schedule editor reads the schedule it was given afresh.
  const [scheduleKey, setScheduleKey] = useState(0);
  const [timezone, setTimezone] = useState(initial.timezone || browserZone());
  const [personalityId, setPersonalityId] = useState(
    initial.personalityId ?? "",
  );
  const [repos, setRepos] = useState<string[]>(initial.repos ?? []);
  const [cap, setCap] = useState(
    initial.dailyCapUsd ? String(initial.dailyCapUsd) : "",
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const ids = { assistant: useId(), zone: useId(), cap: useId(), repos: useId(), scope: useId() };
  // The assistant is chosen when the routine is made: its chat is labelled with it for good.
  const voices = useQuery({
    queryKey: ["agent", "personalities"],
    queryFn: () => agent.listPersonalities({}),
    staleTime: 30_000,
    enabled: open && !lockAssistant && !system,
  });
  const assistants = voices.data?.personalities ?? [];
  const capValue = cap.trim() === "" ? 0 : Number(cap);
  const capProblem =
    cap.trim() !== "" && (!Number.isFinite(capValue) || capValue < 0)
      ? "A spend cap is an amount in dollars, like 5 or 2.50."
      : "";

  const useTemplate = () => {
    setName(CI_FIXER.name);
    setInstruction(CI_FIXER.instruction);
    setNotes(CI_FIXER.notes);
    setSchedule(CI_FIXER.schedule);
    setScheduleKey((k) => k + 1);
  };

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await onSave(
        {
          $typeName: "podium.agent.v1.RoutineDraft",
          name: name.trim(),
          instruction: instruction.trim(),
          notes: notes.trim(),
          schedule: schedule.trim(),
          timezone: timezone.trim() || "UTC",
          personalityId: system
            ? ""
            : lockAssistant
              ? (initial.personalityId ?? "")
              : personalityId,
          model: initial.model ?? "",
          effort: initial.effort ?? "",
          repos: system ? repos : [],
          dailyCapUsd: capValue,
        },
        scope,
      );
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const scopeHint = SCOPES.find((s) => s.scope === scope)?.hint ?? "";
  const blank = !name.trim() && !instruction.trim();

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {title}
            {fixedScope === "system" ? <SystemBadge /> : null}
          </DialogTitle>
          <DialogDescription>
            {description ??
              (fixedScope === "system"
                ? "Runs as Podium with the company’s credentials. Nobody writes in its chat: what it does is steered from here."
                : "A job an assistant does on a schedule, in a chat of its own. A run that finds nothing stays quiet; one that does answers in that chat.")}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          {canChooseScope && !fixedScope ? (
            <div className="grid gap-1.5" role="group" aria-labelledby={ids.scope}>
              <span id={ids.scope} className="text-sm font-medium">
                Runs as
              </span>
              <ToggleGroup
                type="single"
                value={scope}
                aria-label="Runs as"
                onValueChange={(v) => v && setScope(v as Scope)}
                className="grid w-full grid-cols-2 border-border bg-raised"
              >
                {SCOPES.map((s) => (
                  <ToggleGroupItem
                    key={s.scope}
                    value={s.scope}
                    data-testid={`routine-scope-${s.scope}`}
                    className="h-8 text-sm text-muted data-[state=on]:bg-panel data-[state=on]:font-semibold data-[state=on]:text-fg data-[state=on]:shadow-xs"
                  >
                    {s.scope === "system" ? <ShieldCheck aria-hidden /> : <User aria-hidden />}
                    {s.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              <p data-testid="routine-scope-hint" className="text-xs leading-relaxed text-muted">
                {scopeHint}
              </p>
            </div>
          ) : null}
          {system && !fixedScope && blank ? (
            <button
              type="button"
              data-testid="routine-template-ci"
              onClick={useTemplate}
              className="group flex items-start gap-3 rounded-lg border border-dashed border-line-strong px-3 py-2.5 text-left text-sm outline-none transition-colors hover:border-muted hover:bg-raised/60 focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <Wrench className="mt-0.5 size-4 shrink-0 text-muted group-hover:text-fg" aria-hidden />
              <span className="grid gap-0.5">
                <span className="font-medium text-fg">Start from “Fix failing CI”</span>
                <span className="text-xs text-muted">
                  Finds failed checks on open pull requests and proposes a fix on each, once per commit, giving up after
                  3 tries. It only proposes until you edit it to push.
                </span>
              </span>
            </button>
          ) : null}
          <label className="grid gap-1.5 text-sm font-medium">
            Name
            <Input
              data-testid="routine-name"
              value={name}
              maxLength={80}
              onChange={(e) => setName(e.target.value)}
              placeholder={system ? "Fix failing CI" : "Morning PR digest"}
            />
          </label>
          <label className="grid gap-1.5 text-sm font-medium">
            What to do each run
            <Textarea
              data-testid="routine-instruction"
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder={
                system
                  ? "Look for failed checks on open pull requests in these repositories and comment on each with the fix you would make. Do not push."
                  : "Check acme/api for pull requests whose CI failed since the last run, and tell me which ones and why."
              }
              className="min-h-32"
            />
          </label>
          {system ? (
            <RepoField id={ids.repos} value={repos} onChange={setRepos} />
          ) : null}
          {!system && !lockAssistant && assistants.length > 0 ? (
            <div className="grid gap-1.5">
              <label htmlFor={ids.assistant} className="text-sm font-medium">
                Assistant
              </label>
              <Select
                value={personalityId || DEFAULT_ASSISTANT}
                onValueChange={(v) =>
                  setPersonalityId(v === DEFAULT_ASSISTANT ? "" : v)
                }
              >
                <SelectTrigger
                  id={ids.assistant}
                  data-testid="routine-assistant"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_ASSISTANT}>Podium</SelectItem>
                  {assistants.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.displayName || p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}
          <ScheduleField key={scheduleKey} value={schedule} onChange={setSchedule} system={system} />
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_9rem]">
            <div className="grid min-w-0 gap-1.5">
              <label htmlFor={ids.zone} className="text-sm font-medium">
                Timezone
              </label>
              <TimezonePicker
                id={ids.zone}
                data-testid="routine-timezone"
                value={timezone}
                onChange={setTimezone}
              />
            </div>
            <div className="grid gap-1.5">
              <label htmlFor={ids.cap} className="text-sm font-medium">
                Daily spend cap
              </label>
              <div className="relative">
                <span className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm text-faint">$</span>
                <Input
                  id={ids.cap}
                  data-testid="routine-cap"
                  inputMode="decimal"
                  value={cap}
                  onChange={(e) => setCap(e.target.value)}
                  placeholder="No cap"
                  aria-invalid={capProblem !== ""}
                  aria-describedby={`${ids.cap}-hint`}
                  className="pl-6 tabular-nums"
                />
              </div>
            </div>
            <p id={`${ids.cap}-hint`} className={cn("-mt-2 text-xs sm:col-span-2", capProblem ? "text-warn" : "text-muted")}>
              {capProblem ||
                "Reaching the cap pauses the routine until midnight in its timezone, and it starts again on its own."}
            </p>
          </div>
          <label className="grid gap-1.5 text-sm font-medium">
            <span>
              Standing notes{" "}
              <span className="font-normal text-muted">(optional)</span>
            </span>
            <Textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="- skip draft pull requests"
              className="min-h-20"
            />
          </label>
          {error ? (
            <p
              role="alert"
              data-testid="routine-error"
              className="text-sm text-err"
            >
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="routine-save"
              disabled={saving || !name.trim() || !instruction.trim() || capProblem !== ""}
            >
              {saving ? "Saving…" : system && !fixedScope ? "Save system routine" : "Save routine"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * RepoField is a system routine's repositories, owner/name each, as chips. What it reads on
 * GitHub is these and nothing else, so the list is the routine's reach.
 */
function RepoField({ id, value, onChange }: { id: string; value: string[]; onChange: (repos: string[]) => void }) {
  const [text, setText] = useState("");
  const [problem, setProblem] = useState("");
  const add = () => {
    const parts = text.split(/[\s,]+/).filter(Boolean);
    if (parts.length === 0) return;
    const next = [...value];
    for (const p of parts) {
      const repo = parseRepo(p);
      if (!repo) {
        setProblem(`“${p}” is not a repository. Write it as owner/name, like acme/api.`);
        return;
      }
      if (!next.some((r) => r.toLowerCase() === repo.toLowerCase())) next.push(repo);
    }
    onChange(next);
    setText("");
    setProblem("");
  };
  return (
    <div className="grid gap-1.5">
      <label htmlFor={id} className="text-sm font-medium">
        Repositories
      </label>
      <div
        className={cn(
          "flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border border-line-strong bg-panel px-2 py-1.5",
          "focus-within:border-accent focus-within:ring-2 focus-within:ring-ring/35",
        )}
      >
        {value.map((r) => (
          <span
            key={r}
            data-testid="routine-repo"
            className="inline-flex items-center gap-1 rounded-sm bg-raised py-0.5 pr-0.5 pl-2 font-mono text-xs text-fg"
          >
            {r}
            <button
              type="button"
              aria-label={`Remove ${r}`}
              onClick={() => onChange(value.filter((x) => x !== r))}
              className="grid size-4 place-items-center rounded-xs text-muted outline-none hover:bg-line-strong hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <X className="size-3" aria-hidden />
            </button>
          </span>
        ))}
        <input
          id={id}
          data-testid="routine-repos-input"
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            if (problem) setProblem("");
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === "," || e.key === "Tab") {
              if (text.trim()) {
                e.preventDefault();
                add();
              }
            } else if (e.key === "Backspace" && text === "" && value.length > 0) {
              onChange(value.slice(0, -1));
            }
          }}
          onBlur={() => text.trim() && add()}
          placeholder={value.length ? "Add another" : "owner/name, e.g. acme/api"}
          autoComplete="off"
          spellCheck={false}
          className="min-w-[10rem] flex-1 bg-transparent px-1 py-0.5 font-mono text-sm text-fg outline-none placeholder:font-sans placeholder:text-faint"
        />
      </div>
      <p className={cn("text-xs", problem ? "text-warn" : "text-muted")} role={problem ? "alert" : undefined}>
        {problem ||
          (value.length === 0
            ? "It reads GitHub only here, as the Podium App, read-only, and may comment on their pull requests. With none, it reads nothing on GitHub."
            : "It reads GitHub only here, as the Podium App, read-only, and may comment on their pull requests.")}
      </p>
    </div>
  );
}
