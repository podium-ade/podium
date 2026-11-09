import { useState } from "react";
import { Pause } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  MemberRoutine,
  RoutineLimits,
} from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { relative } from "../../lib/format";
import { money } from "../../lib/routines";
import { cn } from "../../lib/utils";
import { Badge, type Tone } from "../Badge";
import { Empty } from "../Empty";
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
import { Label } from "../ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../ui/table";
import { ConductorDown } from "./ConductorDown";

const limitsKey = ["agent", "routine-limits"];
const memberRoutinesKey = ["agent", "member-routines"];

const head =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

/** RUN_WORDS are a run's status as an admin reads it. */
const RUN_WORDS: Record<string, { label: string; tone: Tone }> = {
  done: { label: "Did something", tone: "ok" },
  idle: { label: "Nothing to do", tone: "idle" },
  failed: { label: "Failed", tone: "err" },
  running: { label: "Running", tone: "run" },
};

/**
 * RoutinesAdminPanel is Settings → Routines, for admins and owners: how many routines each
 * person may have and what they may spend, and every person's routines as metadata — name,
 * owner, schedule, spend and how the last run went. Never the instruction, the notes or what a
 * run said. An admin may pause one; its owner is told and resumes it.
 */
export function RoutinesAdminPanel() {
  const routines = useQuery({
    queryKey: memberRoutinesKey,
    queryFn: () => agent.listMemberRoutines({}),
  });

  if (isAgentUnreachable(routines.error)) {
    return (
      <div className="p-4">
        <ConductorDown
          what="Routines could not be read"
          onRetry={() => void routines.refetch()}
          retrying={routines.isFetching}
        />
      </div>
    );
  }

  return (
    <div className="divide-y divide-border">
      <LimitsForm />
      <section aria-labelledby="member-routines-heading">
        <div className="grid gap-1 px-4 pt-4 pb-3">
          <h2
            id="member-routines-heading"
            className="text-sm font-semibold text-fg"
          >
            People&rsquo;s routines
          </h2>
          <p className="text-sm text-muted">
            You see each routine&rsquo;s name, owner, schedule and spend — never
            its instruction or what it said. Pausing one tells its owner.
          </p>
        </div>
        {routines.isError ? (
          <Empty
            title="Could not read routines"
            hint={errorMessage(routines.error)}
          />
        ) : routines.isPending ? (
          <p className="px-4 pb-4 text-sm text-muted">Loading…</p>
        ) : (routines.data?.routines ?? []).length === 0 ? (
          <p
            className="px-4 pb-4 text-sm text-muted"
            data-testid="member-routines-empty"
          >
            Nobody has a routine yet.
          </p>
        ) : (
          <MemberTable rows={routines.data?.routines ?? []} />
        )}
      </section>
    </div>
  );
}

function LimitsForm() {
  const limits = useQuery({
    queryKey: limitsKey,
    queryFn: () => agent.getRoutineLimits({}),
  });
  const loaded = limits.data?.limits;
  return (
    <section
      aria-labelledby="routine-limits-heading"
      className="grid gap-4 p-4"
    >
      <div className="grid gap-1">
        <h2
          id="routine-limits-heading"
          className="text-sm font-semibold text-fg"
        >
          Limits per person
        </h2>
        <p className="text-sm text-muted">
          They apply to each member&rsquo;s own routines. System routines have a
          daily cap of their own, set on each one.
        </p>
      </div>
      {limits.isError ? (
        <p className="text-sm text-err">{errorMessage(limits.error)}</p>
      ) : loaded ? (
        // Keyed on what was loaded, so a save that lands starts the fields from the new values.
        <LimitsFields
          key={`${loaded.maxPerPerson}:${loaded.personDailyCapUsd}`}
          loaded={loaded}
        />
      ) : (
        <p className="text-sm text-muted">Loading…</p>
      )}
    </section>
  );
}

function LimitsFields({ loaded }: { loaded: RoutineLimits }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [count, setCount] = useState(String(loaded.maxPerPerson));
  const [cap, setCap] = useState(
    String(loaded.personDailyCapUsd > 0 ? loaded.personDailyCapUsd : 0),
  );

  const save = useMutation({
    mutationFn: (
      l: Pick<RoutineLimits, "maxPerPerson" | "personDailyCapUsd">,
    ) => agent.setRoutineLimits({ limits: l }),
    onSuccess: (res) => {
      qc.setQueryData(limitsKey, { limits: res.limits });
      toast("Routine limits saved.", "ok");
    },
  });

  const n = Number(count);
  const usd = Number(cap || "0");
  const valid =
    Number.isInteger(n) &&
    n >= 1 &&
    n <= 100 &&
    Number.isFinite(usd) &&
    usd >= 0;
  const dirty = n !== loaded.maxPerPerson || usd !== loaded.personDailyCapUsd;

  return (
    <form
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) save.mutate({ maxPerPerson: n, personDailyCapUsd: usd });
      }}
    >
      <div className="grid max-w-3xl gap-4 sm:grid-cols-2">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="routine-limit-count">Routines per person</Label>
          <Input
            id="routine-limit-count"
            type="number"
            inputMode="numeric"
            min={1}
            max={100}
            step={1}
            value={count}
            onChange={(e) => setCount(e.target.value)}
            className="tabular-nums"
          />
          <p className="text-xs text-faint">
            Between 1 and 100. Nobody loses a routine they already have.
          </p>
        </div>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="routine-limit-cap">Daily spend per person</Label>
          <div className="relative">
            <span className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-sm text-muted">
              $
            </span>
            <Input
              id="routine-limit-cap"
              type="number"
              inputMode="decimal"
              min={0}
              step={0.5}
              value={cap}
              onChange={(e) => setCap(e.target.value)}
              className="pl-6 tabular-nums"
            />
          </div>
          <p className="text-xs text-faint">
            What one person&rsquo;s routines may spend in 24 hours, together. 0
            is no cap. Over it, their routines skip runs until the spend rolls
            off.
          </p>
        </div>
      </div>
      {save.isError ? (
        <p role="alert" className="text-sm text-err">
          {errorMessage(save.error)}
        </p>
      ) : null}
      <div>
        <Button type="submit" size="sm" disabled={!valid || !dirty || save.isPending}>
          {save.isPending ? "Saving…" : "Save limits"}
        </Button>
      </div>
    </form>
  );
}

function MemberTable({ rows }: { rows: MemberRoutine[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [pausing, setPausing] = useState<MemberRoutine | null>(null);
  const pause = useMutation({
    mutationFn: (r: MemberRoutine) => agent.pauseRoutine({ id: r.id }),
    onSuccess: (_res, r) => {
      void qc.invalidateQueries({ queryKey: memberRoutinesKey });
      toast(`${r.name} paused. ${r.ownerLogin} was told.`, "ok");
      setPausing(null);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  return (
    <>
      <Table containerClassName="rounded-none border-0 border-t border-border bg-transparent">
        <TableHeader>
          <TableRow>
            <TableHead className={cn(head, "w-[28%]")}>Routine</TableHead>
            <TableHead className={head}>Schedule</TableHead>
            <TableHead className={cn(head, "text-right")}>24h spend</TableHead>
            <TableHead className={head}>Last run</TableHead>
            <TableHead className={head}>Status</TableHead>
            <TableHead className={head}>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => {
            const last = RUN_WORDS[r.lastRunStatus];
            return (
              <TableRow key={r.id} data-testid="member-routine-row">
                <TableCell className="px-4 py-3 align-top">
                  <div className="truncate font-medium text-fg">{r.name}</div>
                  <div className="truncate text-xs text-muted">
                    {r.ownerLogin}
                  </div>
                </TableCell>
                <TableCell className="px-4 py-3 align-top text-sm">
                  <div className="text-fg">{r.scheduleText}</div>
                  <div className="text-xs text-faint">{r.timezone}</div>
                </TableCell>
                <TableCell className="px-4 py-3 text-right align-top text-sm tabular-nums">
                  {money(r.spendTodayUsd)}
                </TableCell>
                <TableCell className="px-4 py-3 align-top text-sm">
                  {last ? (
                    <div className="grid justify-items-start gap-0.5">
                      <Badge tone={last.tone}>{last.label}</Badge>
                      <span className="text-xs text-faint">
                        {relative(r.lastRunAt)}
                      </span>
                    </div>
                  ) : (
                    <span className="text-muted">Not yet</span>
                  )}
                </TableCell>
                <TableCell className="px-4 py-3 align-top text-sm">
                  {r.enabled ? (
                    <Badge tone="ok">Active</Badge>
                  ) : (
                    <div className="grid justify-items-start gap-0.5">
                      <Badge tone="warn">Paused</Badge>
                      {r.pausedReason ? (
                        <span className="max-w-56 text-xs text-muted">
                          {r.pausedReason}
                        </span>
                      ) : null}
                    </div>
                  )}
                </TableCell>
                <TableCell className="px-4 py-3 text-right align-top">
                  {r.enabled ? (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setPausing(r)}
                      aria-label={`Pause ${r.name}`}
                    >
                      <Pause aria-hidden />
                      Pause
                    </Button>
                  ) : null}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>

      <Dialog
        open={pausing !== null}
        onOpenChange={(o) => !o && setPausing(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Pause {pausing?.name}?</DialogTitle>
            <DialogDescription>
              {pausing?.ownerLogin} is told in their inbox and can resume it.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPausing(null)}>
              Keep it running
            </Button>
            <Button
              data-testid="member-routine-pause-confirm"
              disabled={pause.isPending}
              onClick={() => pausing && pause.mutate(pausing)}
            >
              Pause routine
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
