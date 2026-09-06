import { useState } from "react";
import type { ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound, MoreHorizontal, PauseCircle, PlayCircle, Trash2 } from "lucide-react";
import { useToast } from "./Toast";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Label } from "./ui/label";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import type { Node } from "../gen/podium/v1/admin_pb";
import { admin, Code, connectCode, errorMessage } from "../lib/client";

type Action = "drain" | "undrain" | "delete" | "rekey";

interface Confirm {
  title: string;
  body: ReactNode;
  /** The quieter second paragraph: the consequence an operator does not already know. */
  note?: ReactNode;
  verb: string;
  busy: string;
  variant: "default" | "danger" | "destructive";
}

function confirmFor(action: Action, node: Node): Confirm {
  switch (action) {
    case "drain":
      return {
        title: `Drain ${node.name}?`,
        body: (
          <>
            Podium stops scheduling new tasks on {node.name}. Whatever it is running right now
            keeps running until it finishes — draining is how a machine is taken out of service
            without killing the work already on it.
          </>
        ),
        note: "The instruction is stored against the node, so it survives a restart of either daemon. Undrain puts it back in the pool.",
        verb: "Drain node",
        busy: "Draining…",
        variant: "default",
      };
    case "undrain":
      return {
        title: `Put ${node.name} back in the pool?`,
        body: (
          <>
            Podium starts scheduling new tasks on {node.name} again. Queued tasks that match its
            labels can land on it from the next scheduler pass.
          </>
        ),
        verb: "Undrain node",
        busy: "Undraining…",
        variant: "default",
      };
    case "rekey":
      return {
        title: `Unbind ${node.name} from its Tailscale device?`,
        body: (
          <>
            {node.name} is pinned to Tailscale device{" "}
            <span className="font-mono text-fg">{node.tsStableId}</span>, and a copy of its
            identity taken to any other device is refused. Rekeying drops that binding: the node
            keeps its ID, its labels and its history, and re-binds to whatever device its next
            Hello arrives from.
          </>
        ),
        note: "Until it re-binds, its node key alone is enough to connect — that exposure is exactly what the binding removes. Rekey a node when you are about to move or rebuild it, not as a matter of routine.",
        verb: "Rekey node",
        busy: "Rekeying…",
        variant: "danger",
      };
    case "delete":
      return {
        title: `Delete ${node.name}?`,
        body: (
          <>
            Podium forgets {node.name} (<span className="font-mono">{node.id}</span>). Its
            finished tasks keep the node ID they ran on, so the task history stays readable.
          </>
        ),
        note: "Deleting does not stop the daemon on the machine. A node whose identity.json is still on disk keeps trying to reconnect and is told its key is unknown; delete that file too before you re-enroll it.",
        verb: "Delete node",
        busy: "Deleting…",
        variant: "destructive",
      };
  }
}

/**
 * NodeActions is drain / undrain / delete / rekey for one node.
 *
 * All four take a node ID, never a name. The optimistic part is deliberately small: the button
 * says what was asked for, the row keeps showing what the server last reported, and the list is
 * re-read straight away. `draining` flips the moment DrainNode returns because the server has
 * committed it by then; `status` is derived from heartbeats and is not ours to predict.
 */
export function NodeActions({ node }: { node: Node }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [confirming, setConfirming] = useState<Action>();
  const [force, setForce] = useState(false);
  const [refused, setRefused] = useState<string>();

  function close() {
    setConfirming(undefined);
    setRefused(undefined);
    setForce(false);
  }

  const run = useMutation({
    mutationFn: async (action: Action) => {
      const nodeId = node.id;
      switch (action) {
        case "drain":
          return admin.drainNode({ nodeId });
        case "undrain":
          return admin.undrainNode({ nodeId });
        case "rekey":
          return admin.rekeyNode({ nodeId });
        case "delete":
          return admin.deleteNode({ nodeId, force });
      }
    },
    onSuccess: (_res, action) => {
      close();
      toast(`${node.name}: ${PAST[action]}.`, "ok");
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
    onError: (err) => {
      // DeleteNode refuses with FailedPrecondition twice over — the node is connected and not
      // drained, or it still has tasks running — and the message names which and how many.
      // Keeping the dialog open with the refusal in it is the whole answer: a toast would
      // scroll away, and the force checkbox that answers the first case is right there.
      if (connectCode(err) === Code.FailedPrecondition || connectCode(err) === Code.NotFound) {
        setRefused(errorMessage(err));
        void qc.invalidateQueries({ queryKey: ["nodes"] });
        return;
      }
      close();
      toast(`${node.name}: ${errorMessage(err)}`);
    },
  });

  const isDraining = node.draining || node.status === NodeStatus.DRAINING;
  const confirm = confirming ? confirmFor(confirming, node) : undefined;

  return (
    <>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Actions for ${node.name}`}
            disabled={run.isPending}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {isDraining ? (
            <DropdownMenuItem onSelect={() => setConfirming("undrain")}>
              <PlayCircle />
              Undrain
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem onSelect={() => setConfirming("drain")}>
              <PauseCircle />
              Drain
            </DropdownMenuItem>
          )}
          {/* Rekey is meaningless for a node that never bound to a Tailscale device. */}
          {node.tsStableId ? (
            <DropdownMenuItem onSelect={() => setConfirming("rekey")}>
              <KeyRound />
              Rekey
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="danger" onSelect={() => setConfirming("delete")}>
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={confirming !== undefined} onOpenChange={(open) => !open && close()}>
        {confirm ? (
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{confirm.title}</DialogTitle>
              <DialogDescription>{confirm.body}</DialogDescription>
            </DialogHeader>

            {confirm.note ? (
              <p className="-mt-1 text-2xs leading-relaxed text-faint">{confirm.note}</p>
            ) : null}

            {confirming === "delete" && !isDraining ? (
              <div className="rounded-lg border border-border bg-raised/40 p-3">
                <div className="flex items-start gap-2.5">
                  <Checkbox
                    id="node-delete-force"
                    className="mt-0.5"
                    checked={force}
                    onCheckedChange={(v) => setForce(v === true)}
                  />
                  <div className="min-w-0 space-y-1">
                    <Label htmlFor="node-delete-force" className="text-fg">
                      Delete it even though it is not drained
                    </Label>
                    <p className="text-2xs leading-relaxed text-faint">
                      Podium refuses to delete a node that still holds a connection and has not
                      been drained. Force skips that check. It does not skip the other one: a
                      node with tasks still running on it cannot be deleted at all, forced or
                      not.
                    </p>
                  </div>
                </div>
              </div>
            ) : null}

            {refused ? (
              <Alert variant="warn" title="The server refused">
                {refused}
              </Alert>
            ) : null}

            <DialogFooter>
              <DialogClose asChild>
                <Button variant="outline" size="sm">
                  Cancel
                </Button>
              </DialogClose>
              <Button
                variant={confirm.variant}
                size="sm"
                disabled={run.isPending}
                onClick={() => {
                  if (!confirming) return;
                  setRefused(undefined);
                  run.mutate(confirming);
                }}
              >
                {run.isPending ? confirm.busy : confirm.verb}
              </Button>
            </DialogFooter>
          </DialogContent>
        ) : null}
      </Dialog>
    </>
  );
}

const PAST: Record<Action, string> = {
  drain: "draining, no new work",
  undrain: "back in the pool",
  delete: "deleted",
  rekey: "unbound from its Tailscale device",
};
