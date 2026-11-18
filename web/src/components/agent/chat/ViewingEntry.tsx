import { Eye, EyeOff } from "lucide-react";
import type { Chat } from "../../../gen/podium/agent/v1/agent_pb";
import { viewingKind } from "../../../lib/sharing";
import { Button } from "../../ui/button";
import { Tooltip } from "../../ui/tooltip";

/**
 * ViewingEntry is the open chat at the top of the rail when the rail has no row for it on
 * screen: a routine's chat an admin reads, a Linear issue's conversation, somebody's chat the
 * viewer may read, or one of their own the search or source filter is hiding. Nothing is
 * stored: it is the open chat, and it goes when another chat is opened.
 *
 * The entry only says where you are. Watch, which keeps the chat in the rail for good
 * (SetChatWatched), is in the chat's header beside Share.
 */
export function ViewingEntry({
  chat,
  viewerLogin,
}: {
  chat: Chat;
  viewerLogin: string;
}) {
  const title = chat.linearIssue?.identifier
    ? `${chat.linearIssue.identifier} · ${chat.linearIssue.title || chat.title}`
    : chat.title || "Untitled chat";
  return (
    <li data-testid="chat-viewing" className="mb-2 border-b border-border pb-2">
      <p className="px-3 pt-1 pb-1 text-2xs font-bold tracking-[0.08em] text-faint uppercase">Viewing</p>
      <div
        aria-current="true"
        className="flex min-h-[52px] items-center gap-2 rounded-md border border-line-strong bg-panel py-2 pr-1.5 pl-3 shadow-[0_1px_2px_#0002]"
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-bold text-fg">{title}</span>
          <span data-testid="chat-viewing-kind" className="mt-0.5 block truncate text-2xs text-muted">
            {viewingKind(chat, viewerLogin)}
          </span>
        </span>
      </div>
    </li>
  );
}

/** WatchButton keeps a chat in the viewer's rail, or stops. It sits beside Share, its size. */
export function WatchButton({
  watched,
  busy,
  onWatch,
}: {
  watched: boolean;
  busy: boolean;
  onWatch: (on: boolean) => void;
}) {
  const label = watched ? "Unwatch" : "Watch";
  const hint = watched ? "Take this chat out of your chat list" : "Keep this chat in your chat list";
  return (
    <Tooltip label={hint}>
      <Button
        type="button"
        size="sm"
        variant="outline"
        data-testid="chat-watch"
        aria-pressed={watched}
        disabled={busy}
        onClick={() => onWatch(!watched)}
        className="shrink-0"
      >
        {watched ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
        {label}
      </Button>
    </Tooltip>
  );
}
