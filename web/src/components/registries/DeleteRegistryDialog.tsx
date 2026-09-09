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
import type { Registry } from "../../gen/podium/v1/registry_pb";
import { errorMessage, registries } from "../../lib/client";

/**
 * Deleting a login does not fail anything at admission: the next pull from the host simply
 * goes out anonymous, and fails on the node if the registry is private. That is quieter than a
 * secret's failure mode, so the operator types the host back before it happens.
 */
export function DeleteRegistryDialog({
  registry,
  open,
  onOpenChange,
}: {
  registry: Registry;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [typed, setTyped] = useState("");

  const remove = useMutation({
    mutationFn: () => registries.deleteRegistry({ host: registry.host }),
    onSuccess: () => {
      toast(`${registry.host} removed.`, "ok");
      void qc.invalidateQueries({ queryKey: ["registries"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`DeleteRegistry: ${errorMessage(err)}`),
  });

  const confirmed = typed.trim() === registry.host;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Remove the login for {registry.host}?</DialogTitle>
          <DialogDescription>
            There is no read API, so no copy of this password exists anywhere in Podium to
            restore from.
          </DialogDescription>
        </DialogHeader>

        <Alert variant="destructive" title="Pulls from this registry become anonymous">
          A task whose image lives here is still accepted, and fails on the node with{" "}
          <span className="font-mono">pull access denied</span> if the registry is private. Tasks
          already running keep the login they were assigned.
        </Alert>

        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (confirmed) remove.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="delete-registry-confirm">
              Type <span className="font-mono text-fg">{registry.host}</span> to confirm
            </Label>
            <Input
              id="delete-registry-confirm"
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
              {remove.isPending ? "Removing…" : "Remove registry"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
