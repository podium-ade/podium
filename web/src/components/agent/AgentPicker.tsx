import { useId, useMemo, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";
import type { AgentBackend, AgentModel } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";
import { BackendMark } from "./BackendMark";

export type AgentPickerProps = {
  value: AgentChoice;
  onChange: (next: AgentChoice) => void;
  /** The catalogue from ListAgents. Empty while it loads. */
  agents: AgentBackend[];
  /**
   * What an empty choice means here, and what to call it. A playbook inherits the profile's;
   * the profile falls back to profile.yaml's. Undefined removes the option entirely.
   */
  inherit?: { label: string; hint?: string };
  /** The effective triple when this one is empty, shown on the inherit row. */
  inherited?: AgentChoice;
  disabled?: boolean;
  loading?: boolean;
  /** Prefix for the aria-labels, so two pickers on one screen are distinguishable. */
  label: string;
  /**
   * Skip the trigger and render the searchable list in-flow. The chat composer uses this
   * inside its own popover so picking a model is one click, not a menu inside a menu —
   * nested absolute lists were what painted the catalogue off the bottom of the window.
   */
  embedded?: boolean;
};

/**
 * AgentPicker is one control for the three things that decide what a turn actually runs:
 * which backend, which model, and how hard it thinks.
 *
 * They are one control because they are one decision. Picking a model picks its backend —
 * grok-4.6 only runs on Grok — so a separate agent dropdown would only ever be a way to put
 * the two into a state that cannot run. The effort strip sits below and re-renders per
 * model, because the levels are a property of the model: Grok has no `max`, and Grok 4.5
 * treats `xhigh` as `high`, so it is not offered one.
 *
 * A model id that is not in the catalogue is a first-class value, not an error. Providers
 * ship models faster than this binary is rebuilt, and "Use another model id" is what stops
 * Podium being the reason a new one cannot be used.
 */
export function AgentPicker({
  value,
  onChange,
  agents,
  inherit,
  inherited,
  disabled,
  loading,
  label,
  embedded,
}: AgentPickerProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [custom, setCustom] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const listID = useId();

  const rows = useMemo(() => buildRows(agents, inherit !== undefined, query), [agents, inherit, query]);
  const chosen = useMemo(() => findModel(agents, value.model), [agents, value.model]);
  const backend = useMemo(
    () => agents.find((a) => a.id === (value.agent || chosen?.backend.id)),
    [agents, value.agent, chosen],
  );
  // A model the catalogue knows brings its own levels. One typed by hand has none to bring,
  // so the backend's own set stands in — otherwise choosing a model released since this
  // build would silently cost you the effort control, and the server does not check a level
  // against a model it has never heard of anyway.
  const efforts = chosen?.model.efforts ?? backendEfforts(backend);

  function onOpenChange(next: boolean) {
    setOpen(next);
    if (next) {
      setQuery("");
      setActive(0);
    }
  }

  function choose(row: Row) {
    if (row.kind === "inherit") {
      onChange(INHERIT);
    } else if (row.kind === "model") {
      // Picking a model picks its backend, and an effort the new model does not accept is
      // dropped rather than carried into a combination the server would refuse.
      const keep = row.model.efforts.includes(value.effort) ? value.effort : "";
      onChange({ agent: row.backend.id, model: row.model.id, effort: keep });
    } else {
      setCustom(true);
      setOpen(false);
      return;
    }
    setCustom(false);
    setOpen(false);
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Escape") {
      setOpen(false);
      return;
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => {
        const n = rows.length;
        if (n === 0) return 0;
        return (i + step + n) % n;
      });
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      const row = rows[active];
      if (row) choose(row);
    }
  }

  const summary = describe(value, chosen, backend, inherit, inherited);

  const menu = (
    <>
      <input
        ref={searchRef}
        value={query}
        aria-label={`${label}: search models`}
        placeholder="Search models…"
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
        }}
        autoFocus={embedded}
        className="w-full shrink-0 border-b border-hairline bg-transparent px-3 py-2 text-xs text-fg outline-none placeholder:text-faint"
      />
      <ul
        id={listID}
        role="listbox"
        aria-label={`${label}: models`}
        className={embedded ? "py-1" : "min-h-0 flex-1 overflow-y-auto py-1"}
      >
        {rows.length === 0 ? (
          <li className="px-3 py-2 text-xs text-muted">
            Nothing matches. Pick “Use another model id” to type one.
          </li>
        ) : null}
        {rows.map((row, i) => (
          <RowItem
            key={rowKey(row)}
            row={row}
            active={i === active}
            selected={isSelected(row, value)}
            inherited={inherited}
            onHover={() => setActive(i)}
            onPick={() => choose(row)}
          />
        ))}
      </ul>
    </>
  );

  return (
    <div className="w-full space-y-2">
      {embedded ? (
        <div onKeyDown={onKeyDown}>{menu}</div>
      ) : (
        <Popover open={open} onOpenChange={onOpenChange} modal={false}>
          <PopoverTrigger asChild>
            <button
              type="button"
              disabled={disabled || loading}
              aria-haspopup="listbox"
              aria-expanded={open}
              aria-label={`${label}: agent and model`}
              data-testid="agent-picker-trigger"
              className="flex w-full items-center gap-2.5 rounded-md border border-border bg-bg px-2.5 py-2 text-left text-xs shadow-xs transition-[border-color,box-shadow] duration-150 outline-none hover:border-muted/45 focus-visible:border-accent/60 focus-visible:ring-2 focus-visible:ring-ring/35 disabled:cursor-not-allowed disabled:opacity-50"
            >
              <BackendMark id={summary.markID} />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-fg">{summary.title}</span>
                <span className="block truncate text-2xs text-muted">{summary.sub}</span>
              </span>
              {value.effort ? (
                <span className="rounded bg-raised px-1.5 py-0.5 font-mono text-2xs text-muted">
                  {value.effort}
                </span>
              ) : null}
              <ChevronDown aria-hidden className="size-3.5 shrink-0 text-muted" />
            </button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            side="bottom"
            onOpenAutoFocus={(e) => {
              e.preventDefault();
              searchRef.current?.focus();
            }}
            onKeyDown={onKeyDown}
            className="flex w-[var(--radix-popover-trigger-width)] min-w-72 flex-col overflow-hidden p-0"
          >
            {menu}
          </PopoverContent>
        </Popover>
      )}

      {custom || (value.model !== "" && !chosen) ? (
        <CustomModel
          label={label}
          agents={agents}
          value={value}
          onChange={onChange}
          onDone={() => setCustom(false)}
        />
      ) : null}

      {efforts.length > 0 ? (
        <EffortStrip
          label={label}
          efforts={efforts}
          value={value.effort}
          onChange={(effort) => onChange({ ...value, effort })}
          disabled={disabled}
        />
      ) : null}

      {backend && !backend.ready ? (
        <Alert variant="warn" data-testid="agent-picker-unready">
          No {backend.provider === "xai" ? "xAI" : "Anthropic"} credential is stored, so a turn
          on {backend.displayName} will fail. Set one in Settings — this choice is
          saved either way.
        </Alert>
      ) : null}
    </div>
  );
}

/** Row is one line of the popover. */
type Row =
  | { kind: "inherit" }
  | { kind: "model"; backend: AgentBackend; model: AgentModel; first: boolean }
  | { kind: "custom" };

function rowKey(row: Row): string {
  switch (row.kind) {
    case "inherit":
      return "inherit";
    case "custom":
      return "custom";
    default:
      return `${row.backend.id}/${row.model.id}`;
  }
}

/**
 * buildRows flattens the catalogue into the list the popover renders, filtered by the
 * search. `first` marks the row that carries its backend's header, so the grouping survives
 * filtering: a search that matches one Grok model still shows it under GROK.
 */
function buildRows(agents: AgentBackend[], withInherit: boolean, query: string): Row[] {
  const q = query.trim().toLowerCase();
  const match = (b: AgentBackend, m: AgentModel) =>
    q === "" ||
    m.id.toLowerCase().includes(q) ||
    m.displayName.toLowerCase().includes(q) ||
    b.displayName.toLowerCase().includes(q);

  const rows: Row[] = [];
  if (withInherit && q === "") rows.push({ kind: "inherit" });
  for (const b of agents) {
    let first = true;
    for (const m of b.models) {
      if (!match(b, m)) continue;
      rows.push({ kind: "model", backend: b, model: m, first });
      first = false;
    }
  }
  rows.push({ kind: "custom" });
  return rows;
}

function findModel(
  agents: AgentBackend[],
  id: string,
): { backend: AgentBackend; model: AgentModel } | undefined {
  if (id === "") return undefined;
  for (const b of agents) {
    const m = b.models.find((x) => x.id === id);
    if (m) return { backend: b, model: m };
  }
  return undefined;
}

/** backendEfforts is every level any of a backend's models accepts, weakest first. */
function backendEfforts(backend?: AgentBackend): string[] {
  if (!backend) return [];
  const out: string[] = [];
  for (const m of backend.models) {
    for (const e of m.efforts) {
      if (!out.includes(e)) out.push(e);
    }
  }
  return out;
}

function isSelected(row: Row, value: AgentChoice): boolean {
  if (row.kind === "inherit") return value.model === "" && value.agent === "";
  if (row.kind === "model") return value.model === row.model.id;
  return false;
}

/** describe is the two lines on the closed trigger. */
function describe(
  value: AgentChoice,
  chosen: { backend: AgentBackend; model: AgentModel } | undefined,
  backend: AgentBackend | undefined,
  inherit: { label: string; hint?: string } | undefined,
  inherited: AgentChoice | undefined,
): { title: string; sub: string; markID: string } {
  if (value.model === "" && value.agent === "") {
    const eff = inherited?.effort ? ` · ${inherited.effort}` : "";
    return {
      title: inherit?.label ?? "—",
      sub: inherited?.model ? `${inherited.model}${eff}` : (inherit?.hint ?? ""),
      markID: inherited?.agent ?? "",
    };
  }
  if (chosen) {
    return {
      title: chosen.model.id,
      sub: `${chosen.backend.displayName} · ${chosen.model.displayName}`,
      markID: chosen.backend.id,
    };
  }
  return {
    title: value.model || "(no model)",
    sub: backend ? `${backend.displayName} · not in the catalogue` : "a model id typed by hand",
    markID: value.agent,
  };
}

function RowItem({
  row,
  active,
  selected,
  inherited,
  onHover,
  onPick,
}: {
  row: Row;
  active: boolean;
  selected: boolean;
  inherited?: AgentChoice;
  onHover: () => void;
  onPick: () => void;
}) {
  const base = `flex w-full items-start gap-2 px-3 py-1.5 text-left text-xs transition-colors ${
    active ? "bg-raised" : ""
  }`;

  if (row.kind === "inherit") {
    return (
      <li role="option" aria-selected={selected}>
        <button type="button" className={base} onMouseEnter={onHover} onClick={onPick}>
          <Tick shown={selected} />
          <span className="min-w-0 flex-1">
            <span className="block text-fg">Inherit</span>
            <span className="block truncate text-muted">
              {inherited?.model ? `currently ${inherited.model}` : "whatever the level above says"}
            </span>
          </span>
        </button>
      </li>
    );
  }

  if (row.kind === "custom") {
    return (
      <li role="option" aria-selected={false}>
        <button
          type="button"
          data-testid="agent-picker-custom"
          className={`${base} border-t border-hairline`}
          onMouseEnter={onHover}
          onClick={onPick}
        >
          <Tick shown={false} />
          <span className="min-w-0 flex-1">
            <span className="block text-fg">Use another model id…</span>
            <span className="block text-muted">
              For a model released since this build. It is not checked here.
            </span>
          </span>
        </button>
      </li>
    );
  }

  return (
    <>
      {row.first ? (
        <li
          aria-hidden="true"
          className="flex items-center gap-2 px-3 pt-2 pb-1 text-2xs font-semibold tracking-wide text-faint uppercase"
        >
          <BackendMark id={row.backend.id} />
          {row.backend.displayName}
          {row.backend.ready ? (
            <span className="ml-auto font-normal text-ok normal-case">credential set</span>
          ) : (
            <span className="ml-auto font-normal text-warn normal-case">no credential</span>
          )}
        </li>
      ) : null}
      <li role="option" aria-selected={selected}>
        <button type="button" className={base} onMouseEnter={onHover} onClick={onPick}>
          <Tick shown={selected} />
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-baseline gap-x-2">
              <span className="font-mono text-fg">{row.model.id}</span>
              {row.model.contextTokens > 0 ? (
                <span className="tabular text-2xs text-faint">{tokens(row.model.contextTokens)}</span>
              ) : null}
            </span>
            <span className="block text-muted">{row.model.note}</span>
          </span>
        </button>
      </li>
    </>
  );
}

/**
 * EffortStrip is the segmented control. "Auto" is first and is not a level: it is the
 * absence of one, which leaves the choice to the provider's own default.
 */
function EffortStrip({
  label,
  efforts,
  value,
  onChange,
  disabled,
}: {
  label: string;
  efforts: string[];
  value: string;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  const options = ["", ...efforts];
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-2xs font-medium tracking-wide text-faint uppercase">Effort</span>
      <div
        role="radiogroup"
        aria-label={`${label}: reasoning effort`}
        data-testid="effort-strip"
        className="inline-flex w-fit items-center gap-0.5 rounded-lg border border-border bg-panel p-0.5"
      >
        {options.map((e) => (
          <button
            key={e || "auto"}
            type="button"
            role="radio"
            aria-checked={value === e}
            aria-label={e || "auto"}
            disabled={disabled}
            onClick={() => onChange(e)}
            className={`h-6 rounded-md px-2 text-2xs font-medium capitalize transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50 ${
              value === e ? "bg-raised text-fg" : "text-muted hover:text-fg"
            }`}
          >
            {e || "auto"}
          </button>
        ))}
      </div>
    </div>
  );
}

/** CustomModel is the escape hatch: a model id typed by hand, on a backend chosen by hand. */
function CustomModel({
  label,
  agents,
  value,
  onChange,
  onDone,
}: {
  label: string;
  agents: AgentBackend[];
  value: AgentChoice;
  onChange: (v: AgentChoice) => void;
  onDone: () => void;
}) {
  const id = useId();
  return (
    <div className="flex flex-wrap items-end gap-2 rounded-lg border border-border bg-raised/50 p-2">
      <div className="flex flex-col gap-1">
        <Label htmlFor={`${id}-backend`}>Backend</Label>
        <select
          id={`${id}-backend`}
          aria-label={`${label}: backend`}
          value={value.agent || agents[0]?.id || ""}
          onChange={(e) => onChange({ ...value, agent: e.target.value })}
          className="h-8 rounded-md border border-input bg-bg px-2 font-mono text-xs text-fg outline-none focus-visible:border-accent/60 focus-visible:ring-2 focus-visible:ring-ring/35"
        >
          {agents.map((a) => (
            <option key={a.id} value={a.id}>
              {a.displayName}
            </option>
          ))}
        </select>
      </div>
      <div className="flex min-w-40 flex-1 flex-col gap-1">
        <Label htmlFor={`${id}-model`}>Model id</Label>
        <Input
          id={`${id}-model`}
          aria-label={`${label}: model id`}
          value={value.model}
          autoFocus
          placeholder="grok-5"
          onChange={(e) =>
            onChange({
              agent: value.agent || agents[0]?.id || "",
              model: e.target.value,
              effort: value.effort,
            })
          }
          className="h-8 font-mono text-xs"
        />
      </div>
      <Button type="button" variant="outline" size="sm" onClick={onDone}>
        Done
      </Button>
    </div>
  );
}

function Tick({ shown }: { shown: boolean }) {
  return (
    <Check
      aria-hidden="true"
      className={`mt-0.5 size-3 shrink-0 text-accent ${shown ? "" : "opacity-0"}`}
    />
  );
}

/** tokens renders a context window the way the provider's own docs do. */
function tokens(n: number): string {
  if (n >= 1_000_000) return `${n / 1_000_000}M ctx`;
  return `${Math.round(n / 1000)}K ctx`;
}
