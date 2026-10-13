import { RotateCw } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { agent, errorMessage } from "../../lib/client";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";

/**
 * ReloadProfileDirButton re-reads the conductor's profile directory into the running process:
 * the install seed, every `playbooks/<name>.yaml`, and the prompt files they name.
 *
 * A saved assistant is not replaced by that re-read. Playbook files are. The directory is
 * read once, at start, so an operator editing a playbook over SSH presses this instead of
 * restarting.
 *
 * It is a button and not a timer on purpose: a directory halfway through being saved does
 * not load, and only the person editing it knows when they have finished. A directory that
 * does not load is refused and changes nothing, so pressing this cannot break a bot that is
 * answering.
 */
export function ReloadProfileDirButton() {
  const viewer = useViewer();
  const qc = useQueryClient();
  const toast = useToast();
  const reload = useMutation({
    mutationFn: () => agent.reloadProfileDir({}),
    onSuccess: async () => {
      toast("The profile directory was reloaded. It applies to the next turn.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });
  if (!canManageInfra(viewer)) return null;

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      data-testid="reload-profile-dir"
      disabled={reload.isPending}
      onClick={() => reload.mutate()}
    >
      <RotateCw />
      {reload.isPending ? "Reloading…" : "Reload"}
    </Button>
  );
}
