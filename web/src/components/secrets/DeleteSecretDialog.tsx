import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import type { Secret } from "../../gen/podium/v1/secret_pb";
import { errorMessage, secrets } from "../../lib/client";

/**
 * Delete is the one irreversible action on this screen, and the damage lands somewhere else:
 * a name is a contract with every task spec and agent config that references it, and nothing
 * on this page can tell you who that is. So the operator types the name back — the same
 * keystrokes they would have to get right to recreate it.
 */
export function DeleteSecretDialog({
  secret,
  open,
  onOpenChange,
}: {
  secret: Secret;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [typed, setTyped] = useState("");

  const remove = useMutation({
    mutationFn: () => secrets.deleteSecret({ name: secret.name }),
    onSuccess: () => {
      toast(`${secret.name} deleted.`, "ok");
      void qc.invalidateQueries({ queryKey: ["secrets"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`DeleteSecret: ${errorMessage(err)}`),
  });

  const confirmed = typed.trim() === secret.name;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete {secret.name}?</DialogTitle>
          <DialogDescription>
            Version {secret.version} and every earlier one go with it. There is no read API, so
            no copy of this value exists anywhere in Podium to restore from.
          </DialogDescription>
        </DialogHeader>

        <Alert variant="destructive" title="Anything that references this name starts failing">
          A task spec that lists it in its secrets, or an agent configured with it, is refused
          at admission with{" "}
          <span className="font-mono">missing secret &quot;{secret.name}&quot;</span>. Tasks
          already running keep the copy they were assigned and finish normally.
        </Alert>

        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (confirmed) remove.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="delete-confirm">
              Type <span className="font-mono text-fg">{secret.name}</span> to confirm
            </Label>
            <Input
              id="delete-confirm"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoFocus
              spellCheck={false}
              autoComplete="off"
              className="font-mono text-xs"
            />
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" size="sm">
                Cancel
              </Button>
            </DialogClose>
            <Button
              type="submit"
              variant="destructive"
              size="sm"
              disabled={!confirmed || remove.isPending}
            >
              {remove.isPending ? "Deleting…" : "Delete secret"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
