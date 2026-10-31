import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { Badge } from "../components/Badge";
import { PageFrame } from "../components/PageHeader";
import { SandboxProviderDialog } from "../components/sandboxes/SandboxProviderDialog";
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
import type { SandboxProvider } from "../gen/podium/v1/sandbox_pb";
import { Code, connectCode, errorMessage, sandboxProviders } from "../lib/client";
import { absolute, relative } from "../lib/format";
import { useViewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";
import { SANDBOX_PROVIDERS, capacityText, type ProviderSpec } from "../lib/sandboxProviders";

/**
 * The Sandboxes screen: the cloud providers Podium runs tasks on. Setting one up here is the whole
 * of it — podium-server runs that provider's sandboxes itself, with no node to install — and a
 * key is rotated in one place.
 */
export function SandboxesPage() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const toast = useToast();
  const [editing, setEditing] = useState<ProviderSpec>();
  const [removing, setRemoving] = useState<ProviderSpec>();

  const providers = useQuery({
    queryKey: ["sandboxProviders"],
    queryFn: () => sandboxProviders.listSandboxProviders({}),
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (providers.error) toast(`ListSandboxProviders: ${errorMessage(providers.error)}`);
  }, [providers.error, toast]);

  const byName = useMemo(() => {
    const m = new Map<string, SandboxProvider>();
    for (const p of providers.data?.providers ?? []) m.set(p.name, p);
    return m;
  }, [providers.data]);

  const noMasterKey = connectCode(providers.error) === Code.FailedPrecondition;

  return (
    <PageFrame
      title="Sandboxes"
      bodyClassName="space-y-5 p-6"
      lead="Cloud services that can run each task on a fresh machine of its own."
    >
      {noMasterKey ? (
        <Alert variant="warn" title="This server has no master key configured">
          There is nothing to encrypt a key with, so it cannot store sandbox providers. Set{" "}
          <span className="font-mono">PODIUM_MASTER_KEY_FILE</span> and restart podium-server.
        </Alert>
      ) : null}

      <section className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel" aria-label="Sandbox providers">

        <ul className="m-0 list-none divide-y divide-border p-0">
          {SANDBOX_PROVIDERS.map((spec) => (
            <ProviderRow
              key={spec.name}
              spec={spec}
              current={byName.get(spec.name)}
              loading={providers.isPending}
              manage={manage && !noMasterKey}
              onEdit={() => setEditing(spec)}
              onRemove={() => setRemoving(spec)}
            />
          ))}
        </ul>
      </section>

      {editing ? (
        <SandboxProviderDialog
          spec={editing}
          current={byName.get(editing.name)}
          open
          onOpenChange={(open) => !open && setEditing(undefined)}
        />
      ) : null}
      {removing ? (
        <RemoveProviderDialog spec={removing} open onOpenChange={(open) => !open && setRemoving(undefined)} />
      ) : null}
    </PageFrame>
  );
}

function ProviderRow({
  spec,
  current,
  loading,
  manage,
  onEdit,
  onRemove,
}: {
  spec: ProviderSpec;
  current?: SandboxProvider;
  loading: boolean;
  manage: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const configured = current?.hasSecret ?? false;
  const set = spec.fields.filter((f) => current?.settings[f.key]);

  return (
    <li className="grid gap-4 p-6 sm:grid-cols-[minmax(0,1fr)_auto]" data-testid={`provider-${spec.name}`}>
      <div className="min-w-0 space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-lg font-bold">{spec.label}</h3>
          {loading ? null : configured ? <Badge tone="ok">configured</Badge> : <Badge tone="idle">not set up</Badge>}
        </div>
        <p className="m-0 text-soft">{spec.blurb}</p>
        {configured && current ? (
          <dl className="m-0 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted">{spec.secretLabel}</dt>
            <dd className="m-0 text-fg">stored, hidden</dd>
            <dt className="text-muted">Running</dt>
            <dd className="m-0 text-fg" data-testid={`capacity-${spec.name}`}>
              {capacityText(current)}
            </dd>
            {set.map((f) => (
              <SettingRow key={f.key} label={f.label} value={current.settings[f.key]} />
            ))}
            <dt className="text-muted">Last set</dt>
            <dd className="m-0 text-fg" title={absolute(current.updatedAt)}>
              {relative(current.updatedAt)} by {current.createdBy || "-"}
            </dd>
          </dl>
        ) : null}
        <p className="m-0 text-sm text-muted">
          {configured
            ? `Podium runs ${spec.label} sandboxes itself: a task or playbook that runs on ${spec.label} gets one.`
            : `Once set up, Podium runs ${spec.label} sandboxes itself. There is nothing to install.`}
        </p>
      </div>
      {manage ? (
        <div className="flex items-start gap-2">
          <Button variant={configured ? "outline" : "default"} onClick={onEdit} aria-label={`${configured ? "Edit" : "Set up"} ${spec.label}`}>
            {configured ? "Edit" : "Set up"}
          </Button>
          {configured ? (
            <Button
              variant="ghost"
              onClick={onRemove}
              aria-label={`Remove ${spec.label}`}
              className="text-err hover:bg-err-bg hover:text-err"
            >
              Remove
            </Button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

function SettingRow({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className="m-0 truncate font-mono text-xs text-fg">{value}</dd>
    </>
  );
}

function RemoveProviderDialog({
  spec,
  open,
  onOpenChange,
}: {
  spec: ProviderSpec;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const remove = useMutation({
    mutationFn: () => sandboxProviders.deleteSandboxProvider({ name: spec.name }),
    onSuccess: () => {
      toast(`${spec.label} removed.`, "ok");
      void qc.invalidateQueries({ queryKey: ["sandboxProviders"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`DeleteSandboxProvider: ${errorMessage(err)}`),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[520px]">
        <DialogHeader>
          <DialogTitle>Remove {spec.label}?</DialogTitle>
          <DialogDescription>
            Its key is deleted and cannot be recovered. No new task starts on it; tasks already
            running keep their sandboxes until they finish.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
          <Button variant="destructive" onClick={() => remove.mutate()} disabled={remove.isPending}>
            {remove.isPending ? "Removing…" : `Remove ${spec.label}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
