import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDown,
  Bot,
  MessageSquarePlus,
  Pencil,
  Sparkles,
  Terminal,
  Trash2,
} from "lucide-react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Chat, ChatMessage, Playbook } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { useChatStream } from "../../hooks/useChatStream";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative, toDate } from "../../lib/format";
import { Badge } from "../Badge";
import { Empty } from "../Empty";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Tooltip } from "../ui/tooltip";
import { ChatAttachments } from "./ChatAttachments";
import { ChatComposer } from "./ChatComposer";
import { ChatPullRequests } from "./ChatPullRequests";
import { ConductorDown } from "./ConductorDown";
import { ChatMarkdown } from "./chat/ChatMarkdown";

/** SCROLL_SLACK_PX is how far off the bottom still counts as "at the bottom". */
const SCROLL_SLACK_PX = 40;

/** Matches store.MaxChatTitleRunes — the input refuses more, the server does too. */
const MAX_CHAT_TITLE = 80;

/**
 * ChatPanel is the web chat: the third place a turn can start from, and the only one whose
 * transcript Podium itself holds.
 *
 * EVERYTHING IN A BUBBLE IS CONTENT. A human wrote the questions and a task wrote the
 * answers; this screen renders both through the markdown subset in lib/markdown.ts, which
 * emits no raw HTML and treats any link scheme but http(s) as text. Nothing here interprets
 * what an agent said.
 */
export function ChatPanel() {
  const navigate = useNavigate();
  // The tab's route is `chat/*`, so the rest of the path is the chat id — which keeps
  // /agent/chat/<id> a deep link and the back button a real navigation.
  const active = (useParams()["*"] ?? "").split("/")[0];
  const qc = useQueryClient();
  const toast = useToast();

  const chats = useQuery({
    queryKey: ["agent", "chats"],
    queryFn: () => agent.listChats({}),
    // A turn started in another tab, or by somebody else's browser on the same login,
    // changes turn_running and the preview. Nothing else here would notice.
    refetchInterval: 10_000,
  });
  const playbooks = useQuery({
    queryKey: ["agent", "playbooks"],
    queryFn: () => agent.listPlaybooks({}),
    staleTime: 5 * 60_000,
  });

  const create = useMutation({
    mutationFn: (title: string) => agent.createChat({ title }),
    onSuccess: async (res) => {
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      if (res.chat) navigate(`/agent/chat/${res.chat.id}`);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const rename = useMutation({
    mutationFn: (v: { id: string; title: string }) =>
      agent.renameChat({ chatId: v.id, title: v.title }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const [pendingDelete, setPendingDelete] = useState<Chat | null>(null);
  const remove = useMutation({
    mutationFn: (chat: Chat) => agent.deleteChat({ chatId: chat.id }),
    onSuccess: async (_res, chat) => {
      setPendingDelete(null);
      toast(`${chat.title} deleted.`, "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      if (active === chat.id) navigate("/agent/chat");
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const renameChat = (id: string, title: string) =>
    rename.mutateAsync({ id, title }).then(() => undefined);

  // n opens a new chat when the composer is not focused, which is the one shortcut worth
  // having on a page whose main control is a textarea.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "n" || e.metaKey || e.ctrlKey || e.altKey) return;
      const el = document.activeElement;
      if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) return;
      e.preventDefault();
      create.mutate("");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [create]);

  if (connectCode(chats.error) === Code.FailedPrecondition) {
    return (
      <div className="p-6">
        <Empty
          title="The chat is not available on this conductor"
          hint="podium-agent serves it with no extra configuration. See docs/agent.md#chat."
        />
      </div>
    );
  }
  if (connectCode(chats.error) === Code.Unauthenticated) {
    return (
      <div className="p-6">
        <Empty
          title="The conductor does not know who you are"
          hint="A chat belongs to a login, and podium-server asserts it. Reload, and check that this control plane's identity middleware is configured."
        />
      </div>
    );
  }
  if (chats.isError && !isAgentUnreachable(chats.error)) {
    return (
      <div className="p-6">
        <Empty title="Could not read your chats" hint={errorMessage(chats.error)} />
      </div>
    );
  }

  const list = chats.data?.chats ?? [];
  const deleteStopsTask = pendingDelete?.turnRunning === true;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The pane has no page gutter of its own, so the banner is chrome across the top of
          it — flush with the rail and the composer — rather than a card floating in from
          a padding nobody else on this screen uses. */}
      {isAgentUnreachable(chats.error) ? (
        <div className="shrink-0 border-b border-border bg-panel px-5 py-3">
          <ConductorDown
            className="max-w-none"
            what="Your chats could not be read"
            onRetry={() => void chats.refetch()}
            retrying={chats.isFetching}
          />
        </div>
      ) : null}

      <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
        <ChatRail
          chats={list}
          active={active}
          loading={chats.isPending}
          onNew={() => create.mutate("")}
          creating={create.isPending}
          onOpen={(id) => navigate(`/agent/chat/${id}`)}
          onRename={renameChat}
          onDelete={setPendingDelete}
          deletingId={remove.isPending ? remove.variables?.id : undefined}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg">
          {active === "" ? (
            <div className="grid min-h-0 flex-1 place-items-center p-6">
              <Empty
                className="max-w-lg"
                icon={Sparkles}
                title={list.length === 0 ? "No chats yet" : "Pick a chat, or start a new one"}
                hint="A question here runs as a real Podium task on a node, with the tools its playbook allows. It answers with what it found, and shows the work."
                action={
                  <Button size="sm" disabled={create.isPending} onClick={() => create.mutate("")}>
                    <MessageSquarePlus />
                    {create.isPending ? "Opening…" : "New chat"}
                  </Button>
                }
              />
            </div>
          ) : (
            // Keyed on the chat: a switch remounts the conversation, so its playbook choice
            // and scroll position start fresh without an effect resetting them.
            <Conversation
              key={active}
              chatId={active}
              storedPlaybook={list.find((c) => c.id === active)?.playbook ?? ""}
              title={list.find((c) => c.id === active)?.title ?? ""}
              onRename={(title) => renameChat(active, title)}
              botName={playbooks.data?.profileDisplayName ?? "Podium"}
              playbooks={playbooks.data?.playbooks ?? []}
              chatDefaultPlaybook={playbooks.data?.playbooks.find((s) => s.chatDefault)?.name ?? ""}
            />
          )}
        </div>
      </div>

      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {deleteStopsTask
                ? `Stop the task and delete ${pendingDelete.title}?`
                : `Delete ${pendingDelete?.title}?`}
            </DialogTitle>
            <DialogDescription>
              The conversation goes with it. There is no undo, and nothing else holds a copy.
            </DialogDescription>
          </DialogHeader>
          {deleteStopsTask ? (
            <Alert variant="warn" role="note">
              A task is running in this chat. Confirming asks the node to stop it — SIGTERM, then
              up to 30 seconds — and then deletes the conversation.
            </Alert>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setPendingDelete(null)}>
              Keep
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              data-testid="chat-delete-confirm"
              aria-label={
                deleteStopsTask
                  ? `Stop the task and delete ${pendingDelete.title}`
                  : pendingDelete
                    ? `Confirm deleting ${pendingDelete.title}`
                    : undefined
              }
              disabled={remove.isPending}
              onClick={() => {
                if (pendingDelete) remove.mutate(pendingDelete);
              }}
            >
              {remove.isPending
                ? deleteStopsTask
                  ? "Stopping…"
                  : "Deleting…"
                : deleteStopsTask
                  ? "Stop and delete"
                  : "Delete chat"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** when is the instant a chat was last touched, for ordering the rail by recency. */
function when(c: Chat): number {
  return toDate(c.lastMessageAt ?? c.createdAt)?.getTime() ?? 0;
}

function ChatRail({
  chats,
  active,
  loading,
  onNew,
  creating,
  onOpen,
  onRename,
  onDelete,
  deletingId,
}: {
  chats: Chat[];
  active: string;
  loading: boolean;
  onNew: () => void;
  creating: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
  onDelete: (chat: Chat) => void;
  deletingId?: string;
}) {
  // Newest first. The server's order is not part of the contract, and "what I was just
  // doing" is the only order a chat list is ever read in.
  const ordered = useMemo(() => chats.slice().sort((a, b) => when(b) - when(a)), [chats]);
  const activeChat = ordered.find((c) => c.id === active);

  return (
    <div className="flex w-full min-h-0 shrink-0 flex-col border-b border-border bg-panel sm:w-72 sm:self-stretch sm:border-r sm:border-b-0">
      <div className="flex items-center gap-2 px-3 py-3">
        <p className="text-2xs font-medium tracking-wider text-faint uppercase">Chats</p>
        {ordered.length > 0 ? (
          <span className="tabular text-2xs text-faint">{ordered.length}</span>
        ) : null}
        <Tooltip label="New chat — or press N">
          <Button
            type="button"
            size="sm"
            data-testid="chat-new"
            disabled={creating}
            onClick={onNew}
            className="ml-auto"
          >
            <MessageSquarePlus />
            {creating ? "Opening…" : "New chat"}
          </Button>
        </Tooltip>
      </div>

      {/* On a narrow viewport the rail is a select rather than a drawer: one control, no
          overlay, and the keyboard works. */}
      <div className="mx-3 mb-3 flex items-center gap-2 sm:hidden">
        <select
          aria-label="Chat"
          value={active}
          onChange={(e) => onOpen(e.target.value)}
          className="min-w-0 flex-1 rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-fg"
        >
          <option value="">Pick a chat…</option>
          {ordered.map((c) => (
            <option key={c.id} value={c.id}>
              {c.title}
            </option>
          ))}
        </select>
        {activeChat ? (
          <Tooltip label={`Delete ${activeChat.title}`}>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              data-testid="chat-delete-mobile"
              aria-label={`Delete ${activeChat.title}`}
              disabled={deletingId === activeChat.id}
              onClick={() => onDelete(activeChat)}
              className="hover:bg-err/12 hover:text-err"
            >
              <Trash2 />
            </Button>
          </Tooltip>
        ) : null}
      </div>

      <ul
        data-testid="chat-list"
        className="hidden min-h-0 flex-1 space-y-0.5 overflow-y-auto px-2 pb-3 sm:block sm:h-0"
      >
        {loading
          ? Array.from({ length: 4 }, (_, i) => (
              <li key={i} className="space-y-1.5 px-2.5 py-2" aria-hidden>
                <Skeleton className="h-3.5 w-3/5" />
                <Skeleton className="h-3 w-4/5" />
              </li>
            ))
          : null}
        {!loading && ordered.length === 0 ? (
          <li className="px-2.5 py-2 text-xs leading-relaxed text-muted">
            No chats yet. Start one and it appears here, newest first.
          </li>
        ) : null}
        {ordered.map((c) => (
          <ChatRow
            key={c.id}
            chat={c}
            active={c.id === active}
            onOpen={onOpen}
            onRename={onRename}
            onDelete={onDelete}
            deleting={deletingId === c.id}
          />
        ))}
      </ul>
    </div>
  );
}

function ChatRow({
  chat,
  active,
  onOpen,
  onRename,
  onDelete,
  deleting,
}: {
  chat: Chat;
  active: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
  onDelete: (chat: Chat) => void;
  deleting: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(chat.title);
  const [saving, setSaving] = useState(false);
  const ignoreBlur = useRef(false);

  const start = () => {
    ignoreBlur.current = false;
    setDraft(chat.title);
    setEditing(true);
  };

  const cancel = () => {
    ignoreBlur.current = true;
    setDraft(chat.title);
    setEditing(false);
  };

  const submit = async () => {
    const next = draft.trim();
    if (next === "" || next === chat.title) {
      cancel();
      return;
    }
    setSaving(true);
    try {
      await onRename(chat.id, next);
      ignoreBlur.current = true;
      setEditing(false);
    } catch {
      // The mutation already toasted.
    } finally {
      setSaving(false);
    }
  };

  if (editing) {
    return (
      <li>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          className="px-2.5 py-2"
        >
          <Input
            data-testid="chat-title-input"
            aria-label="Chat title"
            value={draft}
            maxLength={MAX_CHAT_TITLE}
            disabled={saving}
            autoFocus
            onFocus={(e) => e.currentTarget.select()}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              if (!ignoreBlur.current) void submit();
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                cancel();
              }
            }}
            className="h-7 px-2"
          />
        </form>
      </li>
    );
  }

  return (
    <li
      className={`group relative flex items-stretch rounded-md ${
        active
          ? "bg-raised after:absolute after:inset-y-1.5 after:left-0 after:w-0.5 after:rounded-full after:bg-accent"
          : "hover:bg-raised/60"
      }`}
    >
      <button
        type="button"
        onClick={() => onOpen(chat.id)}
        onDoubleClick={(e) => {
          e.preventDefault();
          start();
        }}
        aria-current={active ? "true" : undefined}
        className="min-w-0 flex-1 rounded-md px-2.5 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <span className="flex items-baseline gap-2">
          <span
            className={`min-w-0 flex-1 truncate text-sm ${active ? "font-medium text-fg" : "text-fg"}`}
          >
            {chat.title}
          </span>
          <span
            className="tabular shrink-0 text-2xs text-faint"
            title={absolute(chat.lastMessageAt ?? chat.createdAt)}
          >
            {relative(chat.lastMessageAt ?? chat.createdAt)}
          </span>
        </span>
        <span className="mt-1 flex items-center gap-1.5">
          {chat.turnRunning ? <Badge tone="run">running</Badge> : null}
          {chat.playbook ? (
            <span className="font-mono shrink-0 text-2xs text-faint">/{chat.playbook}</span>
          ) : null}
          <span className="min-w-0 flex-1 truncate text-xs text-muted">
            {chat.preview || "nothing said yet"}
          </span>
        </span>
      </button>
      <div className="m-1 flex shrink-0 self-start">
        <Tooltip label="Rename">
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            data-testid="chat-rename"
            aria-label={`Rename ${chat.title}`}
            onClick={start}
          >
            <Pencil />
          </Button>
        </Tooltip>
        <Tooltip label={`Delete ${chat.title}`}>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            data-testid="chat-delete"
            aria-label={`Delete ${chat.title}`}
            disabled={deleting}
            onClick={() => onDelete(chat)}
            className="hover:bg-err/12 hover:text-err"
          >
            <Trash2 />
          </Button>
        </Tooltip>
      </div>
    </li>
  );
}

function ConversationTitle({
  title,
  onRename,
}: {
  title: string;
  onRename: (title: string) => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(title);
  const [saving, setSaving] = useState(false);
  const ignoreBlur = useRef(false);

  const start = () => {
    if (!title) return;
    ignoreBlur.current = false;
    setDraft(title);
    setEditing(true);
  };

  const cancel = () => {
    ignoreBlur.current = true;
    setDraft(title);
    setEditing(false);
  };

  const submit = async () => {
    const next = draft.trim();
    if (next === "" || next === title) {
      cancel();
      return;
    }
    setSaving(true);
    try {
      await onRename(next);
      ignoreBlur.current = true;
      setEditing(false);
    } catch {
      // The mutation already toasted.
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex h-10 shrink-0 items-center border-b border-hairline px-5">
      {editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          className="min-w-0 flex-1"
        >
          <Input
            data-testid="chat-title-input"
            aria-label="Chat title"
            value={draft}
            maxLength={MAX_CHAT_TITLE}
            disabled={saving}
            autoFocus
            onFocus={(e) => e.currentTarget.select()}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              if (!ignoreBlur.current) void submit();
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                cancel();
              }
            }}
            className="h-7 px-2"
          />
        </form>
      ) : (
        <button
          type="button"
          data-testid="chat-title"
          onClick={start}
          title="Rename"
          disabled={!title}
          className="min-w-0 truncate rounded-md text-left text-sm font-medium text-fg outline-none hover:text-accent focus-visible:ring-2 focus-visible:ring-ring/50 disabled:text-muted"
        >
          {title || "Chat"}
        </button>
      )}
    </div>
  );
}

function Conversation({
  chatId,
  storedPlaybook,
  title,
  onRename,
  botName,
  playbooks,
  chatDefaultPlaybook,
}: {
  chatId: string;
  storedPlaybook: string;
  title: string;
  onRename: (title: string) => Promise<void>;
  botName: string;
  playbooks: Playbook[];
  chatDefaultPlaybook: string;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const stream = useChatStream(chatId);
  // Undefined means "whatever the profile says", which is not known until ListPlaybooks
  // answers — so the choice is derived rather than copied into state on arrival. A playbook
  // the chat already ran is knowledge, not a preference, and wins.
  const [chosen, setChosen] = useState<string | undefined>(storedPlaybook || undefined);
  const playbook = stream.chat?.playbook || chosen || storedPlaybook || chatDefaultPlaybook;
  const locked = (stream.chat?.playbook || storedPlaybook) !== "";
  const [pinned, setPinned] = useState(true);
  const pinnedRef = useRef(true);
  const lastTop = useRef(0);
  const scroller = useRef<HTMLDivElement>(null);
  const transcript = useRef<HTMLDivElement>(null);

  const pin = (next: boolean) => {
    pinnedRef.current = next;
    setPinned(next);
  };

  const stick = useCallback(() => {
    const el = scroller.current;
    if (!el || !pinnedRef.current) return;
    el.scrollTop = el.scrollHeight;
    lastTop.current = el.scrollTop;
  }, []);

  useEffect(() => {
    if (!stream.chat) return;
    void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
  }, [qc, stream.chat]);

  // The choice is sticky across messages, the way every chat that has a model picker
  // behaves: you pick once and keep asking. It is still sent per message, so nothing is
  // remembered server-side and a reload goes back to the playbook's own model.
  const [choice, setChoice] = useState<AgentChoice>(INHERIT);
  const { agents } = useAgents();

  const send = useMutation({
    mutationFn: (v: { text: string; choice: AgentChoice }) =>
      agent.sendChatMessage({
        chatId,
        text: v.text,
        playbook,
        // Empty fields mean "the playbook's", which is exactly what the server does with them.
        agent: v.choice.agent,
        model: v.choice.model,
        effort: v.choice.effort,
      }),
    onSuccess: () => {
      pin(true);
      void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
    },
    onError: (err) => {
      // The composer is disabled while a turn runs, so this is the race rather than the
      // rule: another tab got there first.
      if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
        toast("A turn is already running in this chat. Wait for the answer.");
        return;
      }
      toast(errorMessage(err));
    },
  });

  // Layout, not paint: a replayed transcript is already taller than the viewport, and an
  // effect would flash the top of the conversation before jumping. Images then grow the
  // column after commit — ResizeObserver is what keeps a pinned view on the latest turn.
  useLayoutEffect(() => {
    stick();
  }, [pinned, stick, stream.messages, stream.progress]);

  useLayoutEffect(() => {
    const el = transcript.current;
    if (!el) return;
    const ro = new ResizeObserver(() => stick());
    ro.observe(el);
    return () => ro.disconnect();
  }, [stick, stream.gone]);

  const runs = useMemo(() => runsOf(stream.messages), [stream.messages]);
  const busy = stream.running || send.isPending;
  const connecting = stream.phase === "connecting" && stream.messages.length === 0 && !stream.gone;

  if (stream.gone) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <Empty
          className="max-w-lg"
          icon={Sparkles}
          title="This chat is gone"
          hint="It was deleted, or it was never yours. Pick another, or start a new one."
          action={
            <Button size="sm" onClick={() => navigate("/agent/chat")}>
              Back to chats
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <>
      <ConversationTitle title={title} onRename={onRename} />
      <ChatPullRequests chatId={chatId} pullRequests={stream.pullRequests} />
      <div className="relative min-h-0 flex-1">
        <div
          ref={scroller}
          data-testid="chat-scroller"
          onScroll={(e) => {
            const el = e.currentTarget;
            const atBottom =
              el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_SLACK_PX;
            // Content growing (an image decoding) leaves scrollTop where it was and is
            // not a human scrolling up — unpinning on that would leave a pinned open
            // sitting in the middle of the replay.
            if (atBottom) pin(true);
            else if (el.scrollTop + 1 < lastTop.current) pin(false);
            lastTop.current = el.scrollTop;
          }}
          className="absolute inset-0 overflow-y-auto [overflow-anchor:none]"
        >
          <div ref={transcript} className="mx-auto w-full max-w-3xl space-y-5 px-5 py-6">
            {/* In the flow rather than floating over it: an overlay at the top of the
                scroll port reads the transcript's first message straight through. */}
            {busy ? (
              <div className="sticky top-2 z-10 flex justify-center">
                <RunningTurn progress={stream.progress} taskId={stream.taskId} />
              </div>
            ) : null}

            {connecting ? <TranscriptSkeleton /> : null}

            {stream.error ? (
              <Alert variant="warn" title="The chat stream dropped and is reconnecting">
                {stream.error}. Nothing was lost — the reconnect replays from the last message
                this browser saw.
              </Alert>
            ) : null}

            {!connecting && stream.messages.length === 0 ? (
              <FirstMessage botName={botName} />
            ) : null}

            {stream.messages.map((m, i) => (
              <Turn key={String(m.seq)} message={m} botName={botName} firstOfRun={runs[i]} />
            ))}
          </div>
        </div>

        {!pinned ? (
          <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => pin(true)}
              className="pointer-events-auto animate-in fade-in-0 slide-in-from-bottom-2 rounded-full shadow-md"
            >
              <ArrowDown />
              Jump to latest
            </Button>
          </div>
        ) : null}
      </div>

      <ChatComposer
        playbooks={playbooks}
        playbook={playbook}
        onPlaybookChange={setChosen}
        playbookLocked={locked}
        disabled={busy}
        agents={agents}
        choice={choice}
        onChoiceChange={setChoice}
        onSend={(text, choice) => send.mutate({ text, choice })}
      />
    </>
  );
}

/**
 * RunningTurn is that a turn is running, and the task it is running in. Nothing more: what
 * the task is actually doing is in the transcript below, in its own words, so this is a
 * state and not a message — which is why it is a pill that sticks to the top of the
 * conversation rather than a line of prose stealing the newest thing said.
 *
 * The progress line it does show is the conductor's placeholder, which fills the gap
 * between a turn starting and the task's first words — a container still being pulled has
 * nothing to say yet, and neither does an empty pill.
 */
function RunningTurn({ progress, taskId }: { progress?: string; taskId?: string }) {
  return (
    <div
      data-testid="chat-progress"
      className="flex min-w-0 items-center gap-2 rounded-full border border-border bg-panel/95 py-1 pr-3 pl-1.5 shadow-md backdrop-blur animate-in fade-in-0 slide-in-from-top-1"
    >
      <Badge tone="run">running</Badge>
      <span className="min-w-0 truncate text-xs text-muted">{progress ?? "Working on it…"}</span>
      {taskId ? (
        <Link
          to={`/tasks/${taskId}`}
          title={taskId}
          className="shrink-0 border-l border-hairline pl-2 font-mono text-2xs text-accent hover:underline"
        >
          {taskId}
        </Link>
      ) : null}
    </div>
  );
}

function FirstMessage({ botName }: { botName: string }) {
  return (
    <div className="flex flex-col items-center gap-3 py-10 text-center">
      <span className="grid size-10 place-items-center rounded-xl border border-border bg-panel text-accent">
        <Sparkles className="size-5" />
      </span>
      <p className="text-sm font-medium text-fg">Ask {botName} something</p>
      <p className="max-w-md text-xs leading-relaxed text-muted">
        Each question runs as one Podium task and exits when it has an answer. Try{" "}
        <span className="text-fg">why did the nightly ETL fail?</span> — or pick a playbook below
        to change which image, tools and model the turn runs with.
      </p>
    </div>
  );
}

function TranscriptSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading the conversation" className="space-y-5">
      <div className="flex justify-end">
        <Skeleton className="h-9 w-64 rounded-xl" />
      </div>
      <div className="flex gap-3">
        <Skeleton className="size-7 shrink-0 rounded-lg" />
        <div className="min-w-0 flex-1 space-y-2">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-3.5 w-full" />
          <Skeleton className="h-3.5 w-11/12" />
          <Skeleton className="h-3.5 w-2/3" />
        </div>
      </div>
    </div>
  );
}

/**
 * runsOf marks the first message of each run by one author, so a name is a label above a
 * run rather than a repeat above every bubble. Role is author here: the task narrating its
 * work and the bot answering are two voices, and which one is speaking is the thing the
 * name is there to say.
 */
function runsOf(messages: ChatMessage[]): boolean[] {
  return messages.map((m, i) => i === 0 || messages[i - 1].role !== m.role);
}

/**
 * Turn renders one message, and a question is built differently from everything else on
 * purpose. A question is short and is scanned for, so it is a bubble on the right. Anything
 * said back is a document — headings, lists, diffs — so it runs the full measure of the
 * column under a name, where markdown has room to read as markdown rather than as chat.
 *
 * A `progress` message reads exactly like an answer, because that is what it is: the words
 * the task said on its way there. The name above the run is what separates them — the task
 * narrating its work, then the bot with the answer — rather than a quieter typography,
 * which would make the transcript look like it had a margin of asides in it.
 */
function Turn({
  message,
  botName,
  firstOfRun,
}: {
  message: ChatMessage;
  botName: string;
  firstOfRun: boolean;
}) {
  if (message.role === "user") {
    return (
      <div className="flex justify-end">
        <div
          data-testid="chat-message"
          data-role={message.role}
          title={absolute(message.ts)}
          className="min-w-0 max-w-[85%] rounded-xl rounded-br-sm border border-accent/25 bg-accent/12 px-3.5 py-2.5"
        >
          <ChatMarkdown text={message.text} keyPrefix={`m${message.seq}-`} />
          <ChatAttachments attachments={message.attachments} />
        </div>
      </div>
    );
  }

  // Who is talking. The task is the container doing the work and the bot is what answers
  // with it; the answer is relayed through the same task, so this is the honest half of the
  // distinction: a line while the work is still going, or the thing it came back with.
  const fromTask = message.role === "progress";

  return (
    <div className="flex gap-3">
      <span
        aria-hidden
        className={`mt-0.5 grid size-7 shrink-0 place-items-center rounded-lg ${
          firstOfRun ? `border border-border bg-panel ${fromTask ? "text-muted" : "text-accent"}` : ""
        }`}
      >
        {firstOfRun ? fromTask ? <Terminal className="size-4" /> : <Bot className="size-4" /> : null}
      </span>
      <div className="min-w-0 flex-1 space-y-1.5">
        {firstOfRun ? (
          <div className="flex items-baseline gap-2">
            <span className="text-xs font-medium text-fg">{fromTask ? "task" : botName}</span>
            <span className="text-2xs text-faint" title={absolute(message.ts)}>
              {relative(message.ts)}
            </span>
          </div>
        ) : null}
        <div data-testid="chat-message" data-role={message.role} className="min-w-0">
          <ChatMarkdown
            text={message.text}
            keyPrefix={`m${message.seq}-`}
            className={fromTask ? "text-muted" : undefined}
          />
          <ChatAttachments attachments={message.attachments} />
        </div>
      </div>
    </div>
  );
}
