import type { Message } from "../gen/podium/v1/node_pb";
import { messageTone } from "../lib/format";
import { Badge, Chip } from "./Badge";

/**
 * TaskMessage renders what the task itself said, in a timeline row.
 *
 * The text is untrusted content written by somebody else's code, so it is shown verbatim
 * and never interpreted: no markdown, no links, whitespace preserved. Attachments are
 * artifact *names* the task asked a reader to attach; nothing has checked that they exist.
 *
 * It draws its own surface so that, nested under a timeline entry, it reads as the task
 * speaking rather than as more of the node's reporting.
 */
export function TaskMessage({ message }: { message: Message }) {
  return (
    <div className="min-w-0 flex-1 space-y-1.5 rounded-md border border-hairline bg-raised/40 px-2.5 py-2">
      <Badge tone={messageTone(message.type)}>{message.type || "message"}</Badge>
      <pre className="min-w-0 font-mono text-xs break-words whitespace-pre-wrap text-fg">
        {message.text}
      </pre>
      {message.attachments.length > 0 ? (
        <span className="flex flex-wrap gap-1">
          {message.attachments.map((name) => (
            <Chip key={name}>{name}</Chip>
          ))}
        </span>
      ) : null}
    </div>
  );
}
