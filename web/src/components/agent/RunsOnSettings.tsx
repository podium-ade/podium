import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { PlaybookDefinition } from "../../gen/podium/agent/v1/agent_pb";
import type { SandboxProvider } from "../../gen/podium/v1/sandbox_pb";
import { agent, errorMessage, sandboxProviders } from "../../lib/client";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";
import { RUNS_ON_NODES, SANDBOX_PROVIDERS, capacityText } from "../../lib/sandboxProviders";
import { cn } from "../../lib/utils";
import { Badge } from "../Badge";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";

type Choice = { id: string; name: string; detail: string; status?: string; configured: boolean };

/**
 * RunsOnSettings is where a session's turns run when their playbook does not say: this
 * fleet's own nodes, or a sandbox provider set up on the Sandboxes screen. The providers
 * themselves are configured there and only chosen here, so a key or a cap has one home.
 */
export function RunsOnSettings() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const qc = useQueryClient();
  const toast = useToast();

  const saved = useQuery({ queryKey: ["agent", "runsOn"], queryFn: () => agent.getRunsOn({}) });
  const providers = useQuery({
    queryKey: ["sandboxProviders"],
    queryFn: () => sandboxProviders.listSandboxProviders({}),
    retry: false,
  });
  const profile = useQuery({ queryKey: ["agent", "profile"], queryFn: () => agent.getProfile({}) });

  const current = saved.data?.runsOn?.runsOn || RUNS_ON_NODES;
  // Undefined until somebody picks one, so the saved choice shows once it loads.
  const [picked, setChoice] = useState<string>();
  const choice = picked ?? current;

  const save = useMutation({
    mutationFn: (runsOn: string) => agent.setRunsOn({ runsOn }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["agent", "runsOn"] });
      setChoice(undefined);
      toast("Saved. Turns that start from now run there.");
    },
    onError: (err) => toast(`SetRunsOn: ${errorMessage(err)}`),
  });

  const configured = providers.data?.providers ?? [];
  const choices: Choice[] = [
    {
      id: RUNS_ON_NODES,
      name: "Your nodes",
      detail: "Turns run on the podium-node workers enrolled with this control plane.",
      configured: true,
    },
    ...configured.map((p) => providerChoice(p)),
  ];
  if (!choices.some((c) => c.id === current)) {
    choices.push({
      id: current,
      name: SANDBOX_PROVIDERS.find((p) => p.name === current)?.label ?? current,
      detail: "Saved here, but not set up on Sandboxes: turns wait in the queue until it is.",
      configured: false,
    });
  }

  const { dind, browser } = stranded(profile.data?.playbooks ?? [], choice, configured);

  return (
    <div className="grid gap-4">
      {saved.error ? <Alert variant="destructive">{errorMessage(saved.error)}</Alert> : null}
      <div role="radiogroup" aria-label="Runs on" className="grid gap-2">
        {choices.map((c) => {
          const selected = c.id === choice;
          return (
            <label
              key={c.id}
              data-testid={`runs-on-${c.id}`}
              className={cn(
                "grid grid-cols-[20px_minmax(0,1fr)] items-start gap-3 rounded-md border border-border bg-panel p-4",
                selected && "border-fg bg-raised",
                manage ? "cursor-pointer" : "cursor-not-allowed",
              )}
            >
              <input
                type="radio"
                name="runs-on"
                value={c.id}
                checked={selected}
                disabled={!manage}
                onChange={() => setChoice(c.id)}
                className="mt-0.5 size-[18px] accent-accent disabled:cursor-not-allowed"
              />
              <span className="grid min-w-0 gap-1">
                <strong className="flex flex-wrap items-center gap-2 text-sm">
                  {c.name}
                  {c.id === current ? (
                    <Badge tone="ok" dot={false}>
                      In use
                    </Badge>
                  ) : null}
                  {!c.configured ? (
                    <Badge tone="warn" dot={false}>
                      Not set up
                    </Badge>
                  ) : null}
                </strong>
                <span className="text-sm text-soft">{c.detail}</span>
                {c.status ? <span className="text-xs text-muted">{c.status}</span> : null}
              </span>
            </label>
          );
        })}
      </div>

      {dind.length > 0 ? (
        <Alert variant="warn" title="These playbooks cannot run in a sandbox">
          {dind.join(", ")} attach a Docker daemon, a privileged sidecar a sandbox does not run.
          Pin them to your nodes with Runs on.
        </Alert>
      ) : null}
      {browser.length > 0 ? (
        <Alert variant="warn" title="These playbooks need Docker, which Modal's gvisor runtime does not have">
          {browser.join(", ")} attach a browser sidecar. Set the Modal provider's runtime to vm on
          Sandboxes, set it on each playbook, or pin them to your nodes.
        </Alert>
      ) : null}

      <p className="text-sm text-soft">
        A playbook can pin its own with Runs on, and override a provider's machine type, template
        or runtime for its turns. Providers are set up on{" "}
        <Link to="/sandboxes" className="underline">
          Sandboxes
        </Link>
        {providers.isError ? " (which could not be read just now)" : ""}.
      </p>

      {manage ? (
        <div>
          <Button disabled={choice === current || save.isPending} onClick={() => save.mutate(choice)}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function providerChoice(p: SandboxProvider): Choice {
  const spec = SANDBOX_PROVIDERS.find((s) => s.name === p.name);
  return {
    id: p.name,
    name: spec?.label ?? p.name,
    detail: spec?.blurb ?? "A cloud sandbox per turn.",
    status: `Running ${capacityText(p)}`,
    configured: true,
  };
}

/**
 * stranded is the playbooks that would follow this choice into a sandbox that cannot run
 * their sidecars: a Docker daemon in any sandbox, since it is privileged, and a browser on
 * Modal without the vm runtime.
 */
function stranded(
  playbooks: PlaybookDefinition[],
  choice: string,
  providers: SandboxProvider[],
): { dind: string[]; browser: string[] } {
  if (choice === RUNS_ON_NODES) return { dind: [], browser: [] };
  const following = playbooks.filter((p) => !p.shadowed && !p.runsOn);
  const runtime = providers.find((p) => p.name === "modal")?.settings.runtime || "gvisor";
  return {
    dind: following.filter((p) => p.docker).map((p) => p.name),
    browser:
      choice === "modal"
        ? following
            .filter((p) => !p.docker && p.browser && (p.sandbox?.modal?.runtime || runtime) !== "vm")
            .map((p) => p.name)
        : [],
  };
}
