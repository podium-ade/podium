import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import {
  Ellipsis,
  Hash,
  MessageSquarePlus,
  Pencil,
  Search,
  Sparkles,
  SquarePen,
  Trash2,
} from "lucide-react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  type Assistant,
  type Chat,
} from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { useChatStream } from "../../hooks/useChatStream";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { relative, toDate } from "../../lib/format";
import { cn } from "../../lib/utils";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { Input } from "../ui/input";
import { Kbd } from "../ui/kbd";
import { Tooltip } from "../ui/tooltip";
import { ChatComposer } from "./ChatComposer";
import { ChatPullRequests } from "./ChatPullRequests";
import { ConductorDown } from "./ConductorDown";
import { toThread } from "../../lib/chatThread";

// The thread, its markdown and its highlighter are most of this screen's weight and none of
// any other's, so they load with the first conversation opened.
const ChatThread = lazy(() => import("./chat/ChatThread").then((m) => ({ default: m.ChatThread })));

/** Matches store.MaxChatTitleRunes — the input refuses more, the server does too. */
const MAX_CHAT_TITLE = 80;

/**
 * ChatPanel is the web chat: the third place a turn can start from, and the only one whose
 * transcript Podium itself holds.
 *
 * EVERYTHING IN A BUBBLE IS CONTENT. A human wrote the questions and a task wrote the
 * answers; this screen renders both through chat/MarkdownText.tsx, which emits no raw HTML
 * and makes any link scheme but a web one inert. Nothing here interprets what an agent said.
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
  // What the assistant may delegate to. It is shown, never picked: the turn chooses a
  // playbook per piece of work, and naming them is how a reader learns the conversation can
  // reach a machine at all.
  const playbookNames = (playbooks.data?.playbooks ?? []).map((p) => p.name);
  const deleteStopsTask = pendingDelete?.turnRunning === true || pendingDelete?.taskRunning === true;

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
                className="max-w-lg border-0 bg-transparent px-4 py-10"
                icon={Sparkles}
                title={list.length === 0 ? "No chats yet" : "Pick a chat, or start a new one"}
                hint={assistantHint(playbooks.data?.assistant?.displayName, playbookNames)}
                action={
                  <Button size="sm" disabled={create.isPending} onClick={() => create.mutate("")}>
                    <MessageSquarePlus />
                    {create.isPending ? "Opening…" : "New chat"}
                  </Button>
                }
              />
            </div>
          ) : (
            // Keyed on the chat: a switch remounts the conversation, so its model choice
            // and scroll position start fresh without an effect resetting them.
            <Conversation
              key={active}
              chatId={active}
              title={list.find((c) => c.id === active)?.title ?? ""}
              remembered={storedChoice(list.find((c) => c.id === active))}
              listedOrigin={list.find((c) => c.id === active)?.origin}
              listedChannel={list.find((c) => c.id === active)?.channel}
              listedEmpty={(list.find((c) => c.id === active)?.preview ?? "") === ""}
              onRename={(title) => renameChat(active, title)}
              assistant={playbooks.data?.assistant}
              playbookNames={playbookNames}
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
              A task is running in this chat. Confirming asks the node to stop it (SIGTERM, then
              up to 30 seconds) and then deletes the conversation.
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

function startOfDay(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

/** recencyLabel is Today / Yesterday / Previous 7 days / Older, newest-first grouping. */
function recencyLabel(ms: number, now = Date.now()): string {
  const today = startOfDay(now);
  if (ms >= today) return "Today";
  if (ms >= today - 86_400_000) return "Yesterday";
  if (ms >= today - 7 * 86_400_000) return "Previous 7 days";
  return "Older";
}

function groupChats(chats: Chat[]): { label: string; chats: Chat[] }[] {
  const groups: { label: string; chats: Chat[] }[] = [];
  for (const c of chats) {
    const label = recencyLabel(when(c));
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.chats.push(c);
    else groups.push({ label, chats: [c] });
  }
  return groups;
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
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") return ordered;
    return ordered.filter(
      (c) => c.title.toLowerCase().includes(q) || c.preview.toLowerCase().includes(q),
    );
  }, [ordered, query]);
  const groups = useMemo(() => groupChats(filtered), [filtered]);

  return (
    <div className="flex w-full min-h-0 shrink-0 flex-col border-b border-border bg-sidebar sm:w-64 sm:self-stretch sm:border-r sm:border-b-0">
      <div className="flex flex-col gap-1 px-2 pt-3 pb-2">
        <button
          type="button"
          data-testid="chat-new"
          disabled={creating}
          onClick={onNew}
          className="group/new flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium text-fg transition-colors hover:bg-raised/70 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none disabled:opacity-60"
        >
          <SquarePen className="size-4 text-muted group-hover/new:text-fg" />
          <span className="flex-1 text-left">{creating ? "Opening…" : "New chat"}</span>
          <Kbd className="opacity-0 transition-opacity group-hover/new:opacity-100">N</Kbd>
        </button>
        {ordered.length > 4 ? (
          <label className="relative block">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-faint" />
            <input
              data-testid="chat-search"
              aria-label="Search chats"
              value={query}
              placeholder="Search chats"
              onChange={(e) => setQuery(e.target.value)}
              className="h-9 w-full rounded-lg border border-transparent bg-transparent pr-2.5 pl-9 text-sm text-fg placeholder:text-faint transition-colors outline-none hover:bg-raised/70 focus:border-border focus:bg-bg"
            />
          </label>
        ) : null}
      </div>

      {/* On a narrow viewport the rail is a select rather than a drawer: one control, no
          overlay, and the keyboard works. */}
      <div className="mx-3 mb-3 flex items-center gap-2 sm:hidden">
        <select
          aria-label="Chat"
          value={active}
          onChange={(e) => onOpen(e.target.value)}
          className="min-w-0 flex-1 rounded-lg border border-border bg-bg px-2.5 py-2 text-sm text-fg"
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
        className="hidden min-h-0 flex-1 overflow-y-auto px-2 pb-3 sm:block sm:h-0"
      >
        {loading
          ? Array.from({ length: 6 }, (_, i) => (
              <li key={i} className="flex h-9 items-center px-2.5" aria-hidden>
                <Skeleton className="h-3.5" style={{ width: `${55 + ((i * 17) % 35)}%` }} />
              </li>
            ))
          : null}
        {!loading && ordered.length === 0 ? (
          <li className="px-2.5 py-2 text-sm leading-relaxed text-muted">
            No chats yet. Start one and it appears here, newest first.
          </li>
        ) : null}
        {!loading && ordered.length > 0 && filtered.length === 0 ? (
          <li className="px-2.5 py-2 text-sm text-muted">No chats match.</li>
        ) : null}
        {groups.map((g) => (
          <li key={g.label} className="pt-4 first:pt-1">
            <p className="px-2.5 pb-1 text-xs font-medium text-faint">{g.label}</p>
            <ul className="space-y-px">
              {g.chats.map((c) => (
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
          </li>
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
            className="h-9 rounded-lg px-2.5 text-sm"
          />
        </form>
      </li>
    );
  }

  // Busy while the assistant is answering OR a task it delegated is still going: the turn
  // ends the moment it has delegated, the work does not.
  const running = chat.turnRunning || chat.taskRunning;
  // Where the conversation lives. A mirrored thread is read here and answered there, and
  // the mark is what stops a reader wondering why it has no composer.
  const mirrored = chat.origin !== "" && chat.origin !== "web";
  const where = mirrored ? (chat.channel ? `#${chat.channel}` : chat.origin) : "";
  const hint = [where, chat.startedBy, chat.preview, relative(chat.lastMessageAt ?? chat.createdAt)]
    .filter(Boolean)
    .join(" · ");

  return (
    <li
      className={cn(
        "group relative flex h-9 items-center rounded-lg transition-colors",
        active ? "bg-raised" : "hover:bg-raised/60",
      )}
    >
      <button
        type="button"
        onClick={() => onOpen(chat.id)}
        onDoubleClick={(e) => {
          e.preventDefault();
          start();
        }}
        title={hint}
        aria-current={active ? "true" : undefined}
        className="flex h-full min-w-0 flex-1 items-center gap-2 rounded-lg px-2.5 text-left outline-none group-focus-within:pr-8 group-hover:pr-8 has-[~[data-state=open]]:pr-8 focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {running ? (
          <span className="relative flex size-2 shrink-0" aria-hidden>
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-run opacity-60" />
            <span className="relative inline-flex size-2 rounded-full bg-run" />
          </span>
        ) : mirrored ? (
          <Hash className="size-3.5 shrink-0 text-faint" aria-hidden />
        ) : null}
        <span className={cn("min-w-0 flex-1 truncate text-sm", active ? "text-fg" : "text-fg/85")}>
          {chat.title}
        </span>
        {running ? <span className="sr-only">running</span> : null}
        {where ? <span className="sr-only">{where}</span> : null}
        {chat.startedBy ? <span className="sr-only">started by {chat.startedBy}</span> : null}
        {chat.preview ? <span className="sr-only">{chat.preview}</span> : null}
      </button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            data-testid="chat-actions"
            aria-label={`Actions for ${chat.title}`}
            className="absolute right-1 grid size-7 place-items-center rounded-md text-muted opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 hover:bg-border/60 hover:text-fg focus-visible:opacity-100 data-[state=open]:bg-border/60 data-[state=open]:opacity-100"
          >
            <Ellipsis className="size-4" />
          </button>
        </DropdownMenuTrigger>
        {/* Rename swaps the row for an input; handing focus back to the trigger would blur
            it the moment it appears. */}
        <DropdownMenuContent align="start" className="min-w-36" onCloseAutoFocus={(e) => e.preventDefault()}>
          <DropdownMenuItem data-testid="chat-rename" aria-label={`Rename ${chat.title}`} onSelect={start}>
            <Pencil />
            Rename
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="danger"
            data-testid="chat-delete"
            aria-label={`Delete ${chat.title}`}
            disabled={deleting}
            onSelect={() => onDelete(chat)}
          >
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

function ConversationTitle({
  title,
  channel,
  onRename,
}: {
  title: string;
  channel?: string;
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
    <div className="flex h-12 shrink-0 items-center gap-2 border-b border-hairline px-4 sm:px-5">
      {channel ? (
        <span
          data-testid="chat-channel"
          className="shrink-0 rounded-md bg-raised px-1.5 py-0.5 font-mono text-xs text-muted"
        >
          #{channel}
        </span>
      ) : null}
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
            className="h-8 px-2"
          />
        </form>
      ) : (
        <button
          type="button"
          data-testid="chat-title"
          onClick={start}
          title="Rename"
          disabled={!title}
          className="min-w-0 truncate rounded-md text-left text-base font-medium tracking-tight text-fg outline-none hover:text-accent focus-visible:ring-2 focus-visible:ring-ring/50 disabled:text-muted"
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
  remembered,
  listedOrigin,
  listedChannel,
  listedEmpty,
  onRename,
  assistant,
  playbookNames,
}: {
  chatId: string;
  title: string;
  /** remembered is what this chat was last answered on, from the list row. */
  remembered?: AgentChoice;
  /**
   * listedOrigin is where the conversation lives, as the LIST row already said. The stream
   * says so too, but only once it is open — and deriving "is this read-only" from the stream
   * alone rendered a composer for a mirrored thread and then took it away again.
   */
  listedOrigin?: string;
  /** listedChannel is the Slack channel name the list row already has, if any. */
  listedChannel?: string;
  /**
   * listedEmpty is true when the list row has no preview yet — a new chat. The connecting
   * skeleton is a fake user bubble and would flash before the greeting.
   */
  listedEmpty?: boolean;
  onRename: (title: string) => Promise<void>;
  assistant?: Assistant;
  playbookNames: string[];
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const stream = useChatStream(chatId);
  const botName = assistant?.displayName ?? "Podium";
  // A MIRRORED conversation is answered where it lives, so this end of it is read-only:
  // no composer, and the note in its place says where to reply. The server agrees — a send
  // to a chat with no owning login is refused — so this is the affordance, not the rule.
  //
  // The list row is consulted FIRST because it has already arrived: opening a mirrored thread
  // used to flash the composer, because the only source of the origin was a stream frame that
  // lands after the first render. The stream still wins once it speaks, for a deep link that
  // has no list row yet.
  const origin = stream.chat?.origin || listedOrigin || "";
  const channel = stream.chat?.channel || listedChannel || "";
  const mirrored = origin !== "" && origin !== "web";
  const participants = stream.chat?.participants ?? [];
  useEffect(() => {
    if (!stream.chat) return;
    void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
  }, [qc, stream.chat]);

  // What answers this conversation. Picked once and then remembered: the chat row stores the
  // override, so a reload, another tab and coming back tomorrow all open on the model this
  // chat was last asked for.
  //
  // Derived rather than copied into state on arrival, for the same reason the title is: the
  // row lands asynchronously, and an effect that seeded state from it would either race the
  // first render or overwrite a choice made while it was in flight. So what the PERSON picked
  // in this session wins, and the stored choice is what it falls back to.
  const [picked, setPicked] = useState<AgentChoice | undefined>(undefined);
  const choice = picked ?? storedChoice(stream.chat) ?? remembered ?? INHERIT;
  const setChoice = setPicked;
  const { agents } = useAgents();

  // pendingUser is the question that has been sent but not yet on the stream. Showing it
  // immediately is what stops the greeting (or the previous answer) flashing through the
  // gap between the RPC returning and the stream catching up.
  const [pendingUser, setPendingUser] = useState<{ text: string; before: number } | null>(null);

  const send = useMutation({
    mutationFn: (v: { text: string; choice: AgentChoice }) =>
      agent.sendChatMessage({
        chatId,
        text: v.text,
        // Empty fields mean "the assistant's own", which is exactly what the server does
        // with them.
        agent: v.choice.agent,
        model: v.choice.model,
        effort: v.choice.effort,
      }),
    onMutate: (v) => {
      setPendingUser({
        text: v.text,
        before: stream.messages.filter((m) => m.role === "user").length,
      });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
    },
    onError: (err) => {
      setPendingUser(null);
      // The composer is disabled while a turn runs, so this is the race rather than the
      // rule: another tab got there first.
      if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
        toast("A turn is already running in this chat. Wait for the answer.");
        return;
      }
      toast(errorMessage(err));
    },
  });

  const userCount = stream.messages.filter((m) => m.role === "user").length;
  const last = stream.messages[stream.messages.length - 1];
  const pendingVisible = pendingUser !== null && userCount <= pendingUser.before;
  // Hold the waiting state until the stream says the turn started (or already finished).
  // send.isPending alone drops when the RPC returns, one frame before the status frame.
  const holdBusy =
    pendingUser !== null &&
    !stream.running &&
    !stream.awaiting &&
    last?.role !== "assistant";
  const busy = (stream.running && !stream.awaiting) || send.isPending || holdBusy;
  const connecting =
    stream.phase === "connecting" &&
    stream.messages.length === 0 &&
    !stream.gone &&
    !listedEmpty;
  const showWelcome = !connecting && stream.messages.length === 0 && !pendingVisible;
  const thread = useMemo(
    () =>
      toThread({
        messages: stream.messages,
        busy,
        taskRunning: stream.chat?.taskRunning ?? false,
        pendingUser: pendingVisible ? pendingUser.text : undefined,
      }),
    [stream.messages, busy, stream.chat?.taskRunning, pendingVisible, pendingUser],
  );

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
      <ConversationTitle title={title} channel={channel} onRename={onRename} />
      <ChatPullRequests chatId={chatId} pullRequests={stream.pullRequests} />
      <Suspense fallback={<div className="min-h-0 flex-1" />}>
        <ChatThread
          messages={thread}
          busy={busy}
          botName={botName}
          progress={stream.progress}
          taskId={stream.taskId}
          onSend={(text) => send.mutate({ text, choice })}
          top={
            <>
              {connecting ? <TranscriptSkeleton /> : null}
              {stream.error ? (
                <Alert variant="warn" title="The chat stream dropped and is reconnecting">
                  {stream.error}. Nothing was lost. The reconnect replays from the last message
                  this browser saw.
                </Alert>
              ) : null}
            </>
          }
          welcome={
            showWelcome ? (
              <FirstMessage
                botName={botName}
                playbookNames={playbookNames}
                onSuggest={(text) => send.mutate({ text, choice })}
                suggesting={busy}
              />
            ) : null
          }
        />
      </Suspense>

      {mirrored ? (
        <div
          data-testid="chat-mirrored-note"
          className="border-t border-border bg-panel/40 px-5 py-3 text-xs leading-relaxed text-muted"
        >
          This conversation lives in {origin}. Reply to it there. Podium keeps a copy so it
          can be read here.
          {participants.length > 0 ? <> Taking part: {participants.join(", ")}.</> : null}
        </div>
      ) : (
        <ChatComposer
          disabled={busy}
          agents={agents}
          assistant={assistant}
          choice={choice}
          onChoiceChange={setChoice}
          onSend={(text, choice) => send.mutate({ text, choice })}
        />
      )}
    </>
  );
}

const SUGGESTIONS = [
  "Why did the nightly ETL fail?",
  "Summarise the last failed task",
  "What can you do on this stack?",
];

function FirstMessage({
  botName,
  playbookNames,
  onSuggest,
  suggesting,
}: {
  botName: string;
  playbookNames: string[];
  onSuggest: (text: string) => void;
  suggesting: boolean;
}) {
  return (
    <div className="flex flex-col items-center gap-4 py-12 text-center">
      <span className="grid size-12 place-items-center rounded-2xl border border-border bg-panel text-accent shadow-xs">
        <Sparkles className="size-5" />
      </span>
      <div className="space-y-1.5">
        <p className="text-xl font-medium tracking-tight text-fg">Ask {botName} something</p>
        <p className="mx-auto max-w-md text-sm leading-relaxed text-muted">
          {botName} answers here. When something needs a machine it starts a task on your nodes
          and reports back. You will see each one it runs.
        </p>
      </div>
      <div className="flex max-w-lg flex-wrap justify-center gap-2">
        {SUGGESTIONS.map((prompt) => (
          <button
            key={prompt}
            type="button"
            disabled={suggesting}
            onClick={() => onSuggest(prompt)}
            className="rounded-full border border-border bg-panel px-3 py-1.5 text-xs text-fg shadow-xs transition-colors hover:border-accent/40 hover:bg-raised disabled:opacity-50"
          >
            {prompt}
          </button>
        ))}
      </div>
      {playbookNames.length > 0 ? (
        <p className="max-w-md text-2xs text-faint">
          It can run{" "}
          {playbookNames.map((name, i) => (
            <span key={name}>
              {i > 0 ? ", " : ""}
              <span className="font-mono text-muted">{name}</span>
            </span>
          ))}
          .
        </p>
      ) : null}
    </div>
  );
}

/**
 * assistantHint is the sentence on the no-chat-selected screen. It names the playbooks so
 * that "it starts tasks" is concrete rather than a promise, and it degrades to the sentence
 * alone before ListPlaybooks has answered.
 */
function assistantHint(botName: string | undefined, playbookNames: string[]): string {
  const who = botName ?? "Podium";
  const base = `Ask ${who} anything about your stack. It answers here, and starts a task on your nodes when the work needs a machine.`;
  return playbookNames.length === 0 ? base : `${base} It can run ${playbookNames.join(", ")}.`;
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
 * storedChoice is what a chat row says it is answered on, or undefined when it says nothing.
 *
 * All three fields empty is "the assistant's own", which is what INHERIT already means — so
 * it reads as *nothing stored* rather than as a choice, and the picker falls through to its
 * own default. That keeps one meaning for one state instead of two paths to the same place.
 */
function storedChoice(chat?: Chat): AgentChoice | undefined {
  if (!chat) return undefined;
  // Normalised rather than trusted: the fields are strings on the wire, and a caller holding
  // a partial row must not turn into a choice of three undefineds sent as a model.
  const agent = chat.agent ?? "";
  const model = chat.model ?? "";
  const effort = chat.effort ?? "";
  if (agent === "" && model === "" && effort === "") return undefined;
  return { agent, model, effort };
}
