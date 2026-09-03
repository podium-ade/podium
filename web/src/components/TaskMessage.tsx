import type { Message } from "../gen/podium/v1/node_pb";
import { messageTone } from "../lib/format";
import { Badge, Chip } from "./Badge";

/**
 * TaskMessage renders what the task itself said, in a timeline row.
 *
 * The text is untrusted content written by somebody else's code, so it is shown verbatim
 * and never interpreted: no markdown, no links, whitespace preserved. Attachments are
 * artifact *names* the task asked a reader to attach; nothing has checked that they exist.
 */
export function TaskMessage({ message }: { message: Message }) {
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-1">
      <span>
        <Badge tone={messageTone(message.type)}>{message.type || "message"}</Badge>
      </span>
      <pre className="min-w-0 font-mono text-xs break-words whitespace-pre-wrap">
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
