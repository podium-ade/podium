import { Cpu } from "lucide-react";
import type { UsageBackend } from "../../gen/podium/agent/v1/agent_pb";
import { groupTotals, isUnrecorded, providerLabel } from "../../lib/usage";
import { Empty } from "../Empty";
import { GroupTable } from "./GroupTable";
import { ModelCell } from "./marks";

/**
 * BackendTable is spend grouped by what actually ran it: provider, agent, model and effort.
 *
 * The rows come from the server's own GROUP BY, not from grouping a page of rows in the
 * browser, so a model whose turns fell off the end of a page is never missing here. A row
 * narrows the screen to that model.
 */
export function BackendTable({
  backends,
  onPick,
}: {
  backends: UsageBackend[];
  onPick?: (model: string) => void;
}) {
  if (backends.length === 0) {
    return (
      <Empty
        icon={Cpu}
        title="Nothing ran in this range"
        hint="Pick a wider range. Every turn records the model it ran on from the moment it starts."
      />
    );
  }
  return (
    <GroupTable
      label="Spend by model"
      testId="backend-row"
      perTurn
      heads={["Model", "Provider", "Agent"]}
      rows={backends.map((b) => {
        const unrecorded = isUnrecorded(b);
        return {
          key: `${b.provider}/${b.agent}/${b.model}/${b.effort}`,
          name: unrecorded ? "Unrecorded" : b.model,
          muted: unrecorded,
          t: groupTotals(b),
          onOpen: onPick && !unrecorded ? () => onPick(b.model) : undefined,
          cells: [
            <ModelCell key="m" provider={b.provider} model={b.model} effort={b.effort || (unrecorded ? "" : "default")} />,
            <span key="p" className="block truncate text-xs text-muted">
              {unrecorded ? "-" : providerLabel(b.provider)}
            </span>,
            <span key="a" className="block truncate text-xs text-muted">
              {b.agent || "-"}
            </span>,
          ],
        };
      })}
    />
  );
}
