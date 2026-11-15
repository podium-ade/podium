import { useCallback, useEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Chat, ChatMessage, ChatPullRequest, QueuedTurn } from "../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../lib/client";

/** MAX_BACKOFF_MS caps the wait between reconnects. */
const MAX_BACKOFF_MS = 10_000;

/** BASE_BACKOFF_MS is the first wait after a failure. */
const BASE_BACKOFF_MS = 500;

/**
 * CHAT_PAGE is how many rows a chat opens on, and how many more each scroll to the top
 * loads. A turn is several rows — its thoughts, tool calls and answer — so this is a few
 * screens of conversation, not a hundred questions.
 */
export const CHAT_PAGE = 100;

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
  /**
   * pullRequestsKnown is true once the stream has sent the set. It is sent at the start of
   * every stream, so until then an empty list means "not heard yet", not "none".
   */
  pullRequestsKnown: boolean;
  /**
   * queue is the turns waiting behind the one in flight, oldest first (phase 4). Like the
   * pull requests, every frame is the whole set.
   */
  queue: QueuedTurn[];
  /** taskId is the Podium task the running turn is using, when the stream said so. */
  taskId?: string;
  phase: ChatPhase;
  /**
   * replayed is true once the stream has delivered the transcript so far. The status, chat
   * and pull-request frames come before the replay, so until then "no messages" does not
   * mean an empty chat. It stays true across a reconnect: what was replayed is still here.
   */
  replayed: boolean;
  error?: string;
  /** gone is true when StreamChat answered NotFound: deleted, or never this login's. */
  gone?: boolean;
  /**
   * hasOlder is true while there is history before the first message in hand. A chat opens
   * on its newest CHAT_PAGE rows; loadOlder pages back from there.
   */
  hasOlder: boolean;
  /** loadingOlder is true while a page of older history is on its way. */
  loadingOlder: boolean;
  /** olderError is why the last page of older history failed, until the next try. */
  olderError?: string;
}

export interface ChatStream extends ChatStreamState {
  /** loadOlder fetches the page before the first message in hand. A no-op while one is
   *  already loading, or when there is nothing older. */
  loadOlder: () => void;
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
  pullRequestsKnown: false,
  queue: [],
  running: false,
  awaiting: false,
  phase: "connecting",
  replayed: false,
  hasOlder: false,
  loadingOlder: false,
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
export function useChatStream(chatId: string, linkToken = ""): ChatStream {
  const [state, setState] = useState<Keyed>({ ...empty, chatId });
  // The live state, for loadOlder to read without being rebuilt on every frame.
  const latest = useRef(state);
  useEffect(() => {
    latest.current = state;
  }, [state]);
  // The page last asked for, by the seq it pages back from. It is what stops a second ask
  // for the same page — while it is out, and also just after it lands, when a scroll can
  // still see the old first message because `latest` catches up only after the render.
  const asked = useRef<{ chatId: string; before: bigint } | undefined>(undefined);

  useEffect(() => {
    if (chatId === "") return;
    const ctrl = new AbortController();
    let lastSeq = 0n;
    // first is true until the first replay is in: that one opened on the tail, so how many
    // rows it brought says whether there is history before them.
    let first = true;

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
        // replayedRows counts this stream's replay, until the stream says it is done.
        let replayedRows: number | undefined = 0;
        const fromSeq = lastSeq;
        try {
          const req = {
            chatId,
            fromSeq,
            // A first open is the newest page; a reconnect must have every row it missed.
            tail: fromSeq === 0n ? CHAT_PAGE : 0,
            // linkToken is a share link's k= (phase 5): with it the server lets a signed-in
            // person open a chat shared by link, and writes them a row so they keep it.
            ...(linkToken ? { linkToken } : {}),
          };
          for await (const frame of agent.streamChat(req, { signal: ctrl.signal })) {
            backoff = BASE_BACKOFF_MS;
            switch (frame.frame.case) {
              case "message": {
                const msg = frame.frame.value;
                if (msg.seq > lastSeq) lastSeq = msg.seq;
                if (replayedRows !== undefined) replayedRows++;
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
                  pullRequestsKnown: true,
                }));
                break;
              }
              case "chat": {
                const row = frame.frame.value;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  // shared_with_me and my_role are per viewer; the server works both out
                  // again for this viewer on every chat frame (phase 5), so a share or an
                  // unshare reaches this screen. An older server left them off later frames,
                  // so an empty value keeps what the first frame said.
                  chat: {
                    ...row,
                    sharedWithMe: row.sharedWithMe || (prev.chat?.sharedWithMe ?? false),
                    myRole: row.myRole || (prev.chat?.myRole ?? ""),
                  },
                }));
                break;
              }
              case "queue": {
                const turns = frame.frame.value.turns;
                update((prev) => ({ ...prev, phase: "streaming", error: undefined, queue: turns }));
                break;
              }
              case "replayed": {
                const rows = replayedRows ?? 0;
                replayedRows = undefined;
                const opened = first;
                first = false;
                update((prev) => ({
                  ...prev,
                  phase: "streaming",
                  error: undefined,
                  replayed: true,
                  // A tail that came back full may have more behind it; one that did not is
                  // the whole conversation. A first open that dropped mid-replay and came
                  // back from a seq cannot tell, and says there may be.
                  hasOlder: opened ? (fromSeq === 0n ? rows >= CHAT_PAGE : true) : prev.hasOlder,
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
  }, [chatId, linkToken]);

  const loadOlder = useCallback(() => {
    const cur = latest.current;
    if (cur.chatId !== chatId || !cur.hasOlder) return;
    const beforeSeq = cur.messages[0]?.seq ?? 0n;
    const prior = asked.current;
    if (prior && prior.chatId === chatId && prior.before === beforeSeq) return;
    const ask = { chatId, before: beforeSeq };
    asked.current = ask;
    const update = (fn: (prev: Keyed) => Keyed) =>
      setState((prev) => (prev.chatId === chatId ? fn(prev) : prev));
    update((prev) => ({ ...prev, loadingOlder: true, olderError: undefined }));
    void agent
      .listChatMessages({ chatId, beforeSeq, limit: CHAT_PAGE })
      .then((res) =>
        update((prev) => ({
          ...prev,
          loadingOlder: false,
          hasOlder: res.hasMore,
          messages: mergeAll(prev.messages, res.messages),
        })),
      )
      .catch((err: unknown) => {
        // A failed page may be asked for again: that is what Retry does.
        if (asked.current === ask) asked.current = undefined;
        update((prev) => ({ ...prev, loadingOlder: false, olderError: errorMessage(err) }));
      });
  }, [chatId]);

  return { ...(state.chatId === chatId ? state : empty), loadOlder };
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

/** mergeAll merges a batch — a page of history — in one pass. The batch's copy of a
 *  seq wins: it is the one just read. */
function mergeAll(prev: ChatMessage[], rows: ChatMessage[]): ChatMessage[] {
  if (rows.length === 0) return prev;
  const bySeq = new Map<bigint, ChatMessage>();
  for (const m of prev) bySeq.set(m.seq, m);
  for (const m of rows) bySeq.set(m.seq, m);
  return [...bySeq.values()].sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0));
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
