import { useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { MemorySettings } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { Badge } from "../Badge";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Label } from "../ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";

const SETTINGS_KEY = ["agent", "memory-settings"] as const;

/**
 * MemoryModelSettings is which saved model key the memory service extracts facts with.
 *
 * Every retained turn is read by a model that decides what is worth remembering. That model,
 * and the key it spends, is one of the provider keys already saved under Models: nothing new
 * is pasted here. Members see what is in use; an admin or owner can change it.
 */
export function MemoryModelSettings({ manage }: { manage: boolean }) {
  const settings = useQuery({
    queryKey: SETTINGS_KEY,
    queryFn: async () => (await agent.getMemorySettings({})).settings,
  });
  const [editing, setEditing] = useState(false);

  const s = settings.data;
  // Opens by itself when nothing is chosen, because until something is, nothing new is
  // remembered at all.
  const unchosen = s !== undefined && s.proxyEnabled && s.provider === "";
  const open = manage && s?.proxyEnabled && (editing || unchosen);

  if (settings.isPending) return null;
  if (settings.isError || !s) {
    return (
      <section className="rounded-xl border border-border bg-card px-4 py-3 text-xs text-muted">
        Could not read the memory model: {errorMessage(settings.error)}
      </section>
    );
  }

  const label = s.providers.find((p) => p.provider === s.provider)?.label ?? s.provider;

  return (
    <section
      aria-labelledby="memory-model-title"
      data-testid="memory-model"
      className="space-y-3 rounded-xl border border-border bg-card px-4 py-3.5"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h2 id="memory-model-title" className="text-sm font-semibold text-fg">
            Memory model
          </h2>
          <p className="text-xs leading-relaxed text-muted">
            {!s.proxyEnabled ? (
              <>
                This host&apos;s memory service uses the key it was started with. To choose one of
                your saved model keys here, set <code className="font-mono">PODIUM_AGENT_MEMORY_LLM_TOKEN</code>{" "}
                and point the memory service at the conductor.
              </>
            ) : s.provider === "" ? (
              <>Nothing new is being remembered until a model is chosen.</>
            ) : (
              <>
                Reads each finished conversation and decides what to keep, using your saved{" "}
                {label} key.
              </>
            )}
          </p>
        </div>
        {s.proxyEnabled ? (
          s.provider === "" ? (
            <Badge tone="warn">Not chosen</Badge>
          ) : (
            <div className="flex items-center gap-2">
              <span className="text-xs text-fg">
                {label} · <span className="font-mono">{s.model}</span>
              </span>
              {manage && !open ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  data-testid="memory-model-change"
                  onClick={() => setEditing(true)}
                >
                  Change
                </Button>
              ) : null}
            </div>
          )
        ) : null}
      </div>

      {s.proxyEnabled && s.setBy && !open ? (
        <p className="text-2xs text-faint">
          Chosen by {s.setBy}
          {s.setAt ? (
            <>
              {" "}
              <span title={absolute(s.setAt)}>{relative(s.setAt)}</span>
            </>
          ) : null}
        </p>
      ) : null}

      {open ? (
        <MemoryModelForm
          settings={s}
          onDone={() => setEditing(false)}
          cancellable={!unchosen}
        />
      ) : null}
    </section>
  );
}

function MemoryModelForm({
  settings,
  onDone,
  cancellable,
}: {
  settings: MemorySettings;
  onDone: () => void;
  cancellable: boolean;
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const fieldId = useId();
  // Only keys that can be used are offered: the others have nothing to choose.
  const usable = settings.providers.filter((p) => p.available);
  const [provider, setProvider] = useState(
    settings.provider || (usable[0]?.provider ?? ""),
  );
  // "" is "not picked": the provider's default model stands in, so Save works at once.
  const [picked, setPicked] = useState(settings.model);

  // The models the agent catalogue knows for each provider, by name, as the chat's picker
  // shows them.
  const agents = useQuery({
    queryKey: ["agent", "agents"],
    queryFn: () => agent.listAgents({}),
    staleTime: 60_000,
  });
  const catalogue = useMemo(() => {
    const out = new Map<string, { models: { id: string; name: string }[]; fallback: string }>();
    for (const b of agents.data?.agents ?? []) {
      const entry = out.get(b.provider) ?? { models: [], fallback: b.defaultModel };
      for (const m of b.models) {
        if (!entry.models.some((x) => x.id === m.id)) {
          entry.models.push({ id: m.id, name: m.displayName || m.id });
        }
      }
      out.set(b.provider, entry);
    }
    return out;
  }, [agents.data]);

  const known = catalogue.get(provider);
  const model = picked || known?.fallback || "";
  // A saved model this build's catalogue does not list is still offered, so opening the
  // form never silently changes it.
  const options = [...(known?.models ?? [])];
  if (model && !options.some((m) => m.id === model)) options.unshift({ id: model, name: model });

  const save = useMutation({
    mutationFn: () => agent.setMemorySettings({ provider, model }),
    onSuccess: (res) => {
      qc.setQueryData(SETTINGS_KEY, res.settings);
      toast("Memory model saved", "ok");
      onDone();
    },
  });

  if (usable.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-raised px-3 py-2.5 text-xs leading-relaxed text-muted">
        No model key is saved for everyone yet.{" "}
        <Link to="/agent/settings/models" className="text-accent hover:underline">
          Add one under Models
        </Link>
        , then come back to choose it.
      </div>
    );
  }

  return (
    <form
      className="space-y-3 border-t border-hairline pt-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
        <div className="space-y-1.5">
          <Label htmlFor={`${fieldId}-provider`}>Key</Label>
          <Select
            value={provider}
            onValueChange={(v) => {
              setProvider(v);
              setPicked("");
              save.reset();
            }}
          >
            <SelectTrigger id={`${fieldId}-provider`} size="sm" data-testid="memory-model-provider">
              <SelectValue placeholder="Choose a key" />
            </SelectTrigger>
            <SelectContent>
              {usable.map((p) => (
                <SelectItem key={p.provider} value={p.provider}>
                  {p.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor={`${fieldId}-model`}>Model</Label>
          <Select
            value={model}
            onValueChange={(v) => {
              setPicked(v);
              save.reset();
            }}
            disabled={options.length === 0}
          >
            <SelectTrigger id={`${fieldId}-model`} size="sm" data-testid="memory-model-input">
              <SelectValue placeholder={agents.isPending ? "Loading models…" : "Choose a model"} />
            </SelectTrigger>
            <SelectContent>
              {options.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  {m.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <p className="text-2xs leading-relaxed text-faint">
        Extraction runs after every conversation, so a smaller, cheaper model is usually the
        right call. Saving sends one short test message with this key first.
      </p>
      {save.isError ? (
        <p role="alert" className="text-xs leading-relaxed text-err" data-testid="memory-model-error">
          {errorMessage(save.error)}
        </p>
      ) : null}
      <div className="flex items-center gap-2">
        <Button
          type="submit"
          size="sm"
          data-testid="memory-model-save"
          disabled={save.isPending || provider === "" || model === ""}
        >
          {save.isPending ? "Trying the model…" : "Save"}
        </Button>
        {cancellable ? (
          <Button type="button" variant="ghost" size="sm" onClick={onDone} disabled={save.isPending}>
            Cancel
          </Button>
        ) : null}
      </div>
    </form>
  );
}
