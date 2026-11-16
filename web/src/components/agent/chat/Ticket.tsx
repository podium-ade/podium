import type { ChatLinearIssue } from "../../../gen/podium/agent/v1/agent_pb";
import { cn } from "../../../lib/utils";

/**
 * A Linear ticket's conversation, as a row of the chat rail draws it: `EME-6 · title`, a dot
 * for where the issue is in Linear, and a mark while the conversation waits on the reader.
 */

/** stateTone is the dot's colour for Linear's kind of workflow state. */
function stateTone(stateType: string): string {
  switch (stateType) {
    case "started":
      return "bg-run";
    case "completed":
      return "bg-ok";
    case "canceled":
    case "cancelled":
      return "bg-faint";
    case "triage":
      return "bg-warn";
    default:
      // backlog, unstarted, or a state the deliveries never named.
      return "bg-muted";
  }
}

/** TicketTitle is the row's title: the identifier, set apart, then the issue's own words. */
export function TicketTitle({ ticket, fallback }: { ticket: ChatLinearIssue; fallback: string }) {
  const title = ticket.title || fallback;
  if (!ticket.identifier) return <>{title}</>;
  return (
    <>
      <span data-testid="chat-row-ticket" className="font-semibold text-muted tabular-nums">
        {ticket.identifier}
      </span>
      <span aria-hidden className="text-faint">
        {" · "}
      </span>
      {title}
    </>
  );
}

/** TicketState is the line under it: the Linear state, and "Waiting on you" when it is. */
export function TicketState({ ticket }: { ticket: ChatLinearIssue }) {
  if (!ticket.stateName && !ticket.waitingOnMe) return null;
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      {ticket.stateName ? (
        <span data-testid="chat-row-ticket-state" className="flex min-w-0 items-center gap-1">
          <span
            aria-hidden
            data-tone={ticket.stateType || "unknown"}
            className={cn("size-1.5 shrink-0 rounded-full", stateTone(ticket.stateType))}
          />
          <span className="truncate">{ticket.stateName}</span>
        </span>
      ) : null}
      {ticket.waitingOnMe ? (
        <span
          data-testid="chat-row-waiting"
          className="shrink-0 rounded-sm bg-warn-bg px-1 font-semibold text-warn"
        >
          Waiting on you
        </span>
      ) : null}
    </span>
  );
}
