import { ChevronRight } from "lucide-react";
import { useNavigate } from "react-router";
import type { TaskCost } from "../../gen/podium/agent/v1/agent_pb";
import { absolute, relative } from "../../lib/format";
import { hrefOf, kindLabel, optTokens, ranBy, rowCost, tokens, usd } from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { Tooltip } from "../ui/tooltip";
import { ModelCell, PaidWith, PersonCell, SourceCell } from "./marks";

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
}: {
  rows: TaskCost[];
  routines: Map<string, string>;
  busy?: boolean;
}) {
  const navigate = useNavigate();

  return (
    <Table
      aria-label="Spend"
      aria-busy={busy || undefined}
      className={cn("[&_td]:px-2.5 [&_th]:px-2.5", busy && "opacity-60")}
    >
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
          <TableHead className="w-0">
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
                "h-14 outline-none focus-visible:bg-raised",
                href ? "cursor-pointer" : "cursor-default",
              )}
            >
              <TableCell className="text-xs whitespace-nowrap text-muted" title={c.startedAt ? absolute(c.startedAt) : undefined}>
                {c.startedAt ? relative(c.startedAt) : "-"}
              </TableCell>
              <TableCell className="max-w-[18rem]">
                <What c={c} />
              </TableCell>
              <TableCell>
                <SourceCell origin={c.origin || c.sourceKind} />
              </TableCell>
              <TableCell className="max-w-[12rem]">
                <PersonCell startedBy={c.startedBy} routines={routines} />
              </TableCell>
              <TableCell className="max-w-[13rem]">
                <ModelCell provider={c.provider} model={c.model} effort={c.effort} agent={c.agent} />
              </TableCell>
              <TableCell className="max-w-[13rem]">
                <PaidWith
                  authKind={c.authKind}
                  owner={c.credentialOwner}
                  account={c.credentialAccount}
                  provider={c.provider}
                />
              </TableCell>
              <TableCell className="text-right text-xs tabular whitespace-nowrap text-muted">
                <TokenCell c={c} />
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">{c.numTurns ?? "-"}</TableCell>
              <TableCell pinned className="text-right text-xs tabular whitespace-nowrap">
                <CostCell c={c} />
              </TableCell>
              <TableCell className="w-0 pl-0">
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
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5 text-xs">
        <span className="shrink-0 rounded-sm bg-raised px-1 py-px text-2xs font-semibold text-soft">{kindLabel(c.kind)}</span>
        <span className="truncate font-medium text-fg">{c.kind === "summary" ? "Conversation summary" : ranBy(c.playbook)}</span>
        {c.status === "running" ? <span className="shrink-0 text-2xs text-run">running</span> : null}
      </div>
      <div className="mt-0.5 truncate text-2xs text-faint" title={context || c.taskId || undefined}>
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
  const tip = [
    `${(input ?? 0).toLocaleString()} in`,
    `${(output ?? 0).toLocaleString()} out`,
    reasoning > 0 ? `${reasoning.toLocaleString()} reasoning` : "",
    read > 0 ? `${read.toLocaleString()} read from cache` : "",
    write > 0 ? `${write.toLocaleString()} written to cache` : "",
  ]
    .filter(Boolean)
    .join(", ");
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
