import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";

import { Badge } from "../components/Badge";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Checkbox } from "../components/ui/checkbox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import type { Session } from "../gen/podium/agent/v1/agent_pb";
import type {
  ListSandboxSnapshotsResponse,
  SandboxSnapshot,
  SandboxSnapshotRetention,
} from "../gen/podium/v1/snapshot_pb";
import { errorMessage, snapshots } from "../lib/client";
import { absolute, conversationLabel, relative } from "../lib/format";
import { DAY_SECONDS, sandboxSweepPlan, type SandboxPolicy } from "../lib/snapshots";
import { headCell, MobileLabel } from "./snapshotsShared";

export const SANDBOX_SNAPSHOTS_KEY = ["snapshots", "sandbox"] as const;

/**
 * One sandbox provider's tab on the Snapshots screen: the conversation workspaces it keeps as
 * images of its own (a Modal directory snapshot), which never pass through Podium, and the
 * policy that deletes old ones.
 */
export function SandboxSnapshotsPanel({
  provider,
  label,
  data,
  pending,
  error,
  sessions,
  manage,
}: {
  provider: string;
  label: string;
  data?: ListSandboxSnapshotsResponse;
  pending: boolean;
  error: unknown;
  sessions: Map<string, Session>;
  manage: boolean;
}) {
  const rows = useMemo(
    () => (data?.snapshots ?? []).filter((s) => s.provider === provider),
    [data, provider],
  );
  const configured = data?.providers.includes(provider) ?? true;
  const saved = data?.retention;
  const [draft, setDraft] = useState<Draft>();
  const form = draft ?? draftFrom(saved);
  const policy = policyFrom(form);
  const doomed = sandboxSweepPlan(rows, policy);
  const dirty = draft !== undefined && !samePolicy(policy, policyFrom(draftFrom(saved)));

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState<SandboxSnapshot[]>();
  // A row that went away (deleted, swept) is no longer selected.
  const live = useMemo(() => new Set(rows.map((r) => r.sessionId)), [rows]);
  const picked = [...selected].filter((id) => live.has(id));
  const allPicked = rows.length > 0 && picked.length === rows.length;

  const toggle = (id: string, on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  const showTable = !pending && !error;

  return (
    <div>
      {!configured ? (
        <Alert variant="warn" title={`${label} is not configured as a sandbox provider`} className="mx-6 mt-6">
          Nothing new is kept here until it is. These snapshots are what it kept before, and are
          deleted from {label} with the rules below.
        </Alert>
      ) : null}

      <SandboxRetentionCard
        label={label}
        form={form}
        saved={saved}
        dirty={dirty}
        manage={manage}
        doomed={doomed}
        onChange={setDraft}
        onSaved={() => setDraft(undefined)}
      />

      <section
        className="min-w-0 overflow-hidden border-t border-border"
        aria-label={`${label} snapshots`}
      >
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border bg-raised px-6 py-4">
          <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
            <h2 className="text-base font-bold">Conversations</h2>
            {showTable ? (
              <p className="m-0 text-sm text-soft tabular" data-testid="sandbox-snapshot-summary">
                {rows.length} {rows.length === 1 ? "snapshot" : "snapshots"}, kept by {label}
              </p>
            ) : null}
          </div>
          {manage && picked.length > 0 ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setDeleting(rows.filter((r) => picked.includes(r.sessionId)))}
            >
              Delete {picked.length} selected
            </Button>
          ) : null}
        </div>

        <div aria-live="polite">
          {pending ? <TableSkeleton cols={6} /> : null}
          {showTable && rows.length === 0 ? <SandboxSnapshotsEmpty label={label} /> : null}
          {showTable && rows.length > 0 ? (
            <Table
              containerClassName="rounded-none border-0 bg-transparent max-[600px]:overflow-visible"
              className="min-w-[760px] max-[600px]:block max-[600px]:min-w-0"
            >
              <caption className="sr-only">{label} snapshots</caption>
              <TableHeader className="max-[600px]:hidden">
                <TableRow className="hover:bg-transparent">
                  {manage ? (
                    <TableHead className={`${headCell} w-10`}>
                      <Checkbox
                        aria-label="Select every snapshot"
                        checked={allPicked ? true : picked.length > 0 ? "indeterminate" : false}
                        onCheckedChange={(v) =>
                          setSelected(v === true ? new Set(rows.map((r) => r.sessionId)) : new Set())
                        }
                      />
                    </TableHead>
                  ) : null}
                  <TableHead className={headCell}>Conversation</TableHead>
                  <TableHead className={headCell}>Image</TableHead>
                  <TableHead className={headCell}>Updated</TableHead>
                  <TableHead className={headCell}>Task</TableHead>
                  <TableHead className={headCell}>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="max-[600px]:block [&_tr:last-child_td]:pb-2">
                {rows.map((s) => (
                  <SandboxSnapshotRow
                    key={s.sessionId}
                    snapshot={s}
                    session={sessions.get(s.sessionId)}
                    doomed={doomed.has(s.sessionId)}
                    manage={manage}
                    selected={selected.has(s.sessionId)}
                    onSelect={(on) => toggle(s.sessionId, on)}
                    onDelete={() => setDeleting([s])}
                  />
                ))}
              </TableBody>
            </Table>
          ) : null}
        </div>
      </section>

      {deleting ? (
        <DeleteSandboxSnapshotsDialog
          label={label}
          snapshots={deleting}
          onDone={(gone) => {
            setSelected((prev) => new Set([...prev].filter((id) => !gone.includes(id))));
            setDeleting(undefined);
          }}
          onCancel={() => setDeleting(undefined)}
        />
      ) : null}
    </div>
  );
}

/** The form's own state: whole days and a count as typed, empty meaning off. */
type Draft = { days: string; count: string };

function draftFrom(r?: SandboxSnapshotRetention): Draft {
  const age = Number(r?.maxAgeSeconds ?? 0n);
  const count = Number(r?.maxCount ?? 0n);
  return {
    days: age > 0 ? String(Math.round((age / DAY_SECONDS) * 100) / 100) : "",
    count: count > 0 ? String(count) : "",
  };
}

function policyFrom(d: Draft): SandboxPolicy {
  const days = Number(d.days.trim());
  const count = Number(d.count.trim());
  return {
    maxAgeSeconds: Number.isFinite(days) && days > 0 ? Math.round(days * DAY_SECONDS) : 0,
    maxCount: Number.isInteger(count) && count > 0 ? count : 0,
  };
}

function samePolicy(a: SandboxPolicy, b: SandboxPolicy): boolean {
  return a.maxAgeSeconds === b.maxAgeSeconds && a.maxCount === b.maxCount;
}

function badDays(v: string): boolean {
  const t = v.trim();
  return t !== "" && !(Number(t) > 0);
}

function badCount(v: string): boolean {
  const t = v.trim();
  return t !== "" && !(Number.isInteger(Number(t)) && Number(t) > 0);
}

function SandboxRetentionCard({
  label,
  form,
  saved,
  dirty,
  manage,
  doomed,
  onChange,
  onSaved,
}: {
  label: string;
  form: Draft;
  saved?: SandboxSnapshotRetention;
  dirty: boolean;
  manage: boolean;
  doomed: Set<string>;
  onChange: (d: Draft) => void;
  onSaved: () => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const policy = policyFrom(form);
  const bad = badDays(form.days) || badCount(form.count);

  const save = useMutation({
    mutationFn: () =>
      snapshots.setSandboxSnapshotRetention({
        maxAgeSeconds: BigInt(policy.maxAgeSeconds),
        maxCount: BigInt(policy.maxCount),
      }),
    onSuccess: (res) => {
      toast(
        res.removed > 0
          ? `Retention saved. Deleted ${res.removed} ${res.removed === 1 ? "snapshot" : "snapshots"} from ${label}.`
          : "Retention saved.",
        "ok",
      );
      onSaved();
      void qc.invalidateQueries({ queryKey: SANDBOX_SNAPSHOTS_KEY });
    },
    onError: (err) => toast(`SetSandboxSnapshotRetention: ${errorMessage(err)}`),
  });

  return (
    <section className="min-w-0" aria-label={`${label} retention`}>
      <form
        className="grid gap-5 p-6"
        onSubmit={(e) => {
          e.preventDefault();
          if (manage && dirty && !bad) save.mutate();
        }}
      >
        <div className="space-y-1">
          <h2 className="text-base font-bold">Retention</h2>
          <p className="m-0 text-soft">
            Podium checks these rules every 10 minutes and deletes the oldest snapshots from{" "}
            {label} first. It never deletes one whose conversation has a task still running. Leave a
            field empty to turn that rule off.
          </p>
          <p className="m-0 text-sm text-muted">
            {label} also drops each snapshot a day after the age limit on its own, in case Podium is
            down when it is due. A new limit applies to that fallback only for snapshots taken from
            then on.
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="sandbox-retention-days" className="text-sm font-bold text-fg">
              Delete after this many days unused
            </Label>
            <Input
              id="sandbox-retention-days"
              inputMode="decimal"
              placeholder="Keep forever"
              value={form.days}
              disabled={!manage}
              aria-invalid={badDays(form.days)}
              onChange={(e) => onChange({ ...form, days: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="sandbox-retention-count" className="text-sm font-bold text-fg">
              Keep at most this many
            </Label>
            <Input
              id="sandbox-retention-count"
              inputMode="numeric"
              placeholder="No limit"
              value={form.count}
              disabled={!manage}
              aria-invalid={badCount(form.count)}
              onChange={(e) => onChange({ ...form, count: e.target.value })}
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="m-0 text-sm text-soft" data-testid="sandbox-retention-preview">
            {bad
              ? "Enter a number greater than zero, a whole one for the count, or leave the field empty."
              : doomed.size === 0
                ? policy.maxAgeSeconds === 0 && policy.maxCount === 0
                  ? "Nothing is deleted automatically."
                  : "Every current snapshot is within these limits."
                : `${dirty ? "Saving deletes" : "The next check deletes"} ${doomed.size} ${doomed.size === 1 ? "snapshot" : "snapshots"}.`}
            {saved?.updatedAt && saved.updatedBy ? (
              <span className="text-muted" title={absolute(saved.updatedAt)}>
                {" "}
                Last changed {relative(saved.updatedAt)} by {saved.updatedBy}.
              </span>
            ) : null}
          </p>
          {manage ? (
            <div className="flex gap-2">
              {dirty ? (
                <Button type="button" variant="outline" onClick={onSaved} disabled={save.isPending}>
                  Discard
                </Button>
              ) : null}
              <Button type="submit" disabled={!dirty || bad || save.isPending}>
                {save.isPending ? "Saving…" : "Save retention"}
              </Button>
            </div>
          ) : null}
        </div>
      </form>
    </section>
  );
}

function SandboxSnapshotRow({
  snapshot: s,
  session,
  doomed,
  manage,
  selected,
  onSelect,
  onDelete,
}: {
  snapshot: SandboxSnapshot;
  session?: Session;
  doomed: boolean;
  manage: boolean;
  selected: boolean;
  onSelect: (on: boolean) => void;
  onDelete: () => void;
}) {
  const cell = "px-4 py-4 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2";
  return (
    <TableRow
      data-state={selected ? "selected" : undefined}
      className="max-[600px]:grid max-[600px]:grid-cols-2 max-[600px]:border-t max-[600px]:border-border max-[600px]:px-4 max-[600px]:py-3"
    >
      {manage ? (
        <TableCell className={`${cell} w-10 max-[600px]:col-span-2`}>
          <Checkbox
            aria-label={`Select snapshot ${s.sessionId}`}
            checked={selected}
            onCheckedChange={(v) => onSelect(v === true)}
          />
        </TableCell>
      ) : null}
      <TableCell className={`${cell} max-[600px]:col-span-2`}>
        <MobileLabel>Conversation</MobileLabel>
        <div className="flex min-w-0 flex-col gap-1">
          {session ? (
            <span className="truncate text-sm text-fg" title={session.sourceKey}>
              <span className="text-muted">{session.sourceKind}</span> {conversationLabel(session)}
            </span>
          ) : null}
          <span className="truncate font-mono text-xs text-soft" title={s.sessionId}>
            {s.sessionId}
          </span>
          {s.inUse || doomed ? (
            <div className="flex gap-1.5">
              {s.inUse ? <Badge tone="run">in use</Badge> : null}
              {doomed ? <Badge tone="warn">past retention</Badge> : null}
            </div>
          ) : null}
        </div>
      </TableCell>
      <TableCell className={`${cell} max-[600px]:col-span-2`}>
        <MobileLabel>Image</MobileLabel>
        <span className="font-mono text-xs break-all text-fg">{s.imageId}</span>
      </TableCell>
      <TableCell className={`${cell} text-sm whitespace-nowrap text-muted`} title={absolute(s.updatedAt)}>
        <MobileLabel>Updated</MobileLabel>
        {relative(s.updatedAt)}
      </TableCell>
      <TableCell className={cell}>
        <MobileLabel>Task</MobileLabel>
        {s.taskId ? (
          <Link to={`/tasks/${s.taskId}`} className="font-mono text-xs underline underline-offset-2">
            {s.taskId}
          </Link>
        ) : (
          <span className="text-muted">-</span>
        )}
      </TableCell>
      <TableCell className={`${cell} max-[600px]:col-span-2`}>
        {manage ? (
          <div className="flex justify-end max-[600px]:justify-start">
            <Button
              variant="ghost"
              aria-label={`Delete snapshot ${s.sessionId}`}
              onClick={onDelete}
              className="text-err hover:bg-err-bg hover:text-err"
            >
              Delete
            </Button>
          </div>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

function DeleteSandboxSnapshotsDialog({
  label,
  snapshots: doomed,
  onDone,
  onCancel,
}: {
  label: string;
  snapshots: SandboxSnapshot[];
  onDone: (gone: string[]) => void;
  onCancel: () => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const one = doomed.length === 1;
  const busy = doomed.filter((s) => s.inUse).length;
  const remove = useMutation({
    mutationFn: () => snapshots.deleteSandboxSnapshots({ sessionIds: doomed.map((s) => s.sessionId) }),
    onSuccess: (res) => {
      const failed = Object.keys(res.failed);
      if (failed.length > 0) {
        toast(
          `Deleted ${res.removed} of ${doomed.length}. Not deleted: ${failed.map((id) => `${id} (${res.failed[id]})`).join(", ")}`,
        );
      } else {
        toast(one ? `Snapshot deleted from ${label}.` : `${res.removed} snapshots deleted from ${label}.`, "ok");
      }
      void qc.invalidateQueries({ queryKey: SANDBOX_SNAPSHOTS_KEY });
      onDone(doomed.map((s) => s.sessionId).filter((id) => !failed.includes(id)));
    },
    onError: (err) => toast(`DeleteSandboxSnapshots: ${errorMessage(err)}`),
  });
  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="max-w-[520px]">
        <DialogHeader>
          <DialogTitle>{one ? "Delete this snapshot?" : `Delete ${doomed.length} snapshots?`}</DialogTitle>
          <DialogDescription>
            {one ? "The conversation keeps" : "The conversations keep"} {one ? "its" : "their"} history,
            but {one ? "its" : "each one's"} next task starts from an empty workspace, so anything{" "}
            {one ? "it" : "they"} cloned, installed or built has to be done again. The{" "}
            {one ? "image is" : "images are"} deleted from {label}. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        {busy > 0 ? (
          <Alert
            variant="warn"
            title={one ? "A task of this conversation is still running" : `${busy} of these have a task still running`}
          >
            A running task keeps the files it already has, and stores a new snapshot when it exits.
          </Alert>
        ) : null}
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
          <Button variant="destructive" onClick={() => remove.mutate()} disabled={remove.isPending}>
            {remove.isPending ? "Deleting…" : one ? "Delete snapshot" : `Delete ${doomed.length} snapshots`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SandboxSnapshotsEmpty({ label }: { label: string }) {
  return (
    <div className="grid min-h-60 content-center justify-items-center gap-3 px-6 py-12 text-center">
      <span
        aria-hidden
        className="grid size-12 place-items-center rounded-md border border-line-strong bg-raised font-mono text-lg font-bold text-soft"
      >
        S
      </span>
      <h3 className="text-lg font-bold">No {label} snapshots yet</h3>
      <p className="m-0 max-w-[58ch] text-soft">
        A conversation whose tasks run on {label} keeps its workspace here, as an image {label}{" "}
        holds, when its first task exits. Its files never pass through Podium.
      </p>
    </div>
  );
}
