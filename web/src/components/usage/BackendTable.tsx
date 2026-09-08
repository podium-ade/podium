import { Cpu } from "lucide-react";
import type { UsageBackend } from "../../gen/podium/agent/v1/agent_pb";
import { isUnrecorded, usd } from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../ui/table";
import { Tooltip } from "../ui/tooltip";

/** One tone per provider, so a row is placed before it is read. */
const PROVIDER = {
  anthropic: { tone: "ok", label: "Anthropic" },
  xai: { tone: "lost", label: "xAI" },
} as const;

/**
 * BackendTable is spend grouped by what actually ran it: provider, agent, model and effort.
 *
 * The rows come from the server's own GROUP BY, not from grouping the costs page in the
 * browser — that page is capped, and a model whose turns fell off the end of it would simply
 * be missing here rather than visibly short.
 */
export function BackendTable({ backends }: { backends: UsageBackend[] }) {
  const rows = [...backends].sort((a, b) => b.costUsd - a.costUsd || b.turns - a.turns);
  const total = rows.reduce((s, b) => s + b.costUsd, 0);

  if (rows.length === 0) {
    return (
      <Empty
        icon={Cpu}
        title="Nothing ran in this range"
        hint="Pick a wider range. Every turn records the model it ran on from the moment it starts."
      />
    );
  }

  return (
    <Table className="[&_td]:px-2.5 [&_th]:px-2.5">
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>Provider</TableHead>
          <TableHead>Model</TableHead>
          <TableHead>Agent</TableHead>
          <TableHead>Effort</TableHead>
          <TableHead className="text-right">Turns</TableHead>
          <TableHead className="text-right">Model turns</TableHead>
          <TableHead className="text-right">Avg / turn</TableHead>
          <TableHead className="text-right">Cost</TableHead>
          <TableHead className="w-32">Share</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((b) => {
          const unrecorded = isUnrecorded(b);
          const p = PROVIDER[b.provider as keyof typeof PROVIDER];
          const share = total > 0 ? b.costUsd / total : 0;
          return (
            <TableRow key={`${b.provider}/${b.agent}/${b.model}/${b.effort}`} data-testid="backend-row">
              <TableCell>
                {unrecorded ? (
                  <Tooltip label="These turns ran before the conductor recorded what they ran on. Their cost is real; only the attribution is missing.">
                    <span className="text-xs text-faint">unrecorded</span>
                  </Tooltip>
                ) : p ? (
                  <Badge tone={p.tone} dot={false}>
                    {p.label}
                  </Badge>
                ) : (
                  <Chip>{b.provider}</Chip>
                )}
              </TableCell>
              <TableCell className="font-mono text-xs text-fg">
                {unrecorded ? <span className="text-faint">—</span> : b.model}
              </TableCell>
              <TableCell className="text-xs text-muted">{b.agent || "—"}</TableCell>
              <TableCell>
                {/* An empty effort on a recorded turn is not missing data: it means the
                    model's own default, which is a different thing from "unrecorded". */}
                {unrecorded ? (
                  <span className="text-xs text-faint">—</span>
                ) : b.effort ? (
                  <Chip>{b.effort}</Chip>
                ) : (
                  <span className="text-xs text-faint">default</span>
                )}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {b.turns.toLocaleString()}
                {b.unpriced > 0 ? (
                  <Tooltip label={`${b.unpriced} of these reported no cost`}>
                    <span className="ml-1 text-warn">*</span>
                  </Tooltip>
                ) : null}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {b.modelTurns.toLocaleString()}
              </TableCell>
              <TableCell className="text-right text-xs tabular text-muted">
                {b.turns > 0 ? usd(b.costUsd / b.turns) : "—"}
              </TableCell>
              <TableCell className="text-right text-xs font-medium tabular text-fg">
                {usd(b.costUsd)}
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-raised">
                    <div
                      className={cn("h-full rounded-full", unrecorded ? "bg-idle" : "bg-accent")}
                      style={{ width: `${Math.max(share * 100, share > 0 ? 2 : 0)}%` }}
                    />
                  </div>
                  <span className="w-9 shrink-0 text-right text-2xs tabular text-faint">
                    {total > 0 ? `${Math.round(share * 100)}%` : "—"}
                  </span>
                </div>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

