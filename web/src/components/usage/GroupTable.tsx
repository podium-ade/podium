import type { ReactNode } from "react";
import { billed, tokenBreakdown, tokens, usd, type Totals } from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { Tooltip } from "../ui/tooltip";
import { Clip } from "./marks";

/** Column widths, in rem: the first column's floor, each further group column, the share bar,
 * the five money and volume columns together (5.5 + 5.5 + 8.5 + 4.5 + 4.5), and the per-turn
 * average. Sized so the widest grouping (model, provider, agent) fits a laptop's width. */
const FIRST_MIN = 11;
const EXTRA = 6.5;
const SHARE = 10;
const NUMBERS = 28.5;
const AVG = 6.5;

/** GroupRow is one group of spend: how to draw its name, and its totals. */
export type GroupRow = {
  key: string;
  /** name is the group in words, for the share bar's label. */
  name: string;
  cells: ReactNode[];
  t: Totals;
  /** muted draws the share bar grey: a bucket that is not a real group, such as unrecorded. */
  muted?: boolean;
  onOpen?: () => void;
};

/**
 * GroupTable is spend grouped by one dimension: the group's own columns first, then the same
 * money and volume columns every grouping has, so the tabs read as one table turned different
 * ways. Rows come most expensive first.
 *
 * Billed and covered are separate columns because they are separate kinds of money: a
 * subscription's list-price figure is the work it did, not what it cost the company. The
 * share bar draws both, billed solid and covered lighter, against the largest row so the
 * ranking reads at a glance; the percentage beside it is the share of the whole.
 */
export function GroupTable({
  label,
  heads,
  rows,
  testId,
  perTurn,
}: {
  label: string;
  heads: ReactNode[];
  rows: GroupRow[];
  testId?: string;
  /** perTurn adds the average cost of one turn, which is what makes two models comparable. */
  perTurn?: boolean;
}) {
  const sorted = [...rows].sort((a, b) => b.t.cost - a.t.cost || b.t.turns - a.t.turns);
  const total = sorted.reduce((s, r) => s + r.t.cost, 0);
  const max = sorted[0]?.t.cost ?? 0;
  // The first column takes what the others leave, and never less than this; below the sum
  // the table scrolls sideways rather than squeezing it.
  const minWidth = `${FIRST_MIN + (heads.length - 1) * EXTRA + SHARE + NUMBERS + (perTurn ? AVG : 0)}rem`;

  return (
    <Table
      aria-label={label}
      stickyScrollbar
      style={{ minWidth }}
      className="table-fixed [&_td]:px-2.5 [&_th]:px-2.5"
    >
      {/* Set widths, never sized from the rows, so a filter or another range does not move
          the columns. The first column is the flexible one. */}
      <colgroup>
        {heads.map((_, i) => (
          <col key={i} style={i === 0 ? undefined : { width: `${EXTRA}rem` }} />
        ))}
        <col style={{ width: `${SHARE}rem` }} />
        <col className="w-22" />
        <col className="w-22" />
        <col className="w-34" />
        <col className="w-18" />
        <col className="w-18" />
        {perTurn ? <col style={{ width: `${AVG}rem` }} /> : null}
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          {heads.map((h, i) => (
            <TableHead key={i}>{h}</TableHead>
          ))}
          <TableHead>Share</TableHead>
          <TableHead className="text-right">Billed</TableHead>
          <TableHead className="text-right">Covered</TableHead>
          <TableHead className="text-right">Tokens in / out</TableHead>
          <TableHead className="text-right">Turns</TableHead>
          <TableHead className="text-right">Steps</TableHead>
          {perTurn ? <TableHead className="text-right">Avg / turn</TableHead> : null}
        </TableRow>
      </TableHeader>
      <TableBody>
        {sorted.map((r) => {
          const width = max > 0 ? r.t.cost / max : 0;
          const share = total > 0 ? r.t.cost / total : 0;
          const paidShare = r.t.cost > 0 ? billed(r.t) / r.t.cost : 0;
          const tip =
            `${r.name}: ${usd(r.t.cost)}, ${Math.round(share * 100)}% of ${usd(total)}` +
            (r.t.subscription > 0 ? `, ${usd(r.t.subscription)} of it covered by subscription` : "");
          return (
            <TableRow
              key={r.key}
              data-testid={testId}
              tabIndex={r.onOpen ? 0 : undefined}
              onClick={r.onOpen}
              onKeyDown={(e) => {
                if (r.onOpen && e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
                  e.preventDefault();
                  r.onOpen();
                }
              }}
              className={cn("outline-none focus-visible:bg-raised", r.onOpen && "cursor-pointer")}
            >
              {r.cells.map((c, i) => (
                <TableCell key={i}>
                  <Clip>{c}</Clip>
                </TableCell>
              ))}
              <TableCell>
                <div className="flex items-center gap-2" title={tip} aria-label={tip} role="img">
                  <div className="flex h-1.5 flex-1 overflow-hidden rounded-full bg-raised">
                    <div className="flex h-full" style={{ width: `${Math.max(width * 100, width > 0 ? 2 : 0)}%` }}>
                      <div className={cn("h-full", r.muted ? "bg-idle" : "bg-accent")} style={{ width: `${paidShare * 100}%` }} />
                      <div className={cn("h-full flex-1", r.muted ? "bg-idle/40" : "bg-accent/35")} />
                    </div>
                  </div>
                  <span className="w-9 shrink-0 text-right text-2xs tabular text-faint">
                    {total > 0 ? `${Math.round(share * 100)}%` : "-"}
                  </span>
                </div>
              </TableCell>
              <TableCell className="text-right text-xs font-medium tabular text-fg">{usd(billed(r.t))}</TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {r.t.subscription > 0 ? usd(r.t.subscription) : <span className="text-faint">-</span>}
              </TableCell>
              <TableCell className="text-right text-xs tabular whitespace-nowrap text-muted">
                {r.t.input + r.t.output > 0 ? (
                  <Tooltip label={tokenBreakdown(r.t)}>
                    <span>
                      {tokens(r.t.input)}
                      <span className="text-faint"> / </span>
                      {tokens(r.t.output)}
                    </span>
                  </Tooltip>
                ) : (
                  <span className="text-faint">-</span>
                )}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {r.t.turns.toLocaleString()}
                {r.t.unpriced > 0 ? (
                  <Tooltip label={`${r.t.unpriced} of these reported no cost`}>
                    <span className="ml-1 text-warn">*</span>
                  </Tooltip>
                ) : null}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">{r.t.modelTurns.toLocaleString()}</TableCell>
              {perTurn ? (
                <TableCell className="text-right text-xs tabular text-muted">
                  {r.t.turns > 0 ? usd(r.t.cost / r.t.turns) : "-"}
                </TableCell>
              ) : null}
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
