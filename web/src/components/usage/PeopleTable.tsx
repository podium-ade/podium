import { Users } from "lucide-react";
import type { UsagePerson } from "../../gen/podium/agent/v1/agent_pb";
import { groupTotals, personLabel } from "../../lib/usage";
import { Empty } from "../Empty";
import { GroupTable } from "./GroupTable";
import { PersonCell } from "./marks";

/**
 * PeopleTable is the range's spend by whose turn it was: the person who started each turn
 * (multiplayer chats run every turn as its sender, never as the chat's owner), a system
 * routine by its name, or the stand-ins for nobody and the GitHub App. A row narrows the
 * screen to that person; "-" is the filter for nobody recorded.
 */
export function PeopleTable({
  people,
  routines,
  onPick,
}: {
  people: UsagePerson[];
  routines: Map<string, string>;
  onPick?: (startedBy: string) => void;
}) {
  if (people.length === 0) {
    return (
      <Empty
        icon={Users}
        title="Nobody spent anything in this range"
        hint="No agent turns ran, so there is nobody to attribute spend to."
      />
    );
  }
  return (
    <GroupTable
      label="Spend by person"
      testId="person-row"
      heads={["Person"]}
      rows={people.map((p) => ({
        key: p.startedBy || "-",
        name: personLabel(p.startedBy, routines),
        t: groupTotals(p),
        onOpen: onPick ? () => onPick(p.startedBy || "-") : undefined,
        cells: [<PersonCell key="p" startedBy={p.startedBy} routines={routines} />],
      }))}
    />
  );
}
