import type { LucideIcon } from "lucide-react";
import { Hash, MessageSquare, SquareKanban, Terminal } from "lucide-react";
import { Link, useNavigate } from "react-router";
import type { TaskCost } from "../../gen/podium/agent/v1/agent_pb";
import type { Task } from "../../gen/podium/v1/task_pb";
import { absolute, relative, taskDuration, taskStatusLabel, taskStatusTone } from "../../lib/format";
import { ranBy, usd } from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Badge, Chip, type Tone } from "../Badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../ui/table";
import { Tooltip } from "../ui/tooltip";

/** The same source vocabulary the sessions table uses, so a row reads the same on both. */
const KIND: Record<string, { icon: LucideIcon; tone: Tone }> = {
  chat: { icon: MessageSquare, tone: "ok" },
  slack: { icon: Hash, tone: "run" },
  linear: { icon: SquareKanban, tone: "lost" },
  dev: { icon: Terminal, tone: "warn" },
};

/**
 * TaskCostTable is every task in the window with what it cost beside it.
 *
 * The two halves come from two databases that do not reference each other — tasks from the
 * control plane, cost from the conductor — so `costs` is a lookup, not a join the server
 * did. A task with no entry is not a task that cost nothing: it is one no agent turn ran,
 * and it is shown with a dash rather than a zero.
 *
 * Rows are newest first and that order is the server's. Sorting by cost is deliberately not
 * offered: the control plane cannot order by a column it does not have, so a cost sort could
 * only reorder the page already on screen and would silently lie about being a ranking.
 */
export function TaskCostTable({ tasks, costs }: { tasks: Task[]; costs: Map<string, TaskCost> }) {
  const navigate = useNavigate();

  return (
    <Table className="[&_td]:px-2.5 [&_th]:px-2.5">
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-44">Status</TableHead>
          <TableHead>Task</TableHead>
          <TableHead>Source</TableHead>
          <TableHead>Ran by</TableHead>
          <TableHead>Started</TableHead>
          <TableHead className="text-right">Duration</TableHead>
          <TableHead className="text-right">CPU</TableHead>
          <TableHead className="text-right">Model turns</TableHead>
          <TableHead className="text-right">Cost</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {tasks.map((t) => {
          const cost = costs.get(t.id);
          const kind = cost ? KIND[cost.sourceKind] : undefined;
          const cpu = t.usage?.cpuSeconds ?? 0;
          return (
            <TableRow
              key={t.id}
              data-testid="usage-row"
              className="h-14 cursor-pointer"
              onClick={() => navigate(`/tasks/${t.id}`)}
            >
              <TableCell>
                <Badge tone={taskStatusTone(t.status)}>{taskStatusLabel(t.status)}</Badge>
              </TableCell>
              <TableCell className="max-w-[20rem]">
                <Link
                  to={`/tasks/${t.id}`}
                  onClick={(e) => e.stopPropagation()}
                  className="block truncate font-mono text-xs text-accent hover:underline"
                >
                  {t.id}
                </Link>
                <div className="truncate font-mono text-2xs text-faint" title={t.spec?.image}>
                  {t.spec?.image || "—"}
                </div>
              </TableCell>
              <TableCell>
                {cost && kind ? (
                  <Badge tone={kind.tone} dot={false}>
                    <kind.icon aria-hidden className="size-3" />
                    {cost.sourceKind}
                  </Badge>
                ) : (
                  // Not an agent task: nothing spent model credit on its behalf.
                  <Tooltip label="No agent turn ran this task, so it has no model cost">
                    <span className="text-xs text-faint">—</span>
                  </Tooltip>
                )}
              </TableCell>
              <TableCell>
                {cost ? <Chip>{ranBy(cost.playbook)}</Chip> : <span className="text-xs text-faint">—</span>}
              </TableCell>
              <TableCell
                className="text-xs whitespace-nowrap text-muted"
                title={absolute(t.createdAt)}
              >
                {relative(t.createdAt)}
              </TableCell>
              <TableCell className="text-right text-xs tabular whitespace-nowrap text-muted">
                {taskDuration(t.startedAt, t.finishedAt)}
              </TableCell>
              <TableCell className="text-right text-xs tabular whitespace-nowrap text-muted">
                {cpu > 0 ? `${cpu.toFixed(1)}s` : "—"}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {cost?.numTurns ?? "—"}
              </TableCell>
              <TableCell
                className={cn(
                  "text-right text-xs tabular whitespace-nowrap",
                  cost?.costUsd === undefined ? "text-faint" : "font-medium text-fg",
                )}
              >
                {cost?.costUsd === undefined ? "—" : usd(cost.costUsd)}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
