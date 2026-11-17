import { useEffect, useMemo, useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Boxes, HardDrive } from "lucide-react";
import { Link, Navigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { AssistantPanel, PanelBody, Row, ScopeBar, SectionTabs } from "../components/agent/AssistantPanel";
import { Badge } from "../components/Badge";
import { PageFrame } from "../components/PageHeader";
import { SandboxProviderDialog } from "../components/sandboxes/SandboxProviderDialog";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Skeleton } from "../components/Skeleton";
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
import { agent, Code, connectCode, errorMessage, sandboxProviders } from "../lib/client";
import { absolute, relative } from "../lib/format";
import { useViewer, type Viewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";
import {
  RUNS_ON_NODES,
  SANDBOX_PROVIDERS,
  SECTIONS,
  capacityOf,
  capacityText,
  sidecarProblem,
  withUnit,
  type ProviderSpec,
} from "../lib/sandboxProviders";
import { Snapshots } from "./SnapshotsPage";
import { cn } from "@/lib/utils";

type Tab = {
  id: string;
  label: string;
  icon: LucideIcon;
  lead: string;
  show: (v: Viewer | undefined) => boolean;
  render: () => ReactNode;
};

/**
 * TABS are what used to be three Compute entries: the sandbox providers set up and the default
 * a playbook falls back to (once Backend), now one list, and the workspaces kept between turns
 * (Snapshots).
 */
const TABS: Tab[] = [
  {
    id: "providers",
    label: "Providers",
    icon: Boxes,
    lead: "Where your assistants do their work: on your own nodes, or in a cloud service that gives each task a fresh machine of its own.",
    show: () => true,
    render: () => <SandboxProviders />,
  },
  {
    id: "snapshots",
    label: "Snapshots",
    icon: HardDrive,
    lead: "The workspace each conversation picks up from. Podium keeps one per conversation and restores it wherever the next task runs.",
    show: () => true,
    render: () => <Snapshots />,
  },
];

/**
 * SandboxesPage is Compute › Sandboxes, one tab per part. Each tab is a link, so a deep link
 * lands on it; /sandboxes, or a tab that is not one, opens the first.
 */
export function SandboxesPage() {
  const viewer = useViewer();
  const id = useParams()["*"]?.split("/").filter(Boolean)[0] ?? "";
  const tabs = TABS.filter((t) => t.show(viewer));
  const tab = tabs.find((t) => t.id === id);
  if (!tab) return <Navigate to={`/sandboxes/${tabs[0].id}`} replace />;

  return (
    <PageFrame title="Sandboxes" bodyClassName="p-6">
      <AssistantPanel label={tab.label}>
        <SectionTabs
          label="Sandboxes"
          tabs={tabs.map((t) => ({ id: t.id, label: t.label, icon: t.icon }))}
          current={tab.id}
          href={(tid) => `/sandboxes/${tid}`}
        />
        <ScopeBar>{tab.lead}</ScopeBar>
        {tab.render()}
      </AssistantPanel>
    </PageFrame>
  );
}

/**
 * The Sandboxes screen: the cloud providers Podium runs tasks on. Setting one up here is the whole
 * of it — podium-server runs that provider's sandboxes itself, with no node to install — and a
 * key is rotated in one place.
 */
export function SandboxProviders() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  // The default is the conductor's: with none there are no turns to place, only tasks that
  // name their provider, so the list is just the providers.
  const turns = viewer?.agentEnabled === true;
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<ProviderSpec>();
  const [removing, setRemoving] = useState<ProviderSpec>();

  const providers = useQuery({
    queryKey: ["sandboxProviders"],
    queryFn: () => sandboxProviders.listSandboxProviders({}),
    placeholderData: (prev) => prev,
  });
  const runsOn = useQuery({ queryKey: ["agent", "runsOn"], queryFn: () => agent.getRunsOn({}), enabled: turns });
  const profile = useQuery({ queryKey: ["agent", "profile"], queryFn: () => agent.getProfile({}), enabled: turns });

  useEffect(() => {
    if (providers.error) toast(`ListSandboxProviders: ${errorMessage(providers.error)}`);
  }, [providers.error, toast]);

  const byName = useMemo(() => {
    const m = new Map<string, SandboxProvider>();
    for (const p of providers.data?.providers ?? []) m.set(p.name, p);
    return m;
  }, [providers.data]);

  const makeDefault = useMutation({
    mutationFn: (choice: string) => agent.setRunsOn({ runsOn: choice }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["agent", "runsOn"] });
      toast("Saved. Turns that start from now run there.", "ok");
    },
    onError: (err) => toast(`SetRunsOn: ${errorMessage(err)}`),
  });

  const noMasterKey = connectCode(providers.error) === Code.FailedPrecondition;
  const current = turns && runsOn.data ? runsOn.data.runsOn?.runsOn || RUNS_ON_NODES : undefined;
  const currentSettings = byName.get(current ?? "")?.settings;
  // Playbooks that fall back to the default and whose sidecars it cannot run.
  const stranded = current
    ? (profile.data?.playbooks ?? [])
        .filter((p) => !p.shadowed && !p.runsOn)
        .map((p) => ({ name: p.name, problem: sidecarProblem(current, currentSettings, p.sandbox?.modal?.runtime ?? "", p) }))
        .filter((p) => p.problem !== "")
    : [];
  const unknownDefault = current && current !== RUNS_ON_NODES && !SANDBOX_PROVIDERS.some((p) => p.name === current);
  const unsetDefault =
    current && !unknownDefault && current !== RUNS_ON_NODES && providers.data && !byName.get(current)?.hasSecret
      ? SANDBOX_PROVIDERS.find((p) => p.name === current)?.label
      : undefined;
  // What the default can be: your nodes, and every provider that is set up. A saved default
  // that is neither stays in the list, so the select still says what it is.
  const choices = [
    { id: RUNS_ON_NODES, label: "Your nodes" },
    ...SANDBOX_PROVIDERS.filter((p) => byName.get(p.name)?.hasSecret).map((p) => ({ id: p.name, label: p.label })),
  ];
  if (current && !choices.some((c) => c.id === current)) {
    choices.push({ id: current, label: SANDBOX_PROVIDERS.find((p) => p.name === current)?.label ?? current });
  }

  return (
    <div>
      {noMasterKey ? (
        <Alert variant="warn" title="This server has no master key configured" className="mx-6 mt-6">
          There is nothing to encrypt a key with, so it cannot store sandbox providers. Set{" "}
          <span className="font-mono">PODIUM_MASTER_KEY_FILE</span> and restart podium-server.
        </Alert>
      ) : null}
      {turns ? (
        <section className="min-w-0" aria-label="Default sandbox">
          <PanelBody>
            <Row
              id="default-sandbox"
              label="Default sandbox"
              hint="Where a turn runs when its playbook doesn't name one. A playbook's Runs on overrides it, for that playbook only."
            >
              {current === undefined && !runsOn.error ? (
                <Skeleton className="h-11 w-full max-w-md" />
              ) : (
                <Select
                  value={current}
                  disabled={!manage || makeDefault.isPending}
                  onValueChange={(v) => v !== current && makeDefault.mutate(v)}
                >
                  <SelectTrigger id="default-sandbox" className="w-full max-w-md" aria-label="Default sandbox">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {choices.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {runsOn.error ? (
                <Alert variant="destructive" title="Could not read the default">
                  {errorMessage(runsOn.error)}
                </Alert>
              ) : null}
              {unknownDefault ? (
                <Alert variant="warn" title={`The default is ${current}, which Podium does not know`}>
                  Turns with no Runs on of their own wait in the queue. Choose another.
                </Alert>
              ) : null}
              {unsetDefault ? (
                <Alert variant="warn" title={`The default, ${unsetDefault}, is not set up`}>
                  Turns with no Runs on of their own wait in the queue until it is. Set it up below, or choose another.
                </Alert>
              ) : null}
              {stranded.length > 0 ? (
                <Alert variant="warn" title="These playbooks' sidecars cannot run on the default">
                  {stranded.map((p) => (
                    <p key={p.name}>
                      <strong>{p.name}</strong>: {p.problem}
                    </p>
                  ))}
                  <p>Choose another default, set the runtime on each playbook, or pin them to your nodes.</p>
                </Alert>
              ) : null}
              <p className="m-0 text-sm text-muted">
                Your nodes are enrolled and drained on{" "}
                <Link to="/nodes" className="underline">
                  Nodes
                </Link>
                .
              </p>
            </Row>
          </PanelBody>
        </section>
      ) : null}

      <section
        className={turns ? "min-w-0 overflow-hidden border-t border-border" : "min-w-0 overflow-hidden"}
        aria-label="Sandbox providers"
      >
        <div className="border-b border-border bg-raised px-6 py-4">
          <h2 className="text-base font-bold">Providers</h2>
        </div>
        <ul className="m-0 list-none divide-y divide-border p-0">
          {SANDBOX_PROVIDERS.map((spec) => (
            <ProviderRow
              key={spec.name}
              spec={spec}
              current={byName.get(spec.name)}
              loading={providers.isPending}
              manage={manage && !noMasterKey}
              isDefault={current === spec.name}
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
    </div>
  );
}

/** Marks the provider that is the default. */
function DefaultBadge() {
  return (
    <Badge tone="run" dot={false}>
      Default
    </Badge>
  );
}

/**
 * One provider: who it is and what can be done with it, then — once set up — its settings as a
 * sheet to scan, in the order the setup dialog asks for them.
 */
function ProviderRow({
  spec,
  current,
  loading,
  manage,
  isDefault,
  onEdit,
  onRemove,
}: {
  spec: ProviderSpec;
  current?: SandboxProvider;
  loading: boolean;
  manage: boolean;
  isDefault: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const configured = current?.hasSecret ?? false;
  const order = new Map(SECTIONS.map((s, i) => [s.key, i]));
  const set = spec.fields
    .filter((f) => current?.settings[f.key])
    .sort((a, b) => (order.get(a.section) ?? 0) - (order.get(b.section) ?? 0));

  return (
    <li className="min-w-0" data-testid={`provider-${spec.name}`}>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-3 px-6 py-5">
        <div className="min-w-0 flex-1 basis-80 space-y-1">
          <div className="flex flex-wrap items-center gap-2.5">
            {/* One width for every name, so each row's chips start in the same place. */}
            <h3 className="m-0 text-lg leading-tight font-bold sm:min-w-24">{spec.label}</h3>
            {loading ? null : configured ? <Badge tone="ok">configured</Badge> : <Badge tone="idle">not set up</Badge>}
            {isDefault ? <DefaultBadge /> : null}
          </div>
          <p className="m-0 max-w-[70ch] text-soft">{spec.blurb}</p>
          {configured && current ? (
            <p className="m-0 flex flex-col gap-x-2 pt-1 text-sm text-muted sm:flex-row sm:flex-wrap">
              <span>
                {spec.secretLabel} <span className="text-fg">stored, hidden</span>
              </span>
              <span aria-hidden className="hidden sm:inline">·</span>
              <span title={absolute(current.updatedAt)}>
                Set {relative(current.updatedAt)} by {current.createdBy || "-"}
              </span>
            </p>
          ) : null}
        </div>
        {configured && current ? <Capacity provider={current} name={spec.name} /> : null}
        {manage ? (
          <div className="flex shrink-0 items-center gap-1">
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
            <Button
              variant={configured ? "outline" : "default"}
              onClick={onEdit}
              aria-label={`${configured ? "Edit" : "Set up"} ${spec.label}`}
              className="min-w-24"
            >
              {configured ? "Edit" : "Set up"}
            </Button>
          </div>
        ) : null}
      </div>

      {configured && current && set.length > 0 ? (
        <dl className="m-0 grid grid-cols-[repeat(auto-fill,minmax(10rem,1fr))] gap-x-6 sm:grid-cols-[repeat(auto-fill,minmax(15rem,1fr))] gap-y-4 border-t border-hairline bg-bg/60 px-6 py-4">
          {set.map((f) => {
            const value = withUnit(f, current.settings[f.key]);
            return (
              <Fact key={f.key} label={f.label}>
                {value.includes(",")
                  ? value.split(",").map((v) => (
                      <span key={v} className="block">
                        {v.trim()}
                      </span>
                    ))
                  : value}
              </Fact>
            );
          })}
        </dl>
      ) : null}
    </li>
  );
}

/**
 * How many of this provider's sandboxes are up, against the cap that binds — the operator's or
 * the account's, whichever is lower — with what is left. With no cap there is nothing to fill.
 */
function Capacity({ provider, name }: { provider: SandboxProvider; name: string }) {
  const { inUse, limit, byAccount } = capacityOf(provider);
  const share = limit > 0 ? Math.min(1, inUse / limit) : 0;
  const free = Math.max(0, limit - inUse);
  const tone = share >= 1 ? "bg-err" : share >= 0.8 ? "bg-warn" : "bg-accent";
  return (
    <div
      className="grid w-full shrink-0 content-start gap-1.5 sm:w-48 sm:border-l sm:border-hairline sm:pl-6"
      data-testid={`capacity-${name}`}
      title={`Running ${capacityText(provider)}`}
    >
      <span className="text-xs text-muted">Running</span>
      <span className="flex items-baseline gap-1.5 leading-none tabular-nums">
        <span className="text-2xl font-bold text-fg">{inUse}</span>
        {limit > 0 ? <span className="text-sm text-muted">of {limit}</span> : null}
      </span>
      {limit > 0 ? (
        <>
          <div
            role="meter"
            aria-label={`${name} sandboxes running`}
            aria-valuemin={0}
            aria-valuemax={limit}
            aria-valuenow={inUse}
            className="h-1.5 overflow-hidden rounded-full bg-raised"
          >
            <div className={cn("h-full rounded-full transition-[width] duration-500 ease-out", tone)} style={{ width: `${share * 100}%` }} />
          </div>
          <span className="text-xs text-muted">
            {free > 0 ? `${free} free` : "Full: new tasks wait"}
            {byAccount ? " · account limit" : ""}
          </span>
        </>
      ) : (
        <span className="text-xs text-muted">No cap set</span>
      )}
    </div>
  );
}

/** One of a provider's settings: its name small above, the value as stored below. */
function Fact({ label, title, children }: { label: string; title?: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="m-0 font-mono text-[0.8125rem] [overflow-wrap:anywhere] text-fg" title={title}>
        {children}
      </dd>
    </div>
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
