import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Chip, Dot } from "../components/Badge";
import { Empty } from "../components/Empty";
import { EnrollPanel } from "../components/EnrollPanel";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import { admin, errorMessage } from "../lib/client";
import { nodeStatusLabel, nodeStatusTone, relative } from "../lib/format";

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
    <div className="space-y-4">
      <h1 className="text-base font-semibold">Nodes</h1>

      {query.isPending ? (
        <TableSkeleton cols={7} />
      ) : nodes.length === 0 ? (
        <Empty title="No nodes enrolled" hint="Create an enrollment token below." />
      ) : (
        <div className="overflow-x-auto rounded border border-border">
          <table className="w-full text-left text-sm">
            <thead className="bg-panel text-xs text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">Name</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Labels</th>
                <th className="px-3 py-2 font-medium">Running</th>
                <th className="px-3 py-2 font-medium">CPU</th>
                <th className="px-3 py-2 font-medium">Memory</th>
                <th className="px-3 py-2 font-medium">Version</th>
                <th className="px-3 py-2 font-medium">Heartbeat</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => (
                <tr key={n.id} className="border-t border-border">
                  <td className="px-3 py-1.5">
                    <div>{n.name}</div>
                    <div className="font-mono text-xs text-muted">{n.id}</div>
                  </td>
                  <td className="px-3 py-1.5 text-xs whitespace-nowrap">
                    <Dot tone={nodeStatusTone(n.status)} title={nodeStatusLabel(n.status)} />{" "}
                    {nodeStatusLabel(n.status)}
                  </td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      {n.labels.length === 0 ? (
                        <span className="text-xs text-muted">—</span>
                      ) : (
                        n.labels.map((l) => <Chip key={l}>{l}</Chip>)
                      )}
                    </div>
                  </td>
                  {/* running_tasks / free_slots are only populated for a node holding a live
                      stream on this server process; "connected" is the status, not the slots. */}
                  <td className="px-3 py-1.5 font-mono text-xs">
                    {n.status === NodeStatus.ONLINE
                      ? `${n.runningTasks} / ${n.capacity?.maxTasks ?? 0}`
                      : `— / ${n.capacity?.maxTasks ?? 0}`}
                  </td>
                  <td className="px-3 py-1.5 text-xs">{n.capacity?.cpuCores ?? "—"} cores</td>
                  <td className="px-3 py-1.5 text-xs">
                    {n.capacity ? `${Number(n.capacity.memoryMb)} MB` : "—"}
                  </td>
                  <td className="px-3 py-1.5 font-mono text-xs">{n.version || "—"}</td>
                  <td className="px-3 py-1.5 text-xs whitespace-nowrap">
                    {relative(n.lastHeartbeatAt)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <EnrollPanel />
    </div>
  );
}
