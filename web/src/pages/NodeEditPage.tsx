import { useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router";
import { AlertTriangle, RotateCcw, Server } from "lucide-react";
import { Badge, Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { NodeActions } from "../components/NodeActions";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { CopyValue } from "../components/task/CopyValue";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import type { Node } from "../gen/podium/v1/admin_pb";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import { admin, errorMessage } from "../lib/client";
import { absolute, humanBytes, nodeStateLabel, nodeStateTone, relative } from "../lib/format";

const POLL_MS = 5000;

/** The ceiling podium-server's SetNodeSlots enforces. Kept in step with api.MaxNodeSlots. */
const MAX_SLOTS = 256;

function isConnected(n: Node): boolean {
  return n.status === NodeStatus.ONLINE || n.status === NodeStatus.DRAINING;
}

/**
 * NodeEditPage is one node: what the machine is, and the one thing about it the control plane
 * gets to decide — how many tasks it runs at once.
 *
 * There is no GetNode RPC, so it reads the list and picks its row out, which is also what
 * `podium node drain` does. That keeps the page honest about staleness: everything on it is
 * the same answer the fleet table is showing, refreshed on the same interval.
 */
export function NodeEditPage() {
  const { id = "" } = useParams();
  const query = useQuery({
    queryKey: ["nodes"],
    queryFn: () => admin.listNodes({}),
    refetchInterval: POLL_MS,
    placeholderData: (prev) => prev,
  });

  const node = useMemo(
    () => query.data?.nodes.find((n) => n.id === id),
    [query.data, id],
  );

  if (query.isPending) return <NodeSkeleton />;

  if (query.error && !node) {
    return (
      <Alert variant="destructive" title="Could not read this node">
        {errorMessage(query.error)}. Check that podium-server is running, and that the token
        this console is using is still valid.
      </Alert>
    );
  }

  if (!node) {
    return (
      <div className="space-y-5">
        <PageHeader title="Node" back={{ to: "/nodes", label: "Nodes" }} />
        <Empty
          icon={Server}
          title="No such node"
          hint={`Nothing enrolled here has the ID ${id}. It may have been deleted, or the ID may be from another control plane.`}
        />
      </div>
    );
  }

  return <NodeEditor node={node} listFailed={query.error !== null} />;
}

function NodeEditor({ node, listFailed }: { node: Node; listFailed: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const connected = isConnected(node);
  const configured = node.capacity?.maxTasks ?? 0;
  const override = node.maxTasksOverride;
  const inForce = override ?? configured;

  // The input is seeded from the node and re-seeded only when the server's answer actually
  // moves, adjusted during render rather than in an effect: a poll every five seconds must not
  // rewrite what is being typed, and a save that lands must not leave the old number in the box.
  const [slots, setSlots] = useState(String(inForce));
  const [seed, setSeed] = useState(inForce);
  if (seed !== inForce) {
    setSeed(inForce);
    setSlots(String(inForce));
  }

  const parsed = Number(slots);
  const valid = Number.isInteger(parsed) && parsed >= 1 && parsed <= MAX_SLOTS;
  const changed = valid && parsed !== inForce;

  const save = useMutation({
    mutationFn: (maxTasks: number) => admin.setNodeSlots({ nodeId: node.id, maxTasks }),
    onSuccess: (res) => {
      const n = res.node;
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      if (n?.maxTasksOverride === undefined) {
        toast(`${node.name}: back on its own max_tasks of ${n?.capacity?.maxTasks ?? 0}.`, "ok");
        return;
      }
      toast(`${node.name}: ${n.maxTasksOverride} slot(s).`, "ok");
    },
    onError: (err) => toast(`${node.name}: ${errorMessage(err)}`),
  });

  return (
    <div className="space-y-5">
      <PageHeader
        back={{ to: "/nodes", label: "Nodes" }}
        title={node.name}
        description="What this machine is, and how much work the control plane gives it."
        actions={<NodeActions node={node} />}
        meta={
          <>
            <Badge tone={nodeStateTone(node)}>{nodeStateLabel(node)}</Badge>
            <CopyValue value={node.id} label="node ID" />
          </>
        }
      />

      {listFailed ? (
        <Alert variant="warn" title="This page has stopped refreshing">
          The last read of the node list failed, so what is below is the last answer this
          console got. Saving still works; the numbers beside it may be stale.
        </Alert>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Slots</CardTitle>
          <CardDescription>
            How many tasks {node.name} runs at once. This overrides the{" "}
            <span className="font-mono">max_tasks</span> in the node's own configuration, in
            both directions — the number is stored here and the node is told it, because the
            node is the only thing that can enforce a budget.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="node-slots">Tasks at once</Label>
              <Input
                id="node-slots"
                type="number"
                min={1}
                max={MAX_SLOTS}
                value={slots}
                aria-invalid={slots !== "" && !valid ? true : undefined}
                aria-describedby="node-slots-hint"
                onChange={(e) => setSlots(e.target.value)}
                className="tabular w-28"
              />
            </div>
            <Button
              size="sm"
              disabled={!changed || save.isPending}
              onClick={() => save.mutate(parsed)}
            >
              {save.isPending ? "Saving…" : "Save"}
            </Button>
            {override !== undefined ? (
              <Button
                variant="outline"
                size="sm"
                disabled={save.isPending}
                onClick={() => save.mutate(0)}
              >
                <RotateCcw />
                Use the node's {configured}
              </Button>
            ) : null}
          </div>

          <p id="node-slots-hint" className="text-2xs leading-relaxed text-muted">
            {slots !== "" && !valid
              ? `A slot count is a whole number between 1 and ${MAX_SLOTS}.`
              : override !== undefined
                ? `Set here. The node's own configuration says ${configured}, and this number wins until you clear it.`
                : `The node's own configuration says ${configured}. Saving a different number overrides it.`}
          </p>

          <div className="grid grid-cols-2 gap-x-4 gap-y-3 border-t border-hairline pt-4 sm:grid-cols-4">
            <Fact label="In force">
              <span className="tabular">{inForce}</span>
            </Fact>
            <Fact label="Node's own">
              <span className="tabular">{configured}</span>
            </Fact>
            <Fact label="Running">
              {connected ? (
                <span className="tabular">{node.runningTasks}</span>
              ) : (
                <Unknown>not reporting</Unknown>
              )}
            </Fact>
            <Fact label="Free">
              {connected ? (
                <span className="tabular">{node.freeSlots}</span>
              ) : (
                <Unknown>not reporting</Unknown>
              )}
            </Fact>
          </div>

          {connected && node.runningTasks > inForce ? (
            <Alert variant="warn" title={`${node.name} is running more than ${inForce}`}>
              Nothing is taken down by a lower number: the {node.runningTasks} tasks already on
              this node finish normally, and it accepts no new work until enough of them have.
            </Alert>
          ) : null}

          {!connected ? (
            <p className="text-2xs leading-relaxed text-faint">
              <AlertTriangle className="mr-1 inline size-3 -translate-y-px" />
              This node is not connected, so the change cannot be delivered now. It is stored
              against the node and applied when it next reconnects.
            </p>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Machine</CardTitle>
          <CardDescription>
            What the node reported about itself. All of it comes from the daemon's own
            configuration and the host it runs on, so none of it is editable here.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
            <Fact label="CPU">
              {node.capacity?.cpuCores ? (
                <span className="tabular">{node.capacity.cpuCores} cores</span>
              ) : (
                <Unknown>unmeasured</Unknown>
              )}
            </Fact>
            <Fact label="Memory">
              {node.capacity?.memoryMb ? (
                <span className="tabular">
                  {humanBytes(Number(node.capacity.memoryMb) * 1024 * 1024)}
                </span>
              ) : (
                <Unknown>unmeasured</Unknown>
              )}
            </Fact>
            <Fact label="Version">
              {node.version ? (
                <span className="font-mono text-xs">{node.version}</span>
              ) : (
                <Unknown>unknown</Unknown>
              )}
            </Fact>
            <Fact label="Labels" className="col-span-2 sm:col-span-3">
              {node.labels.length === 0 ? (
                <Unknown>none — a task with no label requirement can land here</Unknown>
              ) : (
                <span className="flex flex-wrap gap-1">
                  {node.labels.map((l) => (
                    <Chip key={l} className="font-mono">
                      {l}
                    </Chip>
                  ))}
                </span>
              )}
            </Fact>
            <Fact label="Last heartbeat">
              <span className="tabular" title={absolute(node.lastHeartbeatAt)}>
                {relative(node.lastHeartbeatAt)}
              </span>
            </Fact>
            <Fact label="Enrolled">
              <span className="tabular" title={absolute(node.createdAt)}>
                {relative(node.createdAt)}
              </span>
            </Fact>
            <Fact label="Tailscale device">
              {node.tsStableId ? (
                <span className="font-mono text-xs">{node.tsStableId}</span>
              ) : (
                <Unknown>unbound</Unknown>
              )}
            </Fact>
          </div>
        </CardContent>
      </Card>

      <p className="text-2xs leading-relaxed text-faint">
        Labels and the machine's own <span className="font-mono">max_tasks</span> come from the
        daemon: they are set in <span className="font-mono">/etc/podium/node.yaml</span> or the{" "}
        <span className="font-mono">PODIUM_NODE_*</span> environment, and a node re-advertises
        them on every reconnect.
      </p>
    </div>
  );
}

function Fact({
  label,
  className,
  children,
}: {
  label: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={className}>
      <p className="text-2xs tracking-wide text-faint uppercase">{label}</p>
      <div className="mt-0.5 text-sm text-fg">{children}</div>
    </div>
  );
}

function Unknown({ children }: { children: ReactNode }) {
  return <span className="text-xs text-faint">{children}</span>;
}

function NodeSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading node" className="space-y-5">
      <Skeleton className="h-7 w-48" />
      <Skeleton className="h-48 w-full rounded-xl" />
      <Skeleton className="h-40 w-full rounded-xl" />
    </div>
  );
}
