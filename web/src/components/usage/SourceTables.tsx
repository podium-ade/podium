import { MessagesSquare, Repeat, Waypoints } from "lucide-react";
import { useNavigate } from "react-router";
import type { UsageGroup } from "../../gen/podium/agent/v1/agent_pb";
import { credentialLabel, originLabel, providerLabel, totals } from "../../lib/usage";
import { Empty } from "../Empty";
import { GroupTable } from "./GroupTable";
import { PaidWith, SourceCell } from "./marks";

/**
 * SourceTable is the range by where the work came from: a Podium chat, Slack, GitHub, Linear,
 * a routine's run wherever it ran, or the conductor's own housekeeping. A row narrows the
 * screen to that source.
 */
export function SourceTable({ sources, onPick }: { sources: UsageGroup[]; onPick?: (origin: string) => void }) {
  if (sources.length === 0) {
    return <Empty icon={Waypoints} title="Nothing ran in this range" hint="Pick a wider range." />;
  }
  return (
    <GroupTable
      label="Spend by source"
      testId="source-row"
      heads={["Source"]}
      rows={sources.map((g) => ({
        key: g.key,
        name: originLabel(g.key),
        t: totals(g.totals),
        onOpen: onPick ? () => onPick(g.key) : undefined,
        cells: [<SourceCell key="s" origin={g.key} />],
      }))}
    />
  );
}

/** RoutineTable is what each routine's runs cost, the tasks they delegated included. */
export function RoutineTable({ routines }: { routines: UsageGroup[] }) {
  if (routines.length === 0) {
    return (
      <Empty icon={Repeat} title="No routine ran in this range" hint="A routine's runs show here with what they and their tasks cost." />
    );
  }
  return (
    <GroupTable
      label="Spend by routine"
      testId="routine-row"
      heads={["Routine"]}
      rows={routines.map((g) => ({
        key: g.key,
        name: g.label || g.key,
        t: totals(g.totals),
        cells: [
          <span key="r" className="inline-flex min-w-0 items-center gap-2 text-xs text-fg">
            <Repeat aria-hidden className="size-3.5 shrink-0 text-faint" />
            <span className="truncate">{g.label || g.key}</span>
          </span>,
        ],
      }))}
    />
  );
}

/**
 * ConversationTable is the range's most expensive conversations, each opening its chat. A
 * conversation is where a cost is easiest to understand: the question, the work it set off,
 * and what that took are all in one place.
 */
export function ConversationTable({ conversations }: { conversations: UsageGroup[] }) {
  const navigate = useNavigate();
  if (conversations.length === 0) {
    return (
      <Empty icon={MessagesSquare} title="No conversation spent anything in this range" hint="Pick a wider range." />
    );
  }
  return (
    <GroupTable
      label="Most expensive conversations"
      testId="conversation-row"
      heads={["Conversation", "Source"]}
      rows={conversations.map((g) => ({
        key: g.key,
        name: g.label || "Untitled",
        t: totals(g.totals),
        onOpen: g.chatId ? () => void navigate(`/agent/chat/${g.chatId}`) : undefined,
        cells: [
          <span key="c" className="block truncate text-xs font-medium text-fg" title={g.label}>
            {g.label || <span className="text-faint">Untitled</span>}
          </span>,
          <SourceCell key="s" origin={g.origin} />,
        ],
      }))}
    />
  );
}

/**
 * CredentialTable is the range by what paid: a subscription or an API key, which provider's,
 * and whose, with the account the provider named. Rows from before the conductor recorded it
 * are one unrecorded bucket. A row narrows the screen to that credential.
 */
export function CredentialTable({
  credentials,
  onPick,
}: {
  credentials: UsageGroup[];
  onPick?: (key: string) => void;
}) {
  if (credentials.length === 0) {
    return <Empty icon={Waypoints} title="Nothing ran in this range" hint="Pick a wider range." />;
  }
  return (
    <GroupTable
      label="Spend by credential"
      testId="credential-row"
      heads={["Paid with", "Provider"]}
      rows={credentials.map((g) => ({
        key: g.key,
        name: credentialLabel(g),
        muted: g.authKind === "",
        t: totals(g.totals),
        onOpen: onPick ? () => onPick(g.key) : undefined,
        cells: [
          <PaidWith key="w" authKind={g.authKind} owner={g.owner} account={g.account} provider={g.provider} />,
          <span key="p" className="text-xs text-muted">{g.authKind === "" ? "-" : providerLabel(g.provider)}</span>,
        ],
      }))}
    />
  );
}

