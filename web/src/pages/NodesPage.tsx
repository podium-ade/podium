import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Chip, Dot } from "../components/Badge";
import { Empty } from "../components/Empty";
import { EnrollPanel } from "../components/EnrollPanel";
import { NodeActions } from "../components/NodeActions";
import { PageHeader } from "../components/PageHeader";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import { admin, errorMessage } from "../lib/client";
import { absolute, nodeStateLabel, nodeStateTone, relative } from "../lib/format";
import { cn } from "../lib/utils";

const POLL_MS = 5000;

export function NodesPage() {
  const toast = useToast();
  const query = useQuery({
    queryKey: ["nodes"],
    queryFn: () => admin.listNodes({}),
    refetchInterval: POLL_MS,
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListNodes: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const nodes = query.data?.nodes ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Nodes"
        description="Machines that run tasks. Drain one before you take it away."
      />

      {query.isPending ? (
        <TableSkeleton cols={7} />
      ) : nodes.length === 0 ? (
        <Empty title="No nodes enrolled" hint="Create an enrollment token below." />
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Labels</TableHead>
              <TableHead>Running</TableHead>
              <TableHead>CPU</TableHead>
              <TableHead>Memory</TableHead>
              <TableHead>Version</TableHead>
              <TableHead>Heartbeat</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.map((n) => {
              const draining = n.draining || n.status === NodeStatus.DRAINING;
              return (
                <TableRow
                  key={n.id}
                  data-testid="node-row"
                  data-draining={draining ? "true" : "false"}
                  // A drained node has to be visibly not a working one at a glance, not just
                  // by reading its status cell.
                  className={cn(draining && "bg-warn/5 opacity-80")}
                >
                  <TableCell>
                    <div className={draining ? "text-warn" : ""}>{n.name}</div>
                    <div className="font-mono text-xs text-muted">{n.id}</div>
                  </TableCell>
                  <TableCell className="text-xs whitespace-nowrap">
                    <Dot tone={nodeStateTone(n)} title={nodeStateLabel(n)} /> {nodeStateLabel(n)}
                    {draining ? <div className="text-muted">takes no new work</div> : null}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {n.labels.length === 0 ? (
                        <span className="text-xs text-muted">—</span>
                      ) : (
                        n.labels.map((l) => <Chip key={l}>{l}</Chip>)
                      )}
                    </div>
                  </TableCell>
                  {/* running_tasks / free_slots are only populated for a node holding a live
                      stream on this server process; "connected" is the status, not the slots. */}
                  <TableCell className="font-mono text-xs">
                    {n.status === NodeStatus.OFFLINE || n.status === NodeStatus.UNREACHABLE
                      ? `— / ${n.capacity?.maxTasks ?? 0}`
                      : `${n.runningTasks} / ${n.capacity?.maxTasks ?? 0}`}
                  </TableCell>
                  <TableCell className="text-xs">{n.capacity?.cpuCores ?? "—"} cores</TableCell>
                  <TableCell className="text-xs">
                    {n.capacity ? `${Number(n.capacity.memoryMb)} MB` : "—"}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{n.version || "—"}</TableCell>
                  <TableCell className="text-xs whitespace-nowrap" title={absolute(n.lastHeartbeatAt)}>
                    {relative(n.lastHeartbeatAt)}
                  </TableCell>
                  <TableCell>
                    <NodeActions node={n} />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}

      <p className="text-xs text-muted">
        Draining is a standing instruction that survives a restart of either daemon, and it is
        separate from status: a node that is drained and has stopped heartbeating reads{" "}
        <span className="font-mono">offline (draining)</span>. Delete refuses a node that is
        online and not drained, or one with tasks still running on it.
      </p>

      <EnrollPanel />
    </div>
  );
}
