import { useEffect, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Chat, ChatMessage, ChatPullRequest } from "../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../lib/client";

/** MAX_BACKOFF_MS caps the wait between reconnects. */
const MAX_BACKOFF_MS = 10_000;

/** BASE_BACKOFF_MS is the first wait after a failure. */
const BASE_BACKOFF_MS = 500;

export type ChatPhase = "connecting" | "streaming" | "error";

export interface ChatStreamState {
  /** messages is the conversation in seq order. */
  messages: ChatMessage[];
  /** progress is the newest progress line while a turn runs, or undefined. */
  progress?: string;
  /** running is true while a turn of this chat is in flight. */
  running: boolean;
  /** awaiting is true while an interactive turn has asked a question and is waiting. */
  awaiting: boolean;
  /** chat is the latest title/playbook row, when the stream said so. */
  chat?: Chat;
  /**
   * pullRequests is the chat's whole set of links, oldest first. The stream sends the set
   * before the transcript and again whenever it changes, so the newest frame is the truth
   * and there is nothing to merge.
   */
  pullRequests: ChatPullRequest[];
  /** taskId is the Podium task the running turn is using, when the stream said so. */
  taskId?: string;
  phase: ChatPhase;
  error?: string;
  /** gone is true when StreamChat answered NotFound: deleted, or never this login's. */
  gone?: boolean;
}

/** state carries the chat it belongs to, so a chat switch shows nothing rather than the
 *  previous conversation while the new stream connects — without resetting state from an
 *  effect body. */
interface Keyed extends ChatStreamState {
  chatId: string;
}

const empty: ChatStreamState = {
  messages: [],
  pullRequests: [],
  running: false,
  awaiting: false,
  phase: "connecting",
};

/**
 * useChatStream follows StreamChat for one chat.
 *
 * The stream replays everything after `from_seq` and then follows live, so reconnecting
 * with the highest seq already seen is exactly once: no gap and no repeat. It never ends on
 * its own — this hook's cleanup is what closes it, on unmount and on a chat switch.
 *
 * Three frame kinds do three different things:
 *
 * - `message` is durable. It is keyed on seq, so the same seq arriving twice — which is
 *   what happens when a turn's attachments are resolved after its answer was posted —
 *   replaces the message rather than appending a duplicate.
 * - `progress` is ephemeral. It replaces the previous line in place and is dropped the
 *   moment the answer lands, which is why a reload shows the answer and no trail.
 * - `resync` means the server dropped a durable frame because this browser fell behind.
 *   The stream is torn down and reopened from the last seq seen, and the replay delivers
 *   whatever was missed.
 */
export function useChatStream(chatId: string): ChatStreamState {
  const [state, setState] = useState<Keyed>({ ...empty, chatId });

  useEffect(() => {
    if (chatId === "") return;
    const ctrl = new AbortController();
    let lastSeq = 0n;

    // Only ever writes state for the chat this effect is following, so a frame arriving
    // from a stream that is being torn down cannot land in the next chat's view.
    const update = (fn: (prev: ChatStreamState) => ChatStreamState) =>
      setState((prev) => ({
        chatId,
        ...fn(prev.chatId === chatId ? prev : empty),
      }));

    void (async () => {
      let backoff = BASE_BACKOFF_MS;
      while (!ctrl.signal.aborted) {
        try {
          for await (const frame of agent.streamChat(
            { chatId, fromSeq: lastSeq },
            { signal: ctrl.signal },
          )) {
            backoff = BASE_BACKOFF_MS;
            switch (frame.frame.case) {
              case "message": {
                const msg = frame.frame.value;
                if (msg.seq > lastSeq) lastSeq = msg.seq;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  messages: merge(prev.messages, msg),
                  // The answer supersedes whatever the turn was last thinking about.
                  progress: msg.role === "assistant" ? undefined : prev.progress,
                }));
                break;
              }
              case "progress": {
                const text = frame.frame.value;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  progress: text,
                }));
                break;
              }
              case "status": {
                const status = frame.frame.value;
                const started = status.state === "started";
                const awaiting = status.state === "awaiting";
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  running: started || awaiting,
                  awaiting,
                  taskId: status.taskId === "" ? undefined : status.taskId,
                  progress: started || awaiting ? prev.progress : undefined,
                }));
                break;
              }
              case "pullRequests": {
                const set = frame.frame.value.pullRequests;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  pullRequests: set,
                }));
                break;
              }
              case "chat": {
                const row = frame.frame.value;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  chat: row,
                }));
                break;
              }
              case "resync":
                // Reopen from the last seq seen; the replay is the catch-up.
                throw new Resync();
              default:
                break;
            }
          }
          // A clean end of stream is not expected — StreamChat never ends on its own — so
          // reconnect rather than sit on a dead view.
          if (ctrl.signal.aborted) return;
        } catch (err) {
          if (ctrl.signal.aborted) return;
          if (err instanceof Resync) continue;
          const message = errorMessage(err);
          const gone = err instanceof ConnectError && err.code === Code.NotFound;
          update((prev) => ({ ...prev, phase: "error", error: message, gone }));
          // NotFound is a deleted or never-yours chat. Reconnecting would loop the same
          // refusal forever and claim "nothing was lost" about a conversation that is gone.
          if (gone) return;
          await sleep(backoff, ctrl.signal);
          backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
        }
      }
    })();

    return () => ctrl.abort();
  }, [chatId]);

  return state.chatId === chatId ? state : empty;
}

/** Resync unwinds the read loop so the outer loop reconnects with the last seq seen. */
class Resync extends Error {
  constructor() {
    super("resync");
    this.name = "Resync";
  }
}

/** merge replaces a message with the same seq and otherwise inserts in seq order. */
function merge(prev: ChatMessage[], msg: ChatMessage): ChatMessage[] {
  const at = prev.findIndex((m) => m.seq === msg.seq);
  if (at >= 0) {
    const next = prev.slice();
    next[at] = msg;
    return next;
  }
  if (prev.length === 0 || prev[prev.length - 1].seq < msg.seq) return prev.concat(msg);
  const next = prev.concat(msg);
  next.sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0));
  return next;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
}
