import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Copy, Ellipsis, SquareArrowOutUpRight, Trash2 } from "lucide-react";
import { agent, errorMessage } from "../../lib/client";
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
 * The per-row menu on the playbook list, in the order it is reached for: open it, copy its
 * name (what a person types after the slash), and, for someone who manages infra, delete it.
 */
export function PlaybookRowActions({
  name,
  href,
  canDelete,
}: {
  name: string;
  href: string;
  canDelete: boolean;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);

  const remove = useMutation({
    mutationFn: () => agent.deletePlaybook({ name }),
    onSuccess: async () => {
      setConfirming(false);
      toast(`/${name} deleted.`, "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
    },
    onError: (err) => toast(`DeletePlaybook: ${errorMessage(err)}`),
  });

  return (
    // The menu and the dialog are portalled, but React still bubbles their clicks through
    // here; stopping them keeps a click inside either from also opening the row.
    <span onClick={(e) => e.stopPropagation()} className="contents">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={`Actions for ${name}`}
            data-testid="playbook-actions"
            className="data-[state=open]:bg-raised data-[state=open]:text-fg"
          >
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => void navigate(href)}>
            <SquareArrowOutUpRight />
            View details
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => {
              void navigator.clipboard?.writeText(name);
              toast("Playbook name copied.", "ok");
            }}
          >
            <Copy />
            Copy name
          </DropdownMenuItem>
          {canDelete ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="danger" onSelect={() => setConfirming(true)}>
                <Trash2 />
                Delete playbook
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Delete /{name}?</DialogTitle>
            <DialogDescription>
              The definition goes with it: prompt, tools, limits and the secrets it names.
              Anything that routes to this playbook stops working on the next turn. There is no
              undo.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Keep
            </Button>
            <Button
              variant="destructive"
              size="sm"
              data-testid="playbook-row-delete-confirm"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending ? "Deleting…" : "Delete playbook"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </span>
  );
}
