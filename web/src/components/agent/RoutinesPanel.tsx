import { useState } from "react";
import { AlertTriangle, MessageSquare, MoreHorizontal, Pause, Pencil, Play, Plus, Repeat, Trash2, Zap } from "lucide-react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Routine } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";
import { draftOf, isSystem, money, routineChatPath, routinesKey, upcoming } from "../../lib/routines";
import { Badge } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { ConductorDown } from "./ConductorDown";
import { RoutineDialog, SystemBadge } from "./RoutineCard";

/**
 * RoutinesPanel is the viewer's routines: what each does, when it runs next, and the controls.
 * A routine is made here, or by asking an assistant for one in a chat; either way it gets a
 * chat of its own, which is where its answers land.
 *
 * An admin also sees the system routines here, in the same list, each marked System and
 * "runs as Podium", and may make one: "Runs as" in the new-routine dialog.
 */
export function RoutinesPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const viewer = useViewer();
  const viewerLogin = viewer?.login ?? "";
  const admin = canManageInfra(viewer);
  const list = useQuery({
    queryKey: routinesKey,
    queryFn: () => agent.listRoutines({}),
    // The next run and whether one is going move on their own.
    refetchInterval: 30_000,
  });
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Routine | null>(null);
  const [deleting, setDeleting] = useState<Routine | null>(null);

  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: routinesKey });
    void qc.invalidateQueries({ queryKey: ["agent", "routine"] });
    void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
  };

  const act = useMutation({
    mutationFn: async ({ r, what }: { r: Routine; what: "pause" | "resume" | "run" }) => {
      if (what === "pause") await agent.pauseRoutine({ id: r.id });
      else if (what === "resume") await agent.resumeRoutine({ id: r.id });
      else await agent.runRoutineNow({ id: r.id });
    },
    onSuccess: async (_res, { r, what }) => {
      await refresh();
      if (what === "run") toast(`${r.name} is running. Its answer appears in its chat if it finds something.`, "ok");
      if (what === "pause" && isSystem(r)) toast(`${r.name} is paused for everyone until an admin resumes it.`, "ok");
    },
    onError: (err) => toast(errorMessage(err)),
  });
  const remove = useMutation({
    mutationFn: (r: Routine) => agent.deleteRoutine({ id: r.id }),
    onSuccess: async (_res, r) => {
      setDeleting(null);
      await refresh();
      toast(`${r.name} deleted. Its chat stays.`, "ok");
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const routines = list.data?.routines ?? [];
  const unreachable = list.isError && isAgentUnreachable(list.error);

  return (
    <PageFrame
      title="Routines"
      lead={
        admin
          ? "Jobs an assistant does on a schedule, each in a chat of its own. Yours run as you; system routines run as Podium and are shared by every admin."
          : "Jobs an assistant does on a schedule, each in a chat of its own. A run that finds nothing stays quiet; one that does answers in that chat."
      }
      actions={
        <Button size="sm" data-testid="routines-new" onClick={() => setCreating(true)}>
          <Plus />
          New routine
        </Button>
      }
    >
      {unreachable ? (
        <ConductorDown what="Routines could not be read" onRetry={() => void list.refetch()} retrying={list.isFetching} />
      ) : null}
      {list.isError && !unreachable ? (
        <Empty icon={Repeat} title="Could not read routines" hint={errorMessage(list.error)} />
      ) : null}

      {list.isPending ? (
        <div className="space-y-px overflow-hidden rounded-lg border border-border">
          <Skeleton className="h-[76px] w-full rounded-none" />
          <Skeleton className="h-[76px] w-full rounded-none" />
        </div>
      ) : null}

      {list.isSuccess && routines.length === 0 ? (
        <Empty
          icon={Repeat}
          title="No routines yet"
          hint="Make one here, or ask an assistant in a chat: “every weekday at 9, tell me which of my pull requests failed CI”."
          action={
            <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
              <Plus />
              New routine
            </Button>
          }
        />
      ) : null}

      {routines.length > 0 ? (
        <ul className="m-0 list-none divide-y divide-border overflow-hidden rounded-lg border border-border bg-panel p-0">
          {routines.map((r) => (
            <RoutineRow
              key={r.id}
              routine={r}
              owner={r.ownerLogin && r.ownerLogin !== viewerLogin ? r.ownerLogin : ""}
              busy={act.isPending && act.variables?.r.id === r.id}
              onOpen={() => navigate(routineChatPath(r))}
              href={routineChatPath(r)}
              onRun={() => act.mutate({ r, what: "run" })}
              onToggle={() => act.mutate({ r, what: r.enabled ? "pause" : "resume" })}
              onEdit={() => setEditing(r)}
              onDelete={() => setDeleting(r)}
            />
          ))}
        </ul>
      ) : null}

      {creating ? (
        <RoutineDialog
          open
          title="New routine"
          initial={{}}
          canChooseScope={admin}
          onClose={() => setCreating(false)}
          onSave={async (draft, scope) => {
            const res = await agent.createRoutine({ draft, scope });
            setCreating(false);
            await refresh();
            toast(`${res.routine?.name ?? "Routine"} runs ${res.routine?.scheduleText ?? ""}.`, "ok");
          }}
        />
      ) : null}

      {editing ? (
        <RoutineDialog
          open
          title="Edit routine"
          initial={draftOf(editing)}
          lockAssistant
          scope={isSystem(editing) ? "system" : "personal"}
          onClose={() => setEditing(null)}
          onSave={async (draft) => {
            await agent.updateRoutine({ id: editing.id, draft });
            setEditing(null);
            await refresh();
            toast("Routine saved.", "ok");
          }}
        />
      ) : null}

      <Dialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deleting?.name}?</DialogTitle>
            <DialogDescription>
              It stops running and forgets what it kept. Its chat and everything the routine said in it stay.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>
              Keep it
            </Button>
            <Button
              variant="destructive"
              data-testid="routine-delete-confirm"
              disabled={remove.isPending}
              onClick={() => deleting && remove.mutate(deleting)}
            >
              Delete routine
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageFrame>
  );
}

function RoutineRow({
  routine: r,
  owner,
  busy,
  href,
  onOpen,
  onRun,
  onToggle,
  onEdit,
  onDelete,
}: {
  routine: Routine;
  owner: string;
  busy: boolean;
  href: string;
  onOpen: () => void;
  onRun: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const flagged = !r.enabled && r.pausedReason !== "";
  const system = isSystem(r);
  // An admin's pause says so itself; Podium's own say what Podium saw.
  const why = /^paused by/i.test(r.pausedReason) ? r.pausedReason : `Paused by Podium. ${r.pausedReason}`;
  return (
    <li
      data-testid="routine-row"
      className="grid gap-x-6 gap-y-3 px-4 py-3.5 transition-colors hover:bg-raised/60 sm:px-5 lg:grid-cols-[minmax(0,1fr)_auto_auto] lg:items-center"
    >
      <div className="min-w-0 space-y-1">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <Link
            to={href}
            className="min-w-0 truncate text-base font-semibold text-fg underline-offset-4 outline-none hover:underline focus-visible:underline focus-visible:ring-2 focus-visible:ring-ring/50 rounded-xs"
          >
            {r.name}
          </Link>
          {system ? <SystemBadge /> : null}
          {r.running ? (
            <Badge tone="run">running</Badge>
          ) : r.enabled ? (
            <Badge tone="ok">active</Badge>
          ) : (
            <Badge tone={flagged ? "warn" : "idle"}>paused</Badge>
          )}
          {owner && !system ? <span className="text-xs text-faint">{owner}</span> : null}
        </div>
        <p className="text-sm text-muted">
          Runs {r.scheduleText}
          {r.timezone && r.timezone !== "UTC" ? <span className="text-faint"> · {r.timezone}</span> : null}
          {system ? <span className="text-faint"> · as Podium</span> : null}
        </p>
        {flagged ? (
          <p className="flex items-start gap-1.5 pt-0.5 text-xs leading-relaxed text-warn">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <span>{why}</span>
          </p>
        ) : null}
      </div>

      <dl
        data-testid="routine-row-status"
        className="m-0 grid grid-cols-3 gap-x-6 text-xs tabular-nums sm:w-max lg:grid-cols-[7.5rem_6.5rem_4rem]"
      >
        <div className="min-w-0">
          <dt className="text-faint">Next run</dt>
          <dd className="m-0 truncate text-fg">
            {r.running
              ? "running now"
              : r.enabled
                ? upcoming(r.nextRunAt)
                : r.pausedUntil
                  ? upcoming(r.pausedUntil)
                  : "paused"}
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-faint">Last run</dt>
          <dd className="m-0 truncate text-fg" title={r.lastRunAt ? absolute(r.lastRunAt) : undefined}>
            {r.lastRunAt ? relative(r.lastRunAt) : "not yet"}
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-faint">24h spend</dt>
          <dd className="m-0 text-fg">{money(r.spendTodayUsd)}</dd>
        </div>
      </dl>

      <div className="flex items-center gap-1.5 lg:justify-end">
        <Button size="sm" variant="outline" disabled={busy || r.running} onClick={onRun}>
          <Zap />
          Run now
        </Button>
        {/* One width for Pause and Resume, so Run now sits in the same place on every row. */}
        <Button size="sm" variant="outline" disabled={busy} onClick={onToggle} className="w-[6.75rem]">
          {r.enabled ? <Pause /> : <Play />}
          {r.enabled ? "Pause" : "Resume"}
        </Button>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button size="icon-sm" variant="ghost" aria-label={`More for ${r.name}`}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onOpen}>
              <MessageSquare />
              Open chat
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onEdit}>
              <Pencil />
              Edit
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="danger" onSelect={onDelete}>
              <Trash2 />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </li>
  );
}

