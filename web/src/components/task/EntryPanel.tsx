import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Braces, Link2, ListRestart, Minus, Plus, X } from "lucide-react";
import { pretty, toolArgs } from "../../lib/activity";
import { entryJson, fieldsOf, linkTo, type Entry, type Field } from "../../lib/entries";
import { tokenize } from "../../lib/entryQuery";
import { absolute, durationMs } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { SourceIcon } from "./LogRow";

type Tab = "output" | "input" | "content" | "fields" | "json";

/** title is what the panel calls the entry it shows. */
function titleOf(e: Entry): string {
  if (e.activity?.kind === "tool") return e.activity.tool;
  if (e.activity?.kind === "reasoning") return "reasoning";
  if (e.message) return e.message.type || "message";
  if (e.line) return e.source;
  return e.kind;
}

function Status({ entry }: { entry: Entry }) {
  const a = entry.activity;
  const failed = a?.kind === "tool" ? a.status === "error" : entry.severity === "error";
  const text = a?.kind === "tool" ? (failed ? "failed" : "completed") : entry.severity === "info" ? "" : entry.severity;
  if (!text) return null;
  return (
    <span
      className={cn(
        "rounded-sm px-1.5 text-[11px] leading-[18px] font-medium",
        failed ? "bg-err-bg text-err" : entry.severity === "warn" ? "bg-warn-bg text-warn" : "bg-ok-bg text-ok",
      )}
    >
      {text}
    </span>
  );
}

/**
 * EntryPanel is the record of the selected row, beside the list: who said it and when on
 * top, what it said below, in tabs — a tool call's output and input, any other entry's
 * text — with its fields a click from a filter and the raw record one tab over.
 */
export function EntryPanel({
  entry,
  previous,
  query,
  filtered,
  bandSize,
  onTerm,
  onSurrounding,
  onClearBand,
  onClose,
}: {
  entry: Entry;
  /** previous is the entry before it in the run, for how long after it this one came. */
  previous?: Entry;
  query: string;
  filtered: boolean;
  bandSize?: number;
  /** onTerm adds a query term, or takes it out again when it is already in. */
  onTerm: (term: string) => void;
  onSurrounding: () => void;
  onClearBand: () => void;
  onClose: () => void;
}) {
  const isTool = entry.activity?.kind === "tool";
  const tabs: Tab[] = isTool ? ["output", "input", "fields", "json"] : ["content", "fields", "json"];
  // The tab is kept as the selection moves, so stepping through tool calls stays on their
  // output; an entry that has no such tab opens on its first.
  const [chosen, setChosen] = useState<Tab>(tabs[0]);
  const tab = tabs.includes(chosen) ? chosen : tabs[0];

  const [copied, setCopied] = useState<string>();
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(undefined), 1600);
    return () => clearTimeout(id);
  }, [copied]);
  const json = useMemo(() => JSON.stringify(entryJson(entry), null, 2), [entry]);
  const copy = (what: string, text: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(what);
  };
  const after = previous && previous.at && entry.at ? entry.at - previous.at : undefined;

  return (
    <aside
      aria-label="Entry details"
      data-testid="entry-detail"
      className="flex max-h-[34rem] min-h-0 min-w-0 flex-col border-t border-hairline bg-panel font-sans lg:max-h-none lg:border-t-0"
    >
      <header className="border-b border-hairline px-4 pt-3 pb-2.5">
        <div className="flex items-center gap-2">
          <SourceIcon source={entry.source} />
          <h3 className="min-w-0 truncate font-mono text-sm font-medium text-fg">{titleOf(entry)}</h3>
          <Status entry={entry} />
          <span className="ml-auto flex items-center">
            <IconAction label={copied === "link" ? "Copied" : "Copy link"} onClick={() => copy("link", linkTo(entry.key, query))}>
              <Link2 />
            </IconAction>
            <IconAction label={copied === "json" ? "Copied" : "Copy JSON"} onClick={() => copy("json", json)}>
              <Braces />
            </IconAction>
            <IconAction label="Close" onClick={onClose}>
              <X />
            </IconAction>
          </span>
        </div>
        <p className="tabular mt-1 flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
          <span>{absolute(entry.ts)}</span>
          <span className="text-faint">·</span>
          <span className="font-mono text-2xs">seq {String(entry.seq)}</span>
          {after !== undefined && after >= 0 ? (
            <>
              <span className="text-faint">·</span>
              <span title="Since the entry before it">+{durationMs(after)}</span>
            </>
          ) : null}
          {filtered ? (
            <Button variant="ghost" size="xs" className="-my-1 ml-auto" onClick={onSurrounding}>
              <ListRestart />
              View in context
            </Button>
          ) : null}
        </p>
        {bandSize ? (
          <p className="mt-2 rounded-sm bg-run-bg px-2 py-1 text-xs text-run">
            {`${bandSize} ${bandSize === 1 ? "line" : "lines"} in the list were printed while this ran. `}
            <button type="button" onClick={onClearBand} className="underline underline-offset-2">
              Clear
            </button>
          </p>
        ) : null}
      </header>

      <Tabs value={tab} onValueChange={(v) => setChosen(v as Tab)} className="min-h-0 flex-1">
        <TabsList className="shrink-0 gap-0 border-hairline px-2">
          {tabs.map((t) => (
            <TabsTrigger key={t} value={t} className="min-h-9 px-3 text-xs capitalize">
              {t === "json" ? "JSON" : t}
            </TabsTrigger>
          ))}
        </TabsList>
        <div className="min-h-0 flex-1 overflow-auto px-4 py-3">
          {isTool ? (
            <>
              <TabsContent value="output" className="pt-0">
                <ToolOutput entry={entry} />
              </TabsContent>
              <TabsContent value="input" className="pt-0">
                <ToolInput entry={entry} />
              </TabsContent>
            </>
          ) : (
            <TabsContent value="content" className="pt-0">
              <Content entry={entry} />
            </TabsContent>
          )}
          <TabsContent value="fields" className="pt-0">
            <FieldTable entry={entry} query={query} onTerm={onTerm} />
          </TabsContent>
          <TabsContent value="json" className="pt-0">
            <Code>{json}</Code>
          </TabsContent>
        </div>
      </Tabs>
    </aside>
  );
}

function IconAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Button variant="ghost" size="icon-xs" aria-label={label} title={label} onClick={onClick}>
      {children}
    </Button>
  );
}

/** ToolOutput is what a call came back with, under the line that made it. */
function ToolOutput({ entry }: { entry: Entry }) {
  const a = entry.activity;
  if (a?.kind !== "tool") return null;
  const failed = a.status === "error";
  const out = failed ? a.error : a.output;
  const args = toolArgs(a);
  return (
    <div className="space-y-2.5">
      {args.primary ? (
        <p className="font-mono text-xs break-all text-muted">
          {a.tool === "bash" ? <span className="text-faint select-none">$ </span> : null}
          {args.primary.value}
        </p>
      ) : null}
      {out ? <Code error={failed}>{out}</Code> : <p className="text-xs text-faint italic">No output.</p>}
    </div>
  );
}

/** ToolInput is every argument of a call, the one it is summarised by first. */
function ToolInput({ entry }: { entry: Entry }) {
  const a = entry.activity;
  if (a?.kind !== "tool") return null;
  const args = toolArgs(a);
  if (args.raw) return <Code>{pretty(args.raw)}</Code>;
  const rows: [string, string][] = [...(args.primary ? [[args.primary.name, args.primary.value] as [string, string]] : []), ...args.rest];
  if (rows.length === 0) return <p className="text-xs text-faint italic">No arguments.</p>;
  return (
    <dl className="divide-y divide-hairline rounded-md border border-hairline">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 px-3 py-2">
          <dt className="font-mono text-2xs leading-5 text-faint">{k}</dt>
          <dd className="font-mono text-xs leading-5 break-words whitespace-pre-wrap text-fg">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Content({ entry }: { entry: Entry }) {
  const a = entry.activity;
  const text = a?.kind === "reasoning" ? a.text : entry.message?.text;
  if (text !== undefined) {
    return <p className="text-sm leading-6 break-words whitespace-pre-wrap text-fg">{text}</p>;
  }
  if (entry.line) return <Code>{entry.line.text}</Code>;
  return <p className="text-sm text-fg">{entry.summary}</p>;
}

/**
 * FieldTable is every field of the record, one per line, each with the two filters on it.
 * A filter already in the query shows as on, and clicking it again takes it out.
 */
function FieldTable({ entry, query, onTerm }: { entry: Entry; query: string; onTerm: (term: string) => void }) {
  const fields = useMemo(() => fieldsOf(entry), [entry]);
  const terms = useMemo(() => new Set(tokenize(query)), [query]);
  return (
    <dl className="divide-y divide-hairline rounded-md border border-hairline">
      {fields.map((f) => (
        <FieldRow key={f.path} field={f} terms={terms} onTerm={onTerm} />
      ))}
    </dl>
  );
}

function FieldRow({ field: f, terms, onTerm }: { field: Field; terms: ReadonlySet<string>; onTerm: (term: string) => void }) {
  const showing = f.term !== undefined && terms.has(f.term);
  const hiding = f.term !== undefined && terms.has(`-${f.term}`);
  return (
    <div className="group/field grid grid-cols-[9rem_minmax(0,1fr)_auto] items-start gap-3 px-3 py-1.5">
      <dt className="truncate font-mono text-2xs leading-5 text-faint" title={f.path}>
        {f.path}
      </dt>
      <dd className="font-mono text-xs leading-5 break-all text-fg">{f.value}</dd>
      {f.term ? (
        <span
          className={cn(
            "flex gap-0.5 transition-opacity",
            showing || hiding ? "opacity-100" : "opacity-0 group-hover/field:opacity-100 focus-within:opacity-100",
          )}
        >
          <FilterButton
            on={showing}
            label={`${showing ? "Stop showing only" : "Show only"} entries with ${f.path} ${f.value}`}
            onClick={() => onTerm(f.term!)}
          >
            <Plus />
          </FilterButton>
          <FilterButton
            on={hiding}
            label={`${hiding ? "Stop hiding" : "Hide"} entries with ${f.path} ${f.value}`}
            onClick={() => onTerm(`-${f.term!}`)}
          >
            <Minus />
          </FilterButton>
        </span>
      ) : (
        <span />
      )}
    </div>
  );
}

function FilterButton({ on, label, onClick, children }: { on: boolean; label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={on}
      title={label}
      onClick={onClick}
      className={cn(
        "grid size-5 place-items-center rounded-sm [&_svg]:size-3",
        on ? "bg-accent text-accent-ink" : "text-faint hover:bg-raised hover:text-fg",
      )}
    >
      {children}
    </button>
  );
}

function Code({ error, children }: { error?: boolean; children: ReactNode }) {
  return (
    <pre
      className={cn(
        "overflow-auto rounded-md border border-hairline bg-bg px-3 py-2 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap",
        error ? "text-err" : "text-fg",
      )}
    >
      {children}
    </pre>
  );
}
