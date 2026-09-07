import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, Bot, MessageSquarePlus, Pencil, Sparkles } from "lucide-react";
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
import { Input } from "../ui/input";
import { Tooltip } from "../ui/tooltip";
import { ChatAttachments } from "./ChatAttachments";
import { ChatComposer } from "./ChatComposer";
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

  return (
    <div className="flex h-full min-h-0 flex-col">
      {isAgentUnreachable(chats.error) ? (
        <div className="px-4 pt-3">
          <ConductorDown
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
              title={list.find((c) => c.id === active)?.title ?? ""}
              onRename={(title) => renameChat(active, title)}
              botName={playbooks.data?.profileDisplayName ?? "Podium"}
              playbooks={playbooks.data?.playbooks ?? []}
              chatDefaultPlaybook={playbooks.data?.playbooks.find((s) => s.chatDefault)?.name ?? ""}
            />
          )}
        </div>
      </div>
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
}: {
  chats: Chat[];
  active: string;
  loading: boolean;
  onNew: () => void;
  creating: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
}) {
  // Newest first. The server's order is not part of the contract, and "what I was just
  // doing" is the only order a chat list is ever read in.
  const ordered = useMemo(() => chats.slice().sort((a, b) => when(b) - when(a)), [chats]);

  return (
    <div className="flex w-full shrink-0 flex-col border-b border-border bg-panel sm:w-72 sm:border-r sm:border-b-0">
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
      <select
        aria-label="Chat"
        value={active}
        onChange={(e) => onOpen(e.target.value)}
        className="mx-3 mb-3 rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-fg sm:hidden"
      >
        <option value="">Pick a chat…</option>
        {ordered.map((c) => (
          <option key={c.id} value={c.id}>
            {c.title}
          </option>
        ))}
      </select>

      <ul
        data-testid="chat-list"
        className="hidden min-h-0 flex-1 space-y-0.5 overflow-y-auto px-2 pb-3 sm:block"
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
}: {
  chat: Chat;
  active: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
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
    <li className="group relative">
      <button
        type="button"
        onClick={() => onOpen(chat.id)}
        onDoubleClick={(e) => {
          e.preventDefault();
          start();
        }}
        aria-current={active ? "true" : undefined}
        className={`relative block w-full rounded-md px-2.5 py-2 pr-8 text-left transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${
          active
            ? "bg-raised after:absolute after:inset-y-1.5 after:left-0 after:w-0.5 after:rounded-full after:bg-accent"
            : "hover:bg-raised/60"
        }`}
      >
        <span className="flex items-baseline gap-2">
          <span
            className={`min-w-0 flex-1 truncate text-sm ${active ? "font-medium text-fg" : "text-fg"}`}
          >
            {chat.title}
          </span>
          <span
            className={`tabular shrink-0 text-2xs text-faint ${
              active ? "invisible" : "group-hover:invisible group-focus-within:invisible"
            }`}
            title={absolute(chat.lastMessageAt ?? chat.createdAt)}
          >
            {relative(chat.lastMessageAt ?? chat.createdAt)}
          </span>
        </span>
        <span className="mt-1 flex items-center gap-1.5">
          {chat.turnRunning ? <Badge tone="run">running</Badge> : null}
          <span className="min-w-0 flex-1 truncate text-xs text-muted">
            {chat.preview || "nothing said yet"}
          </span>
        </span>
      </button>
      <Tooltip label="Rename">
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          data-testid="chat-rename"
          aria-label={`Rename ${chat.title}`}
          onClick={start}
          className={`absolute top-1.5 right-1 ${
            active ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"
          }`}
        >
          <Pencil />
        </Button>
      </Tooltip>
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
  title,
  onRename,
  botName,
  playbooks,
  chatDefaultPlaybook,
}: {
  chatId: string;
  title: string;
  onRename: (title: string) => Promise<void>;
  botName: string;
  playbooks: Playbook[];
  chatDefaultPlaybook: string;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const stream = useChatStream(chatId);
  // Undefined means "whatever the profile says", which is not known until ListPlaybooks
  // answers — so the choice is derived rather than copied into state on arrival.
  const [chosen, setChosen] = useState<string>();
  const playbook = chosen ?? chatDefaultPlaybook;
  const [pinned, setPinned] = useState(true);
  const scroller = useRef<HTMLDivElement>(null);

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
      setPinned(true);
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

  // Auto-scroll, unless the human has scrolled up to read something.
  useEffect(() => {
    if (!pinned) return;
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [pinned, stream.messages, stream.progress]);

  const runs = useMemo(() => runsOf(stream.messages), [stream.messages]);
  const busy = stream.running || send.isPending;
  const connecting = stream.phase === "connecting" && stream.messages.length === 0;

  return (
    <>
      <ConversationTitle title={title} onRename={onRename} />
      <div className="relative min-h-0 flex-1">
        <div
          ref={scroller}
          onScroll={(e) => {
            const el = e.currentTarget;
            setPinned(el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_SLACK_PX);
          }}
          className="absolute inset-0 overflow-y-auto"
        >
          <div className="mx-auto w-full max-w-3xl space-y-5 px-5 py-6">
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
              onClick={() => setPinned(true)}
              className="pointer-events-auto animate-in fade-in-0 slide-in-from-bottom-2 rounded-full shadow-md"
            >
              <ArrowDown />
              Jump to latest
            </Button>
          </div>
        ) : null}
      </div>

      {busy ? <RunningTurn progress={stream.progress} taskId={stream.taskId} /> : null}

      <ChatComposer
        playbooks={playbooks}
        playbook={playbook}
        onPlaybookChange={setChosen}
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
 * RunningTurn is the live state of a turn: what it is doing right now, and the task it is
 * doing it in. The progress line is ephemeral — it is replaced in place and dropped the
 * moment the answer lands — so it belongs here, above the composer, rather than in the
 * transcript where it would leave a trail of things nobody said.
 */
function RunningTurn({ progress, taskId }: { progress?: string; taskId?: string }) {
  return (
    <div data-testid="chat-progress" className="border-t border-hairline bg-panel">
      <div className="h-0.5 w-full animate-shimmer bg-accent/60" aria-hidden />
      <div className="mx-auto flex w-full max-w-3xl items-center gap-2.5 px-5 py-2">
        <Badge tone="run">running</Badge>
        <span className="min-w-0 flex-1 truncate text-xs text-muted">
          {progress ?? "Working on it…"}
        </span>
        {taskId ? (
          <Link
            to={`/tasks/${taskId}`}
            title={taskId}
            className="shrink-0 font-mono text-2xs text-accent hover:underline"
          >
            {taskId}
          </Link>
        ) : null}
      </div>
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
 * runsOf marks the first message of each run by one speaker, so the bot's name is a label
 * above a run rather than a repeat above every bubble.
 */
function runsOf(messages: ChatMessage[]): boolean[] {
  return messages.map((m, i) => i === 0 || messages[i - 1].role !== m.role);
}

/**
 * Turn renders one message, and the two roles are built differently on purpose. A question
 * is short and is scanned for, so it is a bubble on the right. An answer is a document —
 * headings, lists, diffs — so it runs the full measure of the column under a name, where
 * markdown has room to read as markdown rather than as chat.
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

  return (
    <div className="flex gap-3">
      <span
        aria-hidden
        className={`mt-0.5 grid size-7 shrink-0 place-items-center rounded-lg ${
          firstOfRun ? "border border-border bg-panel text-accent" : ""
        }`}
      >
        {firstOfRun ? <Bot className="size-4" /> : null}
      </span>
      <div className="min-w-0 flex-1 space-y-1.5">
        {firstOfRun ? (
          <div className="flex items-baseline gap-2">
            <span className="text-xs font-medium text-fg">{botName}</span>
            <span className="text-2xs text-faint" title={absolute(message.ts)}>
              {relative(message.ts)}
            </span>
          </div>
        ) : null}
        <div data-testid="chat-message" data-role={message.role} className="min-w-0">
          <ChatMarkdown text={message.text} keyPrefix={`m${message.seq}-`} />
          <ChatAttachments attachments={message.attachments} />
        </div>
      </div>
    </div>
  );
}
