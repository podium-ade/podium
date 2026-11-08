import { useId, useState } from "react";
import { ChevronDown, Pause, Pencil, Play, Repeat, RotateCcw, Trash2, Zap } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type Routine, type RoutineDraft } from "../../gen/podium/agent/v1/agent_pb";
import { browserZone, draftOf, money, routineKey, routinesKey, routineStatus } from "../../lib/routines";
import { agent, errorMessage } from "../../lib/client";
import { relative } from "../../lib/format";
import { cn } from "../../lib/utils";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Textarea } from "../ui/textarea";
import { DEFAULT_REPEAT, toCron } from "../../lib/schedule";
import { ScheduleField } from "./ScheduleField";
import { TimezonePicker } from "./TimezonePicker";

/**
 * RoutineCard sits at the top of a routine's chat: what it does, when it runs next, what it
 * kept, and the controls. A routine is its owner's alone, so the card is only ever shown to
 * the person who can act on it.
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
    mutationFn: async (what: "pause" | "resume" | "run" | "reset" | "accept" | "dismiss") => {
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
        case "reset":
          return (await agent.resetRoutineState({ id: r.id })).routine;
        case "accept":
          return (await agent.resolveRoutineNote({ id: r.id, accept: true })).routine;
        case "dismiss":
          return (await agent.resolveRoutineNote({ id: r.id, accept: false })).routine;
      }
    },
    onSuccess: (r, what) => {
      refresh(r);
      if (what === "run") {
        toast("Running now. Its answer appears here if it finds something.", "ok");
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

  const r = routine.data;
  if (!r) return null;
  const busy = act.isPending;
  const state = prettyState(r.state);

  return (
    <section
      data-testid="routine-card"
      aria-label={`Routine ${r.name}`}
      className="mx-4 mt-3 rounded-lg border border-border bg-panel text-sm sm:mx-6"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
        <div className="min-w-[min(100%,16rem)] flex-1 space-y-0.5">
          <p className="flex min-w-0 items-start gap-2 font-semibold text-fg">
            <Repeat className="mt-0.5 size-4 shrink-0 text-muted" aria-hidden />
            <span className="min-w-0">
              Runs {r.scheduleText}
              {r.timezone && r.timezone !== "UTC" ? (
                <span className="font-normal text-faint"> · {r.timezone}</span>
              ) : null}
            </span>
          </p>
          <p data-testid="routine-status" className="pl-6 text-xs text-muted tabular-nums">
            {routineStatus(r)}
            {r.lastRunAt ? ` · last ran ${relative(r.lastRunAt)}` : ""}
            {r.idleCount > 0 ? ` · ${r.idleCount} quiet ${r.idleCount === 1 ? "run" : "runs"} since the last answer` : ""}
            {` · ${money(r.spendTodayUsd)} in 24h`}
          </p>
        </div>
        <span className="flex items-center gap-1">
          <Button size="xs" variant="outline" disabled={busy || r.running} onClick={() => act.mutate("run")}>
            <Zap aria-hidden />
            Run now
          </Button>
          {r.enabled ? (
            <Button size="xs" variant="outline" disabled={busy} onClick={() => act.mutate("pause")}>
              <Pause aria-hidden />
              Pause
            </Button>
          ) : (
            <Button size="xs" variant="outline" disabled={busy} onClick={() => act.mutate("resume")}>
              <Play aria-hidden />
              Resume
            </Button>
          )}
          <Button size="icon-xs" variant="ghost" aria-label="Edit routine" onClick={() => setEditing(true)}>
            <Pencil aria-hidden />
          </Button>
          <Button
            size="icon-xs"
            variant="ghost"
            aria-label={open ? "Hide details" : "Show details"}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
          >
            <ChevronDown aria-hidden className={cn("transition-transform", open && "rotate-180")} />
          </Button>
        </span>
      </div>

      {!r.enabled && r.pausedReason ? (
        <p data-testid="routine-paused-reason" className="border-t border-border px-3 py-2 text-xs text-warn">
          {r.pausedReason}
        </p>
      ) : null}

      {r.proposedNote ? (
        <div
          data-testid="routine-proposed-note"
          className="flex flex-wrap items-center gap-2 border-t border-border px-3 py-2 text-xs"
        >
          <span className="text-muted">Keep this for every run?</span>
          <span className="min-w-[min(100%,14rem)] flex-1 font-medium text-fg">{r.proposedNote}</span>
          <span className="flex items-center gap-1">
            <Button size="xs" disabled={busy} onClick={() => act.mutate("accept")}>
              Keep
            </Button>
            <Button size="xs" variant="ghost" disabled={busy} onClick={() => act.mutate("dismiss")}>
              Dismiss
            </Button>
          </span>
        </div>
      ) : null}

      {open ? (
        <div className="grid max-h-[min(18rem,32vh)] gap-3 overflow-y-auto border-t border-border px-3 py-3 text-xs">
          <div>
            <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">Instruction</h3>
            <p className="whitespace-pre-wrap text-fg">{r.instruction}</p>
          </div>
          {r.notes ? (
            <div>
              <h3 className="mb-1 font-semibold text-muted uppercase tracking-[0.08em]">Standing notes</h3>
              <p className="whitespace-pre-wrap text-fg">{r.notes}</p>
            </div>
          ) : null}
          <div>
            <div className="mb-1 flex items-center justify-between">
              <h3 className="font-semibold text-muted uppercase tracking-[0.08em]">What it remembers</h3>
              {state !== "{}" ? (
                <Button size="xs" variant="ghost" disabled={busy} onClick={() => act.mutate("reset")}>
                  <RotateCcw aria-hidden />
                  Forget
                </Button>
              ) : null}
            </div>
            {state === "{}" ? (
              <p className="text-muted">Nothing yet. Each run keeps the facts the next one needs.</p>
            ) : (
              <pre data-testid="routine-state" className="max-h-48 overflow-auto rounded-md bg-bg p-2 font-mono text-2xs text-soft">
                {state}
              </pre>
            )}
          </div>
          <div className="flex justify-end">
            <Button size="xs" variant="ghost" onClick={() => setConfirmDelete(true)}>
              <Trash2 aria-hidden />
              Delete routine
            </Button>
          </div>
        </div>
      ) : null}

      {editing ? (
        <RoutineDialog
          open
          title="Edit routine"
          initial={draftOf(r)}
          lockAssistant
          onClose={() => setEditing(false)}
          onSave={async (draft) => {
            const res = await agent.updateRoutine({ id: r.id, draft });
            refresh(res.routine);
            setEditing(false);
            toast("Routine saved.", "ok");
          }}
        />
      ) : null}

      <Dialog open={confirmDelete} onOpenChange={(o) => !o && setConfirmDelete(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {r.name}?</DialogTitle>
            <DialogDescription>
              It stops running and forgets what it kept. This chat and everything the routine said in it stay.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              Keep it
            </Button>
            <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
              Delete routine
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function prettyState(raw: string): string {
  try {
    const v: unknown = JSON.parse(raw || "{}");
    if (v && typeof v === "object" && Object.keys(v).length === 0) return "{}";
    return JSON.stringify(v, null, 2);
  } catch {
    return raw;
  }
}


/** DEFAULT_ASSISTANT stands for "no personal assistant" in the picker, which cannot hold "". */
const DEFAULT_ASSISTANT = "podium";

/**
 * RoutineDialog is the one editor, for a new routine and for an existing one. The server checks the schedule — its cron, its timezone and the 15-minute
 * floor — and its refusal is shown as it says it.
 */
export function RoutineDialog({
  open,
  title,
  description,
  initial,
  lockAssistant,
  onClose,
  onSave,
}: {
  open: boolean;
  title: string;
  description?: string;
  initial: Partial<RoutineDraft>;
  lockAssistant?: boolean;
  onClose: () => void;
  onSave: (draft: RoutineDraft) => Promise<void>;
}) {
  const [name, setName] = useState(initial.name ?? "");
  const [instruction, setInstruction] = useState(initial.instruction ?? "");
  const [notes, setNotes] = useState(initial.notes ?? "");
  const [schedule, setSchedule] = useState(initial.schedule || toCron(DEFAULT_REPEAT));
  const [timezone, setTimezone] = useState(initial.timezone || browserZone());
  const [personalityId, setPersonalityId] = useState(initial.personalityId ?? "");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const ids = { assistant: useId(), zone: useId() };
  // The assistant is chosen when the routine is made: its chat is labelled with it for good.
  const voices = useQuery({
    queryKey: ["agent", "personalities"],
    queryFn: () => agent.listPersonalities({}),
    staleTime: 30_000,
    enabled: open && !lockAssistant,
  });
  const assistants = voices.data?.personalities ?? [];

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await onSave({
        $typeName: "podium.agent.v1.RoutineDraft",
        name: name.trim(),
        instruction: instruction.trim(),
        notes: notes.trim(),
        schedule: schedule.trim(),
        timezone: timezone.trim() || "UTC",
        personalityId: lockAssistant ? (initial.personalityId ?? "") : personalityId,
        model: initial.model ?? "",
        effort: initial.effort ?? "",
      });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {description ??
              "A job an assistant does on a schedule, in a chat of its own. A run that finds nothing stays quiet; one that does answers in that chat."}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label className="grid gap-1.5 text-sm font-medium">
            Name
            <Input
              data-testid="routine-name"
              value={name}
              maxLength={80}
              onChange={(e) => setName(e.target.value)}
              placeholder="Morning PR digest"
            />
          </label>
          <label className="grid gap-1.5 text-sm font-medium">
            What to do each run
            <Textarea
              data-testid="routine-instruction"
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder="Check acme/api for pull requests whose CI failed since the last run, and tell me which ones and why."
              className="min-h-32"
            />
          </label>
          {!lockAssistant && assistants.length > 0 ? (
            <div className="grid gap-1.5">
              <label htmlFor={ids.assistant} className="text-sm font-medium">
                Assistant
              </label>
              <Select
                value={personalityId || DEFAULT_ASSISTANT}
                onValueChange={(v) => setPersonalityId(v === DEFAULT_ASSISTANT ? "" : v)}
              >
                <SelectTrigger id={ids.assistant} data-testid="routine-assistant">
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
          <ScheduleField value={schedule} onChange={setSchedule} />
          <div className="grid gap-1.5">
            <label htmlFor={ids.zone} className="text-sm font-medium">
              Timezone
            </label>
            <TimezonePicker id={ids.zone} data-testid="routine-timezone" value={timezone} onChange={setTimezone} />
          </div>
          <label className="grid gap-1.5 text-sm font-medium">
            <span>
              Standing notes <span className="font-normal text-muted">(optional)</span>
            </span>
            <Textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="- skip draft pull requests"
              className="min-h-20"
            />
          </label>
          {error ? (
            <p role="alert" data-testid="routine-error" className="text-sm text-err">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" data-testid="routine-save" disabled={saving || !name.trim() || !instruction.trim()}>
              {saving ? "Saving…" : "Save routine"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
