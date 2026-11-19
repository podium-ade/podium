import { useState, type ReactNode } from "react";
import type { GetUsageResponse } from "../../gen/podium/agent/v1/agent_pb";
import {
  billed,
  credentialLabel,
  groupTotals,
  isUnrecorded,
  personLabel,
  routineNames,
  totals,
  usd,
  type Totals,
} from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { ModelCell, PaidWith, PersonCell, SourceCell } from "./marks";

/** Slice is one row of a breakdown: what it is, drawn, and what it cost. */
type Slice = { key: string; name: string; cell: ReactNode; t: Totals };

type Dimension = { id: string; label: string; slices: (u: GetUsageResponse) => Slice[] };

/**
 * The questions worth asking of a bill: where the work came from, what model did it, whose it
 * was, and what paid. Each one is a grouping the server already did over the whole range, so
 * none of them is a guess from a capped page of rows.
 */
const DIMENSIONS: Dimension[] = [
  {
    id: "source",
    label: "Source",
    slices: (u) =>
      u.sources.map((g) => ({ key: g.key, name: g.key, cell: <SourceCell origin={g.key} />, t: totals(g.totals) })),
  },
  {
    id: "model",
    label: "Model",
    slices: (u) =>
      u.backends.map((b) => ({
        key: `${b.provider}/${b.agent}/${b.model}/${b.effort}`,
        name: isUnrecorded(b) ? "Unrecorded" : b.model,
        cell: <ModelCell provider={b.provider} model={b.model} effort={b.effort} agent={b.agent} />,
        t: groupTotals(b),
      })),
  },
  {
    id: "person",
    label: "Person",
    slices: (u) => {
      const names = routineNames(u.routines);
      return u.people.map((p) => ({
        key: p.startedBy || "-",
        name: personLabel(p.startedBy, names),
        cell: <PersonCell startedBy={p.startedBy} routines={names} />,
        t: groupTotals(p),
      }));
    },
  },
  {
    id: "paid",
    label: "Paid with",
    slices: (u) =>
      u.credentials.map((g) => ({
        key: g.key,
        name: credentialLabel(g),
        cell: <PaidWith authKind={g.authKind} owner={g.owner} account={g.account} provider={g.provider} />,
        t: totals(g.totals),
      })),
  },
];

/** How many rows the card shows before it summarises the rest. */
const SHOWN = 6;

/**
 * UsageBreakdown splits the range's spend along one dimension at a time. Each bar is the
 * group's whole cost, its billed part solid and its subscription-covered part lighter, the
 * same two tones the trend uses.
 */
export function UsageBreakdown({ usage, loading }: { usage: GetUsageResponse | undefined; loading?: boolean }) {
  const [dim, setDim] = useState(DIMENSIONS[0].id);
  const d = DIMENSIONS.find((x) => x.id === dim) ?? DIMENSIONS[0];
  const slices = usage ? [...d.slices(usage)].sort((a, b) => b.t.cost - a.t.cost || b.t.turns - a.t.turns) : [];
  const max = slices[0]?.t.cost ?? 0;

  return (
    <Card aria-label="Where it went" role="region" className="flex flex-col">
      <CardHeader className="pb-1">
        <div>
          <CardTitle>Where it went</CardTitle>
        </div>
        <ToggleGroup
          type="single"
          value={dim}
          onValueChange={(v) => v && setDim(v)}
          aria-label="Break spend down by"
        >
          {DIMENSIONS.map((x) => (
            <ToggleGroupItem key={x.id} value={x.id}>
              {x.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </CardHeader>

      <CardContent className={cn("flex-1 transition-opacity", loading && "opacity-60")}>
        {slices.length === 0 ? (
          <p className="py-8 text-center text-xs text-muted">
            Nothing ran in this range, so there is nothing to attribute.
          </p>
        ) : (
          <ol className="mt-3 space-y-3">
            {slices.slice(0, SHOWN).map((s) => (
              <Bar key={s.key} slice={s} max={max} />
            ))}
          </ol>
        )}
        {slices.length > SHOWN ? (
          <p className="mt-3 text-2xs text-faint">
            and {slices.length - SHOWN} more, each below {usd(slices[SHOWN - 1].t.cost)}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function Bar({ slice, max }: { slice: Slice; max: number }) {
  const { t } = slice;
  // Scaled to the largest group so the ranking reads at a glance. Against a max of zero
  // every bar is empty rather than full: nothing reported a cost.
  const width = max > 0 ? t.cost / max : 0;
  const paidShare = t.cost > 0 ? billed(t) / t.cost : 0;
  const tip =
    `${slice.name}: ${usd(billed(t))} billed` +
    (t.subscription > 0 ? `, ${usd(t.subscription)} covered by subscription` : "") +
    ` over ${t.turns} ${t.turns === 1 ? "turn" : "turns"}`;
  return (
    <li>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">{slice.cell}</div>
        <span className="shrink-0 text-xs tabular text-fg">
          {usd(t.cost)}
          <span className="ml-1.5 text-2xs text-faint">
            {t.turns} {t.turns === 1 ? "turn" : "turns"}
          </span>
        </span>
      </div>
      <div className="mt-1.5 flex h-1.5 overflow-hidden rounded-full bg-raised" role="img" aria-label={tip} title={tip}>
        <div className="flex h-full" style={{ width: `${Math.max(width * 100, width > 0 ? 2 : 0)}%` }}>
          <div className="h-full bg-accent" style={{ width: `${paidShare * 100}%` }} />
          <div className="h-full flex-1 bg-accent/35" />
        </div>
      </div>
    </li>
  );
}
