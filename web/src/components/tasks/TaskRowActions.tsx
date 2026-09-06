import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Copy, Ellipsis, SquareArrowOutUpRight, X } from "lucide-react";
import type { Task } from "../../gen/podium/v1/task_pb";
import { errorMessage, tasks } from "../../lib/client";
import { isTerminal, taskStatusLabel } from "../../lib/format";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../ui/dropdown-menu";

/**
 * The per-row menu: everything an operator wants to do to a task without first opening it.
 *
 * Cancel is the only destructive one and it behaves the way the detail page's does — CancelTask
 * returns the task unchanged, because with no runner the command is PID 1 and a
 * default-disposition SIGTERM is discarded until the node's 30s SIGKILL. So the toast promises a
 * request, not an outcome, and the 5s poll brings the real terminal status back.
 */
export function TaskRowActions({ task }: { task: Task }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);

  const cancel = useMutation({
    mutationFn: () => tasks.cancelTask({ taskId: task.id, reason: "cancelled from the task list" }),
    onSuccess: () => {
      setConfirming(false);
      toast("Cancel requested — the node has up to 30s to stop the container.", "ok");
      void qc.invalidateQueries({ queryKey: ["tasks"] });
    },
    onError: (err) => toast(`CancelTask: ${errorMessage(err)}`),
  });

  const copy = (value: string, what: string) => {
    void navigator.clipboard?.writeText(value);
    toast(`${what} copied.`, "ok");
  };

  const image = task.spec?.image ?? "";

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={`Actions for task ${task.id}`}
            className="data-[state=open]:bg-raised data-[state=open]:text-fg"
          >
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => void navigate(`/tasks/${task.id}`)}>
            <SquareArrowOutUpRight />
            Open task
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => copy(task.id, "Task ID")}>
            <Copy />
            Copy ID
          </DropdownMenuItem>
          {image ? (
            <DropdownMenuItem onSelect={() => copy(image, "Image")}>
              <Copy />
              Copy image
            </DropdownMenuItem>
          ) : null}
          {isTerminal(task.status) ? null : (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="danger" onSelect={() => setConfirming(true)}>
                <X />
                Cancel task
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Cancel this task?</DialogTitle>
            <DialogDescription>
              <span className="font-mono text-fg">{task.id}</span>
              {` is ${taskStatusLabel(task.status)}`}
              {image ? (
                <>
                  {" — "}
                  <span className="font-mono text-fg">{image}</span>
                </>
              ) : null}
              . Cancelling asks the node to stop it: the container gets a SIGTERM and up to 30
              seconds before it is killed. Anything the task has already written is kept.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Keep running
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={cancel.isPending}
              onClick={() => cancel.mutate()}
            >
              {cancel.isPending ? "Cancelling…" : "Cancel task"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
