import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Check, Copy, Server } from "lucide-react";
import { Badge, Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { EnrollPanel } from "../components/EnrollPanel";
import { NodeActions } from "../components/NodeActions";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { Progress } from "../components/ui/progress";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tooltip } from "../components/ui/tooltip";
import type { Node } from "../gen/podium/v1/admin_pb";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import { admin, errorMessage } from "../lib/client";
import { absolute, humanBytes, nodeStateLabel, nodeStateTone, relative } from "../lib/format";
import { cn } from "../lib/utils";

const POLL_MS = 5000;

/**
 * A node's slot counts come from the live stream it holds on this server process, so they are
 * only true while it is connected. For anything else the honest answer is "we do not know",
 * not the zero the API sends.
 */
function isConnected(n: Node): boolean {
  return n.status === NodeStatus.ONLINE || n.status === NodeStatus.DRAINING;
}

function isDraining(n: Node): boolean {
  return n.draining || n.status === NodeStatus.DRAINING;
}

export function NodesPage() {
  const query = useQuery({
    queryKey: ["nodes"],
    queryFn: () => admin.listNodes({}),
    refetchInterval: POLL_MS,
    placeholderData: (prev) => prev,
  });

  const nodes = useMemo(() => query.data?.nodes ?? [], [query.data]);

  const fleet = useMemo(() => {
    const working = nodes.filter((n) => isConnected(n) && !isDraining(n));
    return {
      working: working.length,
      draining: nodes.filter(isDraining).length,
      // Anything the server cannot reach: whether it is unreachable or fully offline, it is
      // running none of your work.
      silent: nodes.filter((n) => !isConnected(n)).length,
      freeSlots: working.reduce((sum, n) => sum + n.freeSlots, 0),
      maxSlots: working.reduce((sum, n) => sum + (n.capacity?.maxTasks ?? 0), 0),
    };
  }, [nodes]);

  const silent = nodes.filter((n) => !isConnected(n));

  return (
    <div className="space-y-5">
      <PageHeader
        title="Nodes"
        description="The machines that run your tasks. Each one dials in, advertises its labels and capacity, and takes work until you drain it."
        actions={<EnrollPanel />}
      />

      {/* A fleet of nothing has no health to summarise, and four zeroes read as an outage. */}
      {query.isPending || nodes.length > 0 ? (
        <Card>
          <div className="grid grid-cols-2 sm:grid-cols-4">
            <Stat
              label="Taking work"
              value={fleet.working}
              tone={fleet.working > 0 ? "text-ok" : "text-muted"}
              hint="online and not drained"
              loading={query.isPending}
            />
            <Stat
              label="Draining"
              value={fleet.draining}
              tone={fleet.draining > 0 ? "text-warn" : "text-muted"}
              hint="finishing, then out"
              loading={query.isPending}
            />
            <Stat
              label="Not reachable"
              value={fleet.silent}
              tone={fleet.silent > 0 ? "text-warn" : "text-muted"}
              hint="no recent heartbeat"
              loading={query.isPending}
            />
            <Stat
              label="Free slots"
              value={fleet.freeSlots}
              tone={fleet.freeSlots > 0 ? "text-fg" : "text-warn"}
              hint={
                query.isPending
                  ? "on nodes taking work"
                  : `of ${fleet.maxSlots} on nodes taking work`
              }
              loading={query.isPending}
            />
          </div>
        </Card>
      ) : null}

      {query.error ? (
        <Alert variant="destructive" title="Could not list nodes">
          {errorMessage(query.error)}.{" "}
          {nodes.length > 0
            ? "The list below is the last answer this console got, and it has stopped refreshing."
            : "Check that podium-server is running, and that the token this console is using is still valid."}
        </Alert>
      ) : null}

      {silent.length > 0 ? (
        <Alert
          variant="warn"
          title={
            silent.length === 1
              ? `${silent[0].name} has not been heard from`
              : `${silent.length} nodes have not been heard from`
          }
        >
          {silent.map((n) => (
            <p key={n.id}>
              {silent.length > 1 ? <span className="font-medium">{n.name}: </span> : null}
              Last heartbeat <span className="tabular">{relative(n.lastHeartbeatAt)}</span>.
              Nothing is being scheduled on it, and anything it was running is lost. Check the
              machine and the <span className="font-mono">podium-node</span> service on it.
            </p>
          ))}
        </Alert>
      ) : null}

      {query.isPending ? (
        <NodesSkeleton />
      ) : nodes.length === 0 ? (
        // A failed list is not an empty fleet. Saying "no nodes enrolled" when the question was
        // never answered sends the operator looking for the wrong problem.
        query.error ? null : (
          <Empty
            icon={Server}
            title="No nodes enrolled"
            hint="Podium has nowhere to put a task until a machine enrolls. Adding one mints a single-use token and gives you the command to run on it."
            action={<EnrollPanel />}
          />
        )
      ) : (
        <>
          {/* Below its natural width the cells crush rather than wrap usefully — a meter and a
              ratio have nothing to break on — so the table scrolls inside its own container. */}
          <Table className="min-w-[62rem]">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Node</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-40">Slots</TableHead>
                <TableHead>Machine</TableHead>
                <TableHead>Labels</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Heartbeat</TableHead>
                <TableHead className="w-0" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.map((n) => (
                <NodeRow key={n.id} node={n} />
              ))}
            </TableBody>
          </Table>

          <p className="text-2xs leading-relaxed text-faint">
            Draining is a standing instruction that survives a restart of either daemon, and it is
            separate from status: a node that is drained and has stopped heartbeating reads{" "}
            <span className="font-mono">offline (draining)</span>.
          </p>
        </>
      )}
    </div>
  );
}

function NodeRow({ node }: { node: Node }) {
  const [copied, setCopied] = useState(false);
  const connected = isConnected(node);
  const draining = isDraining(node);
  const max = node.capacity?.maxTasks ?? 0;
  const running = connected ? node.runningTasks : undefined;
  const pct = max > 0 && running !== undefined ? (running / max) * 100 : 0;

  return (
    <TableRow
      data-testid="node-row"
      data-draining={draining ? "true" : "false"}
      // A node the server cannot reach is the one thing an operator came here to find, so the
      // whole row carries it rather than one cell.
      className={cn(!connected && "bg-warn/[0.06]")}
    >
      <TableCell>
        <div className="text-sm leading-tight font-medium text-fg">{node.name}</div>
        <div className="flex items-center gap-1">
          <span className="font-mono text-2xs text-faint">{node.id}</span>
          <Tooltip label={copied ? "Copied" : `Copy ${node.id}`}>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={`Copy the ID of ${node.name}`}
              onClick={() => {
                void navigator.clipboard?.writeText(node.id);
                setCopied(true);
              }}
              className="size-5 opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100"
            >
              {copied ? <Check className="text-ok" /> : <Copy />}
            </Button>
          </Tooltip>
        </div>
      </TableCell>

      <TableCell>
        <Badge tone={nodeStateTone(node)}>{nodeStateLabel(node)}</Badge>
        {draining ? <div className="mt-1 text-2xs text-faint">takes no new work</div> : null}
      </TableCell>

      <TableCell>
        <div className="flex items-baseline justify-between gap-2 whitespace-nowrap">
          <span className="tabular font-mono text-xs text-fg">
            {running ?? "—"}
            <span className="text-faint"> / {max}</span>
          </span>
          <span className="text-2xs text-faint">
            {connected ? `${node.freeSlots} free` : "not reporting"}
          </span>
        </div>
        <Progress
          value={pct}
          aria-label={`${running ?? 0} of ${max} slots in use on ${node.name}`}
          // The bar carries the node's own colour, so a drained or silent node cannot read as
          // healthy capacity out of the corner of an eye.
          tone={!connected ? "idle" : draining ? "warn" : "accent"}
          className={cn("mt-1.5", !connected && "opacity-45")}
        />
      </TableCell>

      <TableCell className="text-2xs whitespace-nowrap text-muted">
        <div className="tabular">{node.capacity?.cpuCores ?? "—"} cores</div>
        <div className="tabular">
          {node.capacity ? humanBytes(Number(node.capacity.memoryMb) * 1024 * 1024) : "—"}
        </div>
      </TableCell>

      <TableCell>
        {node.labels.length === 0 ? (
          <span className="text-2xs text-faint">none</span>
        ) : (
          <div className="flex max-w-56 flex-wrap gap-1">
            {node.labels.map((l) => (
              <Chip key={l} className="font-mono">
                {l}
              </Chip>
            ))}
          </div>
        )}
      </TableCell>

      <TableCell className="font-mono text-2xs whitespace-nowrap text-muted">
        {node.version || "—"}
      </TableCell>

      <TableCell className="whitespace-nowrap">
        <Tooltip
          label={
            connected
              ? absolute(node.lastHeartbeatAt)
              : `Last heartbeat ${absolute(node.lastHeartbeatAt)}`
          }
        >
          <span
            className={cn(
              "tabular inline-flex items-center gap-1.5 text-xs",
              connected ? "text-muted" : "font-medium text-warn",
            )}
          >
            {connected ? null : <AlertTriangle className="size-3.5 shrink-0" />}
            {relative(node.lastHeartbeatAt)}
          </span>
        </Tooltip>
      </TableCell>

      <TableCell className="text-right">
        <NodeActions node={node} />
      </TableCell>
    </TableRow>
  );
}

function Stat({
  label,
  value,
  hint,
  tone,
  loading,
}: {
  label: string;
  value: number;
  hint: string;
  tone: string;
  loading: boolean;
}) {
  return (
    <div className="space-y-1 border-l border-hairline px-5 py-4 first:border-l-0">
      <p className="text-2xs tracking-wide text-faint uppercase">{label}</p>
      {loading ? (
        <Skeleton className="h-7 w-8" />
      ) : (
        <p className={cn("tabular text-xl leading-none font-semibold", tone)}>{value}</p>
      )}
      <p className="text-2xs text-faint">{hint}</p>
    </div>
  );
}

/** The loading state is the real table with its content greyed, so nothing moves when it lands. */
function NodesSkeleton() {
  return (
    <div
      aria-busy="true"
      aria-label="Loading nodes"
      className="overflow-hidden rounded-xl border border-border bg-card shadow-xs"
    >
      <div className="flex gap-4 border-b border-border bg-panel px-3 py-2.5">
        {Array.from({ length: 6 }, (_, c) => (
          <Skeleton key={c} className="h-3 flex-1" />
        ))}
      </div>
      {Array.from({ length: 4 }, (_, r) => (
        <div key={r} className="flex items-center gap-4 border-b border-hairline px-3 py-4 last:border-0">
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3.5 w-24" />
            <Skeleton className="h-2.5 w-20" />
          </div>
          <Skeleton className="h-4 w-16 flex-none" />
          <div className="w-40 flex-none space-y-1.5">
            <Skeleton className="h-3 w-12" />
            <Skeleton className="h-1.5 w-full" />
          </div>
          <Skeleton className="h-3 flex-1" />
          <Skeleton className="h-3 flex-1" />
          <Skeleton className="h-3 w-16 flex-none" />
        </div>
      ))}
    </div>
  );
}
