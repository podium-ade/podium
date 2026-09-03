import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useToast } from "./Toast";
import { NodeStatus } from "../gen/podium/v1/common_pb";
import type { Node } from "../gen/podium/v1/admin_pb";
import { admin, Code, connectCode, errorMessage } from "../lib/client";

type Action = "drain" | "undrain" | "delete" | "rekey";

const CONFIRM: Record<Action, (n: Node) => string> = {
  drain: (n) => `Stop scheduling new work on ${n.name}? Tasks it is running now finish.`,
  undrain: (n) => `Put ${n.name} back in the pool?`,
  delete: (n) => `Forget ${n.name}? Its finished tasks keep its ID in their history.`,
  rekey: (n) =>
    `Unbind ${n.name} from its Tailscale device? The next Hello binds it to whatever device it comes from.`,
};

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
  const [pending, setPending] = useState<Action>();
  const [refused, setRefused] = useState<string>();

  const refresh = () => qc.invalidateQueries({ queryKey: ["nodes"] });

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
          return admin.deleteNode({ nodeId });
      }
    },
    onSuccess: (_res, action) => {
      setRefused(undefined);
      toast(`${node.name}: ${PAST[action]}.`, "ok");
      void refresh();
    },
    onError: (err) => {
      // DeleteNode refuses with FailedPrecondition twice over — the node is online and not
      // drained, or it still has tasks running — and the message names which and how many.
      // Rendering it beside the node is the whole answer; a toast would scroll away.
      if (connectCode(err) === Code.FailedPrecondition || connectCode(err) === Code.NotFound) {
        setRefused(errorMessage(err));
        void refresh();
        return;
      }
      toast(`${node.name}: ${errorMessage(err)}`);
    },
  });

  const ask = (action: Action) => {
    setRefused(undefined);
    setPending(action);
  };

  const isDraining = node.draining || node.status === NodeStatus.DRAINING;

  if (pending) {
    return (
      <div className="flex flex-col items-end gap-1 text-xs">
        <span className="text-muted">{CONFIRM[pending](node)}</span>
        <span className="flex gap-2">
          <button
            type="button"
            onClick={() => {
              const action = pending;
              setPending(undefined);
              run.mutate(action);
            }}
            className={`rounded border px-2 py-0.5 ${
              pending === "delete" ? "border-err/60 text-err" : "border-accent/60 text-accent"
            }`}
          >
            Yes, {pending}
          </button>
          <button
            type="button"
            onClick={() => setPending(undefined)}
            className="rounded border border-border px-2 py-0.5"
          >
            Cancel
          </button>
        </span>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <div className="flex flex-wrap justify-end gap-2 text-xs">
        {isDraining ? (
          <Button label="Undrain" onClick={() => ask("undrain")} busy={run.isPending} />
        ) : (
          <Button label="Drain" onClick={() => ask("drain")} busy={run.isPending} />
        )}
        {node.tsStableId ? (
          <Button label="Rekey" onClick={() => ask("rekey")} busy={run.isPending} />
        ) : null}
        <Button label="Delete" onClick={() => ask("delete")} busy={run.isPending} danger />
      </div>
      {refused ? (
        <p className="max-w-sm text-right text-xs text-warn">{refused}</p>
      ) : null}
    </div>
  );
}

const PAST: Record<Action, string> = {
  drain: "draining, no new work",
  undrain: "back in the pool",
  delete: "deleted",
  rekey: "unbound from its Tailscale device",
};

function Button({
  label,
  onClick,
  busy,
  danger,
}: {
  label: string;
  onClick: () => void;
  busy: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      disabled={busy}
      onClick={onClick}
      className={`rounded border border-border px-2 py-0.5 disabled:opacity-40 ${
        danger ? "hover:border-err hover:text-err" : "hover:border-accent hover:text-accent"
      }`}
    >
      {label}
    </button>
  );
}
