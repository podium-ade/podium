import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Ellipsis, MessageSquarePlus, SquareArrowOutUpRight, Trash2 } from "lucide-react";
import type { Personality } from "../../gen/podium/agent/v1/agent_pb";
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
 * The per-row menu on the assistant list, in the order it is reached for: open it, talk to
 * it, and, for a voice of your own, delete it. Podium cannot be deleted, so its menu stops
 * at Talk.
 */
export function AssistantRowActions({
  name,
  href,
  chatHref,
  personalId,
}: {
  name: string;
  href: string;
  chatHref: string;
  /** Set for a personal assistant: the id Delete removes. */
  personalId?: string;
}) {
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);

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
            data-testid="assistant-actions"
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
          <DropdownMenuItem onSelect={() => void navigate(chatHref)}>
            <MessageSquarePlus />
            Talk to it
          </DropdownMenuItem>
          {personalId ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="danger" onSelect={() => setConfirming(true)}>
                <Trash2 />
                Delete assistant
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>

      {personalId ? (
        <DeleteAssistantDialog id={personalId} name={name} open={confirming} onOpenChange={setConfirming} />
      ) : null}
    </span>
  );
}

/** DeleteAssistantDialog confirms removing a personal assistant, from its row or its page. */
export function DeleteAssistantDialog({
  id,
  name,
  open,
  onOpenChange,
  onDeleted,
}: {
  id: string;
  name: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted?: () => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const remove = useMutation({
    mutationFn: () => agent.deletePersonality({ id }),
    onSuccess: async () => {
      onOpenChange(false);
      qc.setQueryData(["agent", "personalities"], (current: { personalities: Personality[] } | undefined) => ({
        personalities: (current?.personalities ?? []).filter((p) => p.id !== id),
      }));
      toast(`${name} deleted.`, "ok");
      onDeleted?.();
      await qc.invalidateQueries({ queryKey: ["agent", "personalities"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Delete {name}?</DialogTitle>
          <DialogDescription>
            Chats you already had stay readable under this name. A new message in one of them is refused.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            Keep
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            data-testid="assistant-delete-confirm"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete assistant"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
