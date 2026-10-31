import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SandboxProvider } from "../../gen/podium/v1/sandbox_pb";
import { agent, errorMessage, sandboxProviders } from "../../lib/client";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";
import { RUNS_ON_HOST, RUNS_ON_NODES, SANDBOX_PROVIDERS, capacityText } from "../../lib/sandboxProviders";
import { cn } from "../../lib/utils";
import { Badge } from "../Badge";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";

type Choice = { id: string; name: string; detail: string; status?: string; available: boolean };

/**
 * AssistantRunsOnSettings is where the assistant itself runs: the conductor's own host, the
 * fleet's nodes, or a sandbox provider. It is a choice of its own, apart from where playbooks
 * run, because the assistant is what a person waits on: the host answers fastest, and a
 * sandbox gives every conversation one of its own and scales past one machine.
 */
export function AssistantRunsOnSettings() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const qc = useQueryClient();
  const toast = useToast();

  const placement = useQuery({
    queryKey: ["agent", "assistantRunsOn"],
    queryFn: () => agent.getAssistantRunsOn({}),
  });
  const providers = useQuery({
    queryKey: ["sandboxProviders"],
    queryFn: () => sandboxProviders.listSandboxProviders({}),
    retry: false,
  });

  const p = placement.data?.placement;
  const saved = p?.saved?.runsOn ?? "";
  const effective = p?.effective ?? "";
  const fallback = p?.default ?? "";
  const hostAvailable = p?.hostAvailable ?? false;
  // Undefined until somebody picks one, so the saved choice shows once it loads.
  const [picked, setChoice] = useState<string>();
  const choice = picked ?? (saved || effective);

  const save = useMutation({
    mutationFn: (runsOn: string) => agent.setAssistantRunsOn({ runsOn }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["agent", "assistantRunsOn"] });
      setChoice(undefined);
      toast("Saved. Conversations that start a turn from now are answered there.");
    },
    onError: (err) => toast(`SetAssistantRunsOn: ${errorMessage(err)}`),
  });

  const configured = providers.data?.providers ?? [];
  const choices: Choice[] = [
    {
      id: RUNS_ON_HOST,
      name: "This conductor",
      detail: "Answers in the conductor's own process. Fastest to the first word, and bounded by this one machine.",
      available: hostAvailable,
    },
    {
      id: RUNS_ON_NODES,
      name: "Your nodes",
      detail: "Each turn runs in a small task on your podium-node workers, kept warm between messages.",
      available: true,
    },
    ...configured.map((c) => providerChoice(c)),
  ];
  if (choice && !choices.some((c) => c.id === choice)) {
    choices.push({
      id: choice,
      name: SANDBOX_PROVIDERS.find((s) => s.name === choice)?.label ?? choice,
      detail: "Saved here, but not set up on Sandboxes: conversations wait in the queue until it is.",
      available: false,
    });
  }

  return (
    <div className="grid gap-4" data-testid="assistant-runs-on">
      <div className="grid gap-1">
        <h2 className="text-sm font-semibold">Where the assistant runs</h2>
        <p className="text-sm text-soft">
          The assistant answers every conversation and hands real work to playbooks. Each conversation it runs off
          this conductor gets a sandbox of its own, with no repository and nothing kept between turns.
        </p>
      </div>
      {placement.error ? <Alert variant="destructive">{errorMessage(placement.error)}</Alert> : null}
      {p && !effective ? (
        <Alert variant="warn" title="No assistant">
          This conductor has no host runtime and nowhere else to run the assistant, so conversations are refused.
          Pick your nodes or a sandbox provider.
        </Alert>
      ) : null}
      {saved && effective && saved !== effective ? (
        <Alert variant="warn" title="The saved choice is not in use">
          {saved} cannot be honoured here, so the assistant runs on {effective}.
        </Alert>
      ) : null}
      <div role="radiogroup" aria-label="Assistant runs on" className="grid gap-2">
        {choices.map((c) => {
          const selected = c.id === choice;
          const disabled = !manage || (!c.available && c.id === RUNS_ON_HOST);
          return (
            <label
              key={c.id}
              data-testid={`assistant-runs-on-${c.id}`}
              className={cn(
                "grid grid-cols-[20px_minmax(0,1fr)] items-start gap-3 rounded-md border border-border bg-panel p-4",
                selected && "border-fg bg-raised",
                disabled ? "cursor-not-allowed" : "cursor-pointer",
              )}
            >
              <input
                type="radio"
                name="assistant-runs-on"
                value={c.id}
                checked={selected}
                disabled={disabled}
                onChange={() => setChoice(c.id)}
                className="mt-0.5 size-[18px] accent-accent disabled:cursor-not-allowed"
              />
              <span className="grid min-w-0 gap-1">
                <strong className="flex flex-wrap items-center gap-2 text-sm">
                  {c.name}
                  {c.id === effective ? (
                    <Badge tone="ok" dot={false}>
                      In use
                    </Badge>
                  ) : null}
                  {c.id === fallback && !saved ? (
                    <Badge tone="idle" dot={false}>
                      Default
                    </Badge>
                  ) : null}
                  {!c.available ? (
                    <Badge tone="warn" dot={false}>
                      {c.id === RUNS_ON_HOST ? "No host runtime" : "Not set up"}
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

      {manage ? (
        <div className="flex flex-wrap gap-2">
          <Button
            aria-label="Save where the assistant runs"
            disabled={!choice || choice === (saved || effective) || save.isPending}
            onClick={() => save.mutate(choice)}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
          {saved ? (
            <Button variant="ghost" disabled={save.isPending} onClick={() => save.mutate("")}>
              Use the default{fallback ? ` (${fallback})` : ""}
            </Button>
          ) : null}
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
    detail: "A sandbox per conversation, kept warm between messages. Scales with the provider, not this machine.",
    status: `Running ${capacityText(p)}`,
    available: true,
  };
}
