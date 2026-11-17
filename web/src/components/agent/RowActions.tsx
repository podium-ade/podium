import { useState, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Copy, Ellipsis, SquareArrowOutUpRight, Trash2 } from "lucide-react";
import { errorMessage } from "../../lib/client";
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
 * RowActions is the ⋯ menu on the assistant's list screens (Playbooks, Skills), in the order it
 * is reached for: open the item, copy its name, and — when the viewer may — delete it behind a
 * confirmation.
 */
export function RowActions({
  noun,
  name,
  title,
  href,
  testId,
  onDelete,
  deleteDescription,
}: {
  /** "playbook", "skill": used in labels and the toast. */
  noun: string;
  name: string;
  /** How the item is written in the dialog title, e.g. "/review". Defaults to the name. */
  title?: string;
  /** The item's page. Absent hides View details. */
  href?: string;
  /** Prefix for test ids: `${testId}-actions`, `${testId}-row-delete-confirm`. */
  testId: string;
  /** Deletes the item. Absent hides Delete. Resolves once the list has been re-read. */
  onDelete?: () => Promise<unknown>;
  deleteDescription?: ReactNode;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const shown = title ?? name;

  const remove = useMutation({
    mutationFn: () => onDelete!(),
    onSuccess: () => {
      setConfirming(false);
      toast(`${shown} deleted.`, "ok");
    },
    onError: (err) => toast(errorMessage(err)),
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
            data-testid={`${testId}-actions`}
            className="data-[state=open]:bg-raised data-[state=open]:text-fg"
          >
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {href ? (
            <DropdownMenuItem onSelect={() => void navigate(href)}>
              <SquareArrowOutUpRight />
              View details
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem
            onSelect={() => {
              void navigator.clipboard?.writeText(name);
              toast(`${capitalize(noun)} name copied.`, "ok");
            }}
          >
            <Copy />
            Copy name
          </DropdownMenuItem>
          {onDelete ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="danger" onSelect={() => setConfirming(true)}>
                <Trash2 />
                Delete {noun}
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>

      {onDelete ? (
        <Dialog open={confirming} onOpenChange={setConfirming}>
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle>Delete {shown}?</DialogTitle>
              {deleteDescription ? <DialogDescription>{deleteDescription}</DialogDescription> : null}
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>
                Keep
              </Button>
              <Button
                variant="destructive"
                size="sm"
                data-testid={`${testId}-row-delete-confirm`}
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {remove.isPending ? "Deleting…" : `Delete ${noun}`}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </span>
  );
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
