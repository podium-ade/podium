import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { useNavigate } from "react-router";
import type { TaskCost } from "../../gen/podium/agent/v1/agent_pb";
import { absolute, relative } from "../../lib/format";
import { hrefOf, kindLabel, optTokens, ranBy, rowCost, tokenBreakdown, tokens, usd } from "../../lib/usage";
import { dimmed } from "../../lib/useLagging";
import { cn } from "../../lib/utils";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { Tooltip } from "../ui/tooltip";
import { Clip, ModelCell, PaidWith, PersonCell, SourceCell } from "./marks";

/**
 * SpendTable is every unit of spend in the range, newest first: each turn, each task a turn
 * delegated and each piece of the conductor's own housekeeping. The rows are the conductor's,
 * paged by the server, so a busy range is shown in full rather than cut at a cap.
 *
 * A row opens its task when it ran as one, and its conversation otherwise: an assistant turn
 * answered on the chat host has no task, but it has a chat.
 *
 * The cost column is pinned so it stays in view while the wide middle scrolls. A row a
 * subscription paid for shows its list-price figure in a quieter tone with "covered", so it is
 * never added up by eye with money that was billed.
 */
export function SpendTable({
  rows,
  routines,
  busy,
  footer,
}: {
  rows: TaskCost[];
  routines: Map<string, string>;
  busy?: boolean;
  /** footer is the table's Pager, inside its card under the last row. */
  footer?: ReactNode;
}) {
  const navigate = useNavigate();

  return (
    <Table
      aria-label="Spend"
      aria-busy={busy || undefined}
      stickyScrollbar
      footer={footer}
      className={cn("min-w-[73rem] table-fixed [&_td]:px-2.5 [&_td]:py-2 [&_th]:px-2.5", dimmed(busy))}
    >
      {/* Set widths, never sized from the rows, so a filter that leaves other rows behind
          does not move the columns. What is the one flexible column: it takes whatever the
          card has beyond the others, and never less than 10.75rem; below the table's minimum
          width (the columns' sum) the table scrolls sideways instead, with Cost pinned.
          Text that does not fit truncates, and hovering it shows the whole of it. */}
      <colgroup>
        <col className="w-16" />
        <col />
        <col className="w-36" />
        <col className="w-57" />
        <col className="w-45" />
        <col className="w-24" />
        <col className="w-22" />
        <col className="w-14" />
        <col className="w-26" />
        <col className="w-9" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>When</TableHead>
          <TableHead>What</TableHead>
          <TableHead>Source</TableHead>
          <TableHead>Person</TableHead>
          <TableHead>Model</TableHead>
          <TableHead>Paid with</TableHead>
          <TableHead className="text-right">Tokens</TableHead>
          <TableHead className="text-right">Steps</TableHead>
          <TableHead pinned className="text-right">
            Cost
          </TableHead>
          <TableHead>
            <span className="sr-only">Open</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((c) => {
          const href = hrefOf(c);
          const open = () => {
            if (href) void navigate(href);
          };
          return (
            <TableRow
              key={c.turnId}
              data-testid="usage-row"
              tabIndex={href ? 0 : undefined}
              onClick={open}
              onKeyDown={(e) => {
                if (e.target !== e.currentTarget) return;
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  open();
                }
              }}
              className={cn(
                "outline-none focus-visible:bg-raised",
                href ? "cursor-pointer" : "cursor-default",
              )}
            >
              <TableCell className="text-xs whitespace-nowrap text-muted">
                {c.startedAt ? (
                  <Tooltip label={absolute(c.startedAt)}>
                    <span>{relative(c.startedAt)}</span>
                  </Tooltip>
                ) : (
                  "-"
                )}
              </TableCell>
              <TableCell>
                <Clip>
                  <What c={c} />
                </Clip>
              </TableCell>
              <TableCell>
                <Clip>
                  <SourceCell origin={c.origin || c.sourceKind} />
                </Clip>
              </TableCell>
              <TableCell>
                <Clip>
                  <PersonCell startedBy={c.startedBy} routines={routines} />
                </Clip>
              </TableCell>
              <TableCell>
                <Clip>
                  <ModelCell provider={c.provider} model={c.model} effort={c.effort} agent={c.agent} />
                </Clip>
              </TableCell>
              <TableCell>
                <Clip>
                  <PaidWith
                    compact
                    authKind={c.authKind}
                    owner={c.credentialOwner}
                    account={c.credentialAccount}
                    provider={c.provider}
                  />
                </Clip>
              </TableCell>
              <TableCell className="text-right text-xs tabular whitespace-nowrap text-muted">
                <TokenCell c={c} />
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">{c.numTurns ?? "-"}</TableCell>
              <TableCell pinned className="text-right text-xs tabular whitespace-nowrap">
                <CostCell c={c} />
              </TableCell>
              <TableCell>
                {href ? <ChevronRight aria-hidden className="size-4 text-faint group-hover/row:text-muted" /> : null}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

/**
 * What names the row: whether it was a turn, a delegated task or housekeeping, what ran it
 * (a playbook, or the assistant), and the conversation or routine it belongs to.
 */
function What({ c }: { c: TaskCost }) {
  const context = c.routineName ? `Routine: ${c.routineName}` : c.chatTitle;
  const name = c.kind === "summary" ? "Conversation summary" : ranBy(c.playbook);
  return (
    <div className="min-w-0" data-full={[`${kindLabel(c.kind)}: ${name}`, context || c.taskId].filter(Boolean).join("\n")}>
      <div className="flex min-w-0 items-center gap-1.5 text-xs">
        <span className="shrink-0 rounded-sm bg-raised px-1 py-px text-2xs font-semibold text-soft">{kindLabel(c.kind)}</span>
        <span className="min-w-0 truncate font-medium text-fg">
          {name}
        </span>
        {c.status === "running" ? <span className="shrink-0 text-2xs text-run">running</span> : null}
      </div>
      <div className="mt-0.5 truncate text-2xs text-faint">
        {context || (c.taskId ? <span className="font-mono">{c.taskId}</span> : "-")}
      </div>
    </div>
  );
}

function TokenCell({ c }: { c: TaskCost }) {
  const input = optTokens(c.inputTokens);
  const output = optTokens(c.outputTokens);
  if (input === undefined && output === undefined) {
    return <span className="text-faint">-</span>;
  }
  const read = optTokens(c.cacheReadTokens) ?? 0;
  const write = optTokens(c.cacheWriteTokens) ?? 0;
  const reasoning = optTokens(c.reasoningTokens) ?? 0;
  const tip = tokenBreakdown({
    input: input ?? 0,
    output: output ?? 0,
    reasoning,
    cacheRead: read,
    cacheWrite: write,
  });
  return (
    <Tooltip label={tip}>
      <span>
        {tokens(input ?? 0)}
        <span className="text-faint"> / </span>
        {tokens(output ?? 0)}
      </span>
    </Tooltip>
  );
}

function CostCell({ c }: { c: TaskCost }) {
  const { billed, covered } = rowCost(c);
  if (billed !== undefined) return <span className="font-medium text-fg">{usd(billed)}</span>;
  if (covered !== undefined) {
    return (
      <Tooltip label="List-price equivalent. A subscription paid for this, so it is not in billed spend.">
        <span className="text-muted">
          {usd(covered)}
          <span className="ml-1 text-2xs text-faint">covered</span>
        </span>
      </Tooltip>
    );
  }
  return (
    <Tooltip label={c.status === "running" ? "Still running: the cost arrives when it ends" : "The runtime reported no cost"}>
      <span className="text-faint">-</span>
    </Tooltip>
  );
}
