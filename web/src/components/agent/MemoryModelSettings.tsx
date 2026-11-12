import { useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { MemorySettings } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { Badge } from "../Badge";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
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
  const listId = useId();
  const usable = settings.providers.filter((p) => p.available);
  const [provider, setProvider] = useState(
    settings.provider || (usable[0]?.provider ?? ""),
  );
  // null is "not typed": the field then shows the provider's default model, so Save works
  // without knowing a model id by heart.
  const [typed, setTyped] = useState<string | null>(settings.model || null);

  // The models the agent catalogue already knows for each provider, offered as suggestions.
  // Any id the provider accepts can still be typed.
  const agents = useQuery({
    queryKey: ["agent", "agents"],
    queryFn: () => agent.listAgents({}),
    staleTime: 60_000,
  });
  const catalogue = useMemo(() => {
    const out = new Map<string, { models: string[]; fallback: string }>();
    for (const b of agents.data?.agents ?? []) {
      const entry = out.get(b.provider) ?? { models: [], fallback: b.defaultModel };
      for (const m of b.models) if (!entry.models.includes(m.id)) entry.models.push(m.id);
      out.set(b.provider, entry);
    }
    return out;
  }, [agents.data]);

  const model = typed ?? catalogue.get(provider)?.fallback ?? "";

  const save = useMutation({
    mutationFn: () => agent.setMemorySettings({ provider, model: model.trim() }),
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

  const suggestions = catalogue.get(provider)?.models ?? [];

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
          <Label htmlFor={`${listId}-provider`}>Key</Label>
          <Select
            value={provider}
            onValueChange={(v) => {
              setProvider(v);
              setTyped(null);
              save.reset();
            }}
          >
            <SelectTrigger id={`${listId}-provider`} size="sm" data-testid="memory-model-provider">
              <SelectValue placeholder="Choose a key" />
            </SelectTrigger>
            <SelectContent>
              {settings.providers.map((p) => (
                <SelectItem key={p.provider} value={p.provider} disabled={!p.available} title={p.reason || undefined}>
                  {p.label}
                  {p.available ? "" : " (unavailable)"}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor={`${listId}-model`}>Model</Label>
          <Input
            id={`${listId}-model`}
            data-testid="memory-model-input"
            list={`${listId}-models`}
            value={model}
            onChange={(e) => {
              setTyped(e.target.value);
              save.reset();
            }}
            placeholder="Model id"
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
          <datalist id={`${listId}-models`}>
            {suggestions.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
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
          disabled={save.isPending || provider === "" || model.trim() === ""}
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
