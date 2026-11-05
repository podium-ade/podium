import type { UsagePerson } from "../../gen/podium/agent/v1/agent_pb";
import { usd } from "../../lib/usage";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";

/** started_by values that are not a person, and what to call them. */
const NOT_A_PERSON: Record<string, { label: string; hint: string }> = {
  "": {
    label: "Unattributed",
    hint: "Turns from before the conductor recorded who started them, and turns started with the shared dev token.",
  },
  "github-app": {
    label: "GitHub App",
    hint: "Automatic reviews, and review commands from people outside Podium. They run as the App, so nobody's account pays for them.",
  },
};

export function personLabel(startedBy: string): string {
  return NOT_A_PERSON[startedBy]?.label ?? startedBy;
}

/**
 * PeopleTable is the range's spend by whose turn it was: the person who started each turn
 * (multiplayer chats run every turn as its sender, never as the chat's owner), most
 * expensive first, with a bar for each one's share of the total.
 *
 * It is one series, so the bars share one colour and need no legend; the row is the label.
 * The numbers are text beside the bar, so nothing is read from the colour alone.
 */
export function PeopleTable({ people }: { people: UsagePerson[] }) {
  if (people.length === 0) {
    return (
      <p className="rounded-xl border border-border bg-card py-8 text-center text-xs text-muted">
        No agent turns ran in this window, so there is nobody to attribute spend to.
      </p>
    );
  }
  // The server sends them most expensive first; sorting again keeps the order honest if a
  // client ever hands this a list it built itself.
  const rows = [...people].sort((a, b) => b.costUsd - a.costUsd || b.turns - a.turns);
  const total = rows.reduce((sum, p) => sum + p.costUsd, 0);
  const max = rows[0]?.costUsd ?? 0;

  return (
    <Table aria-label="Spend by person">
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>Person</TableHead>
          <TableHead className="w-[40%]">Share</TableHead>
          <TableHead className="text-right">Spent</TableHead>
          <TableHead className="text-right">Turns</TableHead>
          <TableHead className="text-right">Model turns</TableHead>
          <TableHead className="text-right">No cost reported</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((p) => {
          const special = NOT_A_PERSON[p.startedBy];
          // Bars are scaled to the largest row, not to the total, so the ranking reads at a
          // glance; the percentage beside the bar is the share of the total.
          const width = max > 0 ? p.costUsd / max : 0;
          const share = total > 0 ? p.costUsd / total : 0;
          const tip = `${personLabel(p.startedBy)}: ${usd(p.costUsd)} (${Math.round(share * 100)}% of ${usd(total)}) over ${p.turns} ${p.turns === 1 ? "turn" : "turns"}`;
          return (
            <TableRow key={p.startedBy || "-"} data-testid="person-row">
              <TableCell>
                <span
                  className={special ? "text-muted" : "font-mono text-fg"}
                  title={special?.hint ?? p.startedBy}
                >
                  {personLabel(p.startedBy)}
                </span>
              </TableCell>
              <TableCell>
                {/* The hit target is the whole track, taller than the 6px bar inside it. */}
                <div className="flex items-center gap-2" title={tip} aria-label={tip} role="img">
                  <div className="flex h-4 flex-1 items-center">
                    <div className="h-1.5 w-full overflow-hidden rounded-full bg-raised">
                      <div
                        className="h-full rounded-full bg-accent transition-[width] duration-300 ease-out"
                        style={{ width: `${Math.max(width * 100, width > 0 ? 2 : 0)}%` }}
                      />
                    </div>
                  </div>
                  <span className="w-9 shrink-0 text-right text-2xs tabular text-faint">
                    {Math.round(share * 100)}%
                  </span>
                </div>
              </TableCell>
              <TableCell className="text-right tabular text-fg">{usd(p.costUsd)}</TableCell>
              <TableCell className="text-right tabular text-muted">{p.turns.toLocaleString()}</TableCell>
              <TableCell className="text-right tabular text-muted">{p.modelTurns.toLocaleString()}</TableCell>
              <TableCell className="text-right tabular text-muted">
                {p.unpriced > 0 ? p.unpriced.toLocaleString() : "-"}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
