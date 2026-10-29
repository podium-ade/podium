import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";

import { Badge } from "../components/Badge";
import { PageFrame } from "../components/PageHeader";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import type { Session } from "../gen/podium/agent/v1/agent_pb";
import type { Snapshot, SnapshotRetention } from "../gen/podium/v1/snapshot_pb";
import { admin, agent, errorMessage, snapshots } from "../lib/client";
import { absolute, conversationLabel, humanBytes, relative } from "../lib/format";
import { useViewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";
import { DAY_SECONDS, GIB, sweepPlan, totalBytes, type Policy } from "../lib/snapshots";

/**
 * The Snapshots screen: the one workspace tar each conversation keeps in the object store,
 * and the policy that deletes old ones. A snapshot is not tied to the node that took it; the
 * next task of the conversation restores it on whichever node runs it.
 */
export function SnapshotsPage() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const toast = useToast();
  const [deleting, setDeleting] = useState<Snapshot>();

  const query = useQuery({
    queryKey: ["snapshots"],
    queryFn: () => snapshots.listSnapshots({}),
    placeholderData: (prev) => prev,
  });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: () => admin.listNodes({}) });
  // Best effort: a snapshot is keyed by the conductor's session id, and the conductor is
  // what knows where that conversation happened. Without one the id is shown as it is.
  const sessions = useQuery({
    queryKey: ["agent", "sessions", "snapshots"],
    queryFn: () => agent.listSessions({ page: { limit: 500 } }),
    enabled: viewer?.agentEnabled === true,
  });

  useEffect(() => {
    if (query.error) toast(`ListSnapshots: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const rows = useMemo(() => query.data?.snapshots ?? [], [query.data]);
  const nodeName = useMemo(() => {
    const m = new Map<string, string>();
    for (const n of nodes.data?.nodes ?? []) m.set(n.id, n.name);
    return m;
  }, [nodes.data]);
  const sessionByID = useMemo(() => {
    const m = new Map<string, Session>();
    for (const s of sessions.data?.sessions ?? []) m.set(s.id, s);
    return m;
  }, [sessions.data]);

  const saved = query.data?.retention;
  const [draft, setDraft] = useState<Draft>();
  const form = draft ?? draftFrom(saved);
  const policy = policyFrom(form);
  const doomed = sweepPlan(rows, policy);
  const dirty = draft !== undefined && !samePolicy(policy, policyFrom(draftFrom(saved)));

  const enabled = query.data?.enabled ?? true;
  const showTable = !query.isPending && query.error === null;
  const total = totalBytes(rows);

  return (
    <PageFrame
      title="Snapshots"
      bodyClassName="space-y-5 p-6"
      lead="The workspace each conversation picks up from. Podium keeps one per conversation in the object store and restores it on whichever node runs the next task."
    >
      {!enabled ? (
        <Alert variant="warn" title="This server has no object store configured">
          Without one nothing is stored, and every task starts from an empty workspace. Set{" "}
          <span className="font-mono">PODIUM_S3_ENDPOINT</span> and restart podium-server.
        </Alert>
      ) : null}

      {query.error ? (
        <Alert variant="destructive" title="Could not list snapshots">
          <p>{errorMessage(query.error)}</p>
          <Button
            variant="outline"
            size="xs"
            className="mt-2"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            {query.isFetching ? "Retrying…" : "Retry"}
          </Button>
        </Alert>
      ) : null}

      <RetentionCard
        form={form}
        saved={saved}
        dirty={dirty}
        manage={manage && enabled}
        doomed={doomed}
        rows={rows}
        onChange={setDraft}
        onSaved={() => setDraft(undefined)}
      />

      <section className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel" aria-label="Snapshots">
        <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 border-b border-border bg-raised px-6 py-4">
          <h2 className="text-base font-bold">Conversations</h2>
          {showTable ? (
            <p className="m-0 text-sm text-soft tabular" data-testid="snapshot-summary">
              {rows.length} {rows.length === 1 ? "snapshot" : "snapshots"}, {humanBytes(total)}
            </p>
          ) : null}
        </div>

        <div aria-live="polite">
          {query.isPending ? <TableSkeleton cols={6} /> : null}
          {showTable && rows.length === 0 ? <SnapshotsEmpty /> : null}
          {showTable && rows.length > 0 ? (
            <Table
              containerClassName="rounded-none border-0 bg-transparent max-[600px]:overflow-visible"
              className="min-w-[760px] max-[600px]:block max-[600px]:min-w-0"
            >
              <caption className="sr-only">Snapshots</caption>
              <TableHeader className="max-[600px]:hidden">
                <TableRow className="hover:bg-transparent">
                  <TableHead className={headCell}>Conversation</TableHead>
                  <TableHead className={headCell}>Size</TableHead>
                  <TableHead className={headCell}>Updated</TableHead>
                  <TableHead className={headCell}>Taken on</TableHead>
                  <TableHead className={headCell}>Task</TableHead>
                  <TableHead className={headCell}>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="max-[600px]:block [&_tr:last-child_td]:pb-2">
                {rows.map((s) => (
                  <SnapshotRow
                    key={s.sessionId}
                    snapshot={s}
                    session={sessionByID.get(s.sessionId)}
                    node={nodeName.get(s.nodeId)}
                    doomed={doomed.has(s.sessionId)}
                    manage={manage && enabled}
                    onDelete={() => setDeleting(s)}
                  />
                ))}
              </TableBody>
            </Table>
          ) : null}
        </div>
      </section>

      {deleting ? (
        <DeleteSnapshotDialog
          snapshot={deleting}
          open
          onOpenChange={(open) => !open && setDeleting(undefined)}
        />
      ) : null}
    </PageFrame>
  );
}

/** The form's own state: whole days and gigabytes as typed, empty meaning off. */
type Draft = { days: string; gib: string };

function draftFrom(r?: SnapshotRetention): Draft {
  const age = Number(r?.maxAgeSeconds ?? 0n);
  const total = Number(r?.maxTotalBytes ?? 0n);
  return {
    days: age > 0 ? String(Math.round((age / DAY_SECONDS) * 100) / 100) : "",
    gib: total > 0 ? String(Math.round((total / GIB) * 100) / 100) : "",
  };
}

function policyFrom(d: Draft): Policy {
  return {
    maxAgeSeconds: Math.max(0, Math.round(positive(d.days) * DAY_SECONDS)),
    maxTotalBytes: Math.max(0, Math.round(positive(d.gib) * GIB)),
  };
}

function positive(v: string): number {
  const n = Number(v.trim());
  return Number.isFinite(n) && n > 0 ? n : 0;
}

function samePolicy(a: Policy, b: Policy): boolean {
  return a.maxAgeSeconds === b.maxAgeSeconds && a.maxTotalBytes === b.maxTotalBytes;
}

function invalid(v: string): boolean {
  const t = v.trim();
  return t !== "" && !(Number(t) > 0);
}

function RetentionCard({
  form,
  saved,
  dirty,
  manage,
  doomed,
  rows,
  onChange,
  onSaved,
}: {
  form: Draft;
  saved?: SnapshotRetention;
  dirty: boolean;
  manage: boolean;
  doomed: Set<string>;
  rows: Snapshot[];
  onChange: (d: Draft) => void;
  onSaved: () => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const policy = policyFrom(form);
  const bad = invalid(form.days) || invalid(form.gib);
  const freed = rows.filter((r) => doomed.has(r.sessionId)).reduce((sum, r) => sum + Number(r.sizeBytes), 0);

  const save = useMutation({
    mutationFn: () =>
      snapshots.setSnapshotRetention({
        maxAgeSeconds: BigInt(policy.maxAgeSeconds),
        maxTotalBytes: BigInt(policy.maxTotalBytes),
      }),
    onSuccess: (res) => {
      toast(
        res.removed > 0
          ? `Retention saved. Deleted ${res.removed} ${res.removed === 1 ? "snapshot" : "snapshots"}, ${humanBytes(res.freedBytes)}.`
          : "Retention saved.",
        "ok",
      );
      onSaved();
      void qc.invalidateQueries({ queryKey: ["snapshots"] });
    },
    onError: (err) => toast(`SetSnapshotRetention: ${errorMessage(err)}`),
  });

  return (
    <section className="min-w-0 rounded-lg border border-border bg-panel" aria-label="Retention">
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
            Podium checks these rules every 10 minutes and deletes the oldest snapshots first. It
            never deletes a snapshot whose conversation has a task still running. Leave a field
            empty to turn that rule off.
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="retention-days" className="text-sm font-bold text-fg">
              Delete after this many days unused
            </Label>
            <Input
              id="retention-days"
              inputMode="decimal"
              placeholder="Keep forever"
              value={form.days}
              disabled={!manage}
              aria-invalid={invalid(form.days)}
              onChange={(e) => onChange({ ...form, days: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="retention-gib" className="text-sm font-bold text-fg">
              Keep the total under (GB)
            </Label>
            <Input
              id="retention-gib"
              inputMode="decimal"
              placeholder="No limit"
              value={form.gib}
              disabled={!manage}
              aria-invalid={invalid(form.gib)}
              onChange={(e) => onChange({ ...form, gib: e.target.value })}
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="m-0 text-sm text-soft" data-testid="retention-preview">
            {bad
              ? "Enter a number greater than zero, or leave the field empty."
              : doomed.size === 0
                ? policy.maxAgeSeconds === 0 && policy.maxTotalBytes === 0
                  ? "Nothing is deleted automatically."
                  : "Every current snapshot is within these limits."
                : `${dirty ? "Saving deletes" : "The next check deletes"} ${doomed.size} ${doomed.size === 1 ? "snapshot" : "snapshots"}, ${humanBytes(freed)}.`}
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

function SnapshotRow({
  snapshot: s,
  session,
  node,
  doomed,
  manage,
  onDelete,
}: {
  snapshot: Snapshot;
  session?: Session;
  node?: string;
  doomed: boolean;
  manage: boolean;
  onDelete: () => void;
}) {
  return (
    <TableRow className="max-[600px]:grid max-[600px]:grid-cols-2 max-[600px]:border-t max-[600px]:border-border max-[600px]:px-4 max-[600px]:py-3">
      <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
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
      <TableCell className="px-4 py-4 text-sm whitespace-nowrap text-fg tabular max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
        <MobileLabel>Size</MobileLabel>
        <span title={`sha256 ${s.sha256}`}>{humanBytes(s.sizeBytes)}</span>
      </TableCell>
      <TableCell
        className="px-4 py-4 text-sm whitespace-nowrap text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2"
        title={absolute(s.updatedAt)}
      >
        <MobileLabel>Updated</MobileLabel>
        {relative(s.updatedAt)}
      </TableCell>
      <TableCell className="px-4 py-4 text-sm text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
        <MobileLabel>Taken on</MobileLabel>
        {s.nodeId ? (
          <Link to={`/nodes/${s.nodeId}`} className="underline underline-offset-2" title={s.nodeId}>
            {node || s.nodeId}
          </Link>
        ) : (
          "-"
        )}
      </TableCell>
      <TableCell className="px-4 py-4 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
        <MobileLabel>Task</MobileLabel>
        {s.taskId ? (
          <Link to={`/tasks/${s.taskId}`} className="font-mono text-xs underline underline-offset-2">
            {s.taskId}
          </Link>
        ) : (
          <span className="text-muted">-</span>
        )}
      </TableCell>
      <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
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

function DeleteSnapshotDialog({
  snapshot,
  open,
  onOpenChange,
}: {
  snapshot: Snapshot;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const remove = useMutation({
    mutationFn: () => snapshots.deleteSnapshot({ sessionId: snapshot.sessionId }),
    onSuccess: () => {
      toast(`Snapshot deleted, ${humanBytes(snapshot.sizeBytes)} freed.`, "ok");
      void qc.invalidateQueries({ queryKey: ["snapshots"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`DeleteSnapshot: ${errorMessage(err)}`),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[520px]">
        <DialogHeader>
          <DialogTitle>Delete this snapshot?</DialogTitle>
          <DialogDescription>
            The conversation keeps its history, but its next task starts from an empty workspace,
            so anything it cloned, installed or built has to be done again. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        {snapshot.inUse ? (
          <Alert variant="warn" title="A task of this conversation is still running">
            It keeps the files it already has, and stores a new snapshot when it exits.
          </Alert>
        ) : null}
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
          <Button variant="destructive" onClick={() => remove.mutate()} disabled={remove.isPending}>
            {remove.isPending ? "Deleting…" : `Delete ${humanBytes(snapshot.sizeBytes)}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Design table head: 12px, weight 750, muted, uppercase, 0.08em tracking, 12×16 padding. */
const headCell =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

function SnapshotsEmpty() {
  return (
    <div className="grid min-h-60 content-center justify-items-center gap-3 px-6 py-12 text-center">
      <span
        aria-hidden
        className="grid size-12 place-items-center rounded-md border border-line-strong bg-raised font-mono text-lg font-bold text-soft"
      >
        S
      </span>
      <h3 className="text-lg font-bold">No snapshots yet</h3>
      <p className="m-0 max-w-[58ch] text-soft">
        A conversation stores one when its first task exits, on your own nodes or on sandbox
        nodes.
      </p>
      <p className="m-0 max-w-[58ch] text-sm text-muted">
        Not listed here: host turns and one-shot tasks, which keep no workspace, and snapshots
        made by a sandbox provider itself, such as a boat named snapshot or a Modal image.
      </p>
    </div>
  );
}

function MobileLabel({ children }: { children: string }) {
  return (
    <span className="mb-1 hidden text-xs font-bold tracking-[0.08em] text-muted uppercase max-[600px]:block">
      {children}
    </span>
  );
}
