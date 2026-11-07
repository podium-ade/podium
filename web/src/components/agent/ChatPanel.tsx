import { Fragment, lazy, Suspense, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import {
  Building2,
  ChevronDown,
  Ellipsis,
  LogOut,
  Monitor,
  Pencil,
  Plus,
  Search,
  Sparkles,
  Trash2,
  Users,
} from "lucide-react";
import { Link, Navigate, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { markChatSeen } from "../../lib/chatSeen";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  type Assistant,
  type Chat,
  type Personality,
} from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { useChatStream } from "../../hooks/useChatStream";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { relative, toDate } from "../../lib/format";
import { useViewer } from "../../lib/identity";
import { comparePersonalities, inheritedAssistant } from "../../lib/personality";
import { canWrite, roleOf, sharedByLabel, unread } from "../../lib/sharing";
import { cn } from "../../lib/utils";
import { Empty } from "../Empty";
import { PodiumMark } from "../PodiumMark";
import { desktopCandidate, useDesktopTask } from "../../lib/desktop";
import { DesktopPane } from "./DesktopPane";
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
import { Tooltip } from "../ui/tooltip";
import { BrandTile } from "./BrandIcon";
import { ChatComposer } from "./ChatComposer";
import { ChatPullRequests } from "./ChatPullRequests";
import { ChatPeople, ChatSurfaces } from "./chat/ChatPeople";
import { ShareButton } from "./chat/ShareDialog";
import { ConductorDown } from "./ConductorDown";
import { toThread } from "../../lib/chatThread";

const railRow =
  "flex min-h-11 min-w-0 items-center rounded-md border border-transparent px-3 text-sm font-semibold transition-colors duration-150 hover:border-border hover:bg-panel";

function RailHeading({ title, action, className }: { title: string; action?: ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 items-center justify-between gap-2 px-4 pt-4 pb-3", className)}>
      <h2 className="min-w-0 truncate text-xs font-bold tracking-[0.08em] text-muted uppercase">{title}</h2>
      {action}
    </div>
  );
}

/**
 * The first segment of /agent/chat is a chat id. `a` is the one reserved prefix, because
 * chat ids are `chat_` plus a ULID, so a personality can live at /agent/chat/a/:id/:chatId
 * without taking over an existing link.
 */
function parseChatPath(splat: string): { personalityId: string; chatId: string; redirect: boolean } {
  const parts = splat.split("/").filter(Boolean);
  if (parts[0] !== "a") return { personalityId: "", chatId: parts[0] ?? "", redirect: false };
  if (!parts[1]) return { personalityId: "", chatId: "", redirect: true };
  return { personalityId: parts[1], chatId: parts[2] ?? "", redirect: false };
}

function chatPath(personalityId: string, chatId = ""): string {
  if (personalityId === "") return chatId === "" ? "/agent/chat" : `/agent/chat/${chatId}`;
  return chatId === "" ? `/agent/chat/a/${personalityId}` : `/agent/chat/a/${personalityId}/${chatId}`;
}

type VoiceRow = { id: string; label: string; gone?: boolean };

// The thread, its markdown and its highlighter are most of this screen's weight and none of
// any other's, so they load with the first conversation opened.
// The transcript is its own chunk. ChatPanel starts fetching it as soon as it mounts, so a chat
// opened from the arrival does not sit on an empty pane while it downloads.
const loadChatThread = () => import("./chat/ChatThread");
const ChatThread = lazy(() => loadChatThread().then((m) => ({ default: m.ChatThread })));

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
  // The tab's route is `chat/*`. The first segment is the chat id, except `a`, which names
  // a personal assistant: /agent/chat/a/:id and /agent/chat/a/:id/:chatId.
  const splat = useParams()["*"] ?? "";
  useEffect(() => {
    void loadChatThread();
  }, []);
  const route = useMemo(() => parseChatPath(splat), [splat]);
  const active = route.chatId;
  const personalityId = route.personalityId;
  const qc = useQueryClient();
  const toast = useToast();

  const chats = useQuery({
    queryKey: ["agent", "chats", personalityId],
    queryFn: () => agent.listChats({ filterPersonality: true, personalityId }),
    // A turn started in another tab, or by somebody else's browser on the same login,
    // changes turn_running and the preview. Nothing else here would notice.
    refetchInterval: 10_000,
  });
  // Chats shared with every member and not opened yet (phase 5). They are the workspace's,
  // not an assistant's, so they are listed with Podium's chats only.
  const workspace = useQuery({
    queryKey: ["agent", "chats", "workspace"],
    queryFn: () => agent.listChats({ section: "workspace" }),
    enabled: personalityId === "",
    refetchInterval: 30_000,
  });
  const voices = useQuery({
    queryKey: ["agent", "personalities"],
    queryFn: () => agent.listPersonalities({}),
    staleTime: 30_000,
  });
  const playbooks = useQuery({
    queryKey: ["agent", "playbooks"],
    queryFn: () => agent.listPlaybooks({}),
    staleTime: 5 * 60_000,
  });

  // The open chat's latest message has been seen, so the inbox does not announce it.
  useEffect(() => {
    const chat = chats.data?.chats.find((c) => c.id === active);
    if (!chat?.lastMessageAt) return;
    markChatSeen(chat.id, Number(chat.lastMessageAt.seconds) * 1000 + Math.floor(chat.lastMessageAt.nanos / 1e6));
  }, [active, chats.data]);

  const personalities = voices.data?.personalities ?? [];
  // A failed or still-loading list is not "gone". Only a successful list that lacks the id is.
  const assistantGone =
    personalityId !== "" && voices.isSuccess && !personalities.some((p) => p.id === personalityId);
  const blank = chatPath(personalityId);
  const voiceRows = voiceRowsFor(personalities, personalityId, assistantGone, chats.data?.chats ?? []);

  // The first question creates the chat. The Chats +, and N, only return to this
  // arrival. Pressing them here does not insert an empty row. A deleted assistant
  // has nothing new to open.
  const openBlank = () => {
    if (assistantGone) return;
    if (active !== "") navigate(blank);
  };
  const ask = useMutation({
    mutationFn: async (v: { text: string; choice: AgentChoice }) => {
      const res = await agent.createChat(personalityId === "" ? { title: "" } : { title: "", personalityId });
      const id = res.chat?.id ?? "";
      if (id === "") throw new ConnectError("the conductor did not open a chat", Code.Internal);
      try {
        await agent.sendChatMessage({
          chatId: id,
          text: v.text,
          agent: v.choice.agent,
          model: v.choice.model,
          effort: v.choice.effort,
        });
      } catch (err) {
        await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
        navigate(chatPath(personalityId, id));
        throw err;
      }
      return { id, text: v.text };
    },
    onSuccess: async ({ id, text }) => {
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      navigate(chatPath(personalityId, id), { state: { pendingText: text } });
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

  const viewerLogin = useViewer()?.login ?? "";
  const [pendingDelete, setPendingDelete] = useState<Chat | null>(null);
  const remove = useMutation({
    mutationFn: (chat: Chat) => agent.deleteChat({ chatId: chat.id }),
    onSuccess: async (_res, chat) => {
      setPendingDelete(null);
      toast(`${chat.title} deleted.`, "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      if (active === chat.id) navigate(blank);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  // Leaving takes the viewer out of somebody else's chat (phase 5). Speaking in it again,
  // from any surface, brings them back, so there is nothing to confirm.
  const leave = useMutation({
    mutationFn: (chat: Chat) => agent.leaveChat({ chatId: chat.id }),
    onSuccess: async (_res, chat) => {
      toast(`You left ${chat.title}.`, "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      await qc.invalidateQueries({ queryKey: ["agent", "sessions"] });
      if (active === chat.id) navigate(blank);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const renameChat = (id: string, title: string) =>
    rename.mutateAsync({ id, title }).then(() => undefined);

  // n returns to the blank arrival when the composer is not focused. On the arrival
  // itself it does nothing: a second press must not open a second empty chat.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "n" || e.metaKey || e.ctrlKey || e.altKey) return;
      const el = document.activeElement;
      if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) return;
      if (assistantGone || active === "") return;
      e.preventDefault();
      navigate(blank);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [active, assistantGone, blank, navigate]);

  if (route.redirect) return <Navigate to="/agent/chat" replace />;

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
  // The rail shows both: what is in the viewer's lists, and what the workspace shares that
  // they have not opened. A chat in both (just opened) is shown once, from their own list.
  const own = new Set(list.map((c) => c.id));
  const railChats = [...list, ...(workspace.data?.chats ?? []).filter((c) => !own.has(c.id))];
  // What the assistant may delegate to. It is shown, never picked: the turn chooses a
  // playbook per piece of work, and naming them is how a reader learns the conversation can
  // reach a machine at all.
  const playbookNames = (playbooks.data?.playbooks ?? []).map((p) => p.name);
  const selectedVoice = personalities.find((p) => p.id === personalityId);
  const arrivalName = selectedVoice?.displayName || playbooks.data?.assistant?.displayName || "Podium";
  // Bubbles keep Podium's display name. The chip shows this voice's model when it has one.
  const answeredBy = inheritedAssistant(playbooks.data?.assistant, selectedVoice);
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
          chats={railChats}
          active={active}
          loading={chats.isPending}
          onNew={openBlank}
          creating={false}
          newDisabled={assistantGone}
          onOpen={(id) => navigate(chatPath(personalityId, id))}
          onRename={renameChat}
          onDelete={setPendingDelete}
          onLeave={(c) => leave.mutate(c)}
          deletingId={remove.isPending ? remove.variables?.id : undefined}
          viewerLogin={viewerLogin}
          voices={voiceRows}
          voiceId={personalityId}
          onPickVoice={(id) => {
            if (id !== personalityId) navigate(chatPath(id));
          }}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg">
          {assistantGone && active === "" ? (
            <div className="grid min-h-0 flex-1 place-items-center p-6">
              <p className="text-sm text-muted">That assistant was deleted.</p>
            </div>
          ) : active === "" ? (
            <Arrival
              botName={arrivalName}
              playbookNames={playbookNames}
              sending={ask.isPending}
              assistant={answeredBy}
              onSend={(text, choice) => ask.mutate({ text, choice })}
            />
          ) : (
            // Keyed on the chat: a switch remounts the conversation, so its model choice
            // and scroll position start fresh without an effect resetting them.
            <Conversation
              key={active}
              chatId={active}
              title={railChats.find((c) => c.id === active)?.title ?? ""}
              remembered={storedChoice(railChats.find((c) => c.id === active))}
              listedOrigin={railChats.find((c) => c.id === active)?.origin}
              listedChannel={railChats.find((c) => c.id === active)?.channel}
              listedLogin={railChats.find((c) => c.id === active)?.login}
              listedRole={railChats.find((c) => c.id === active)?.myRole}
              listedSharedWithMe={railChats.find((c) => c.id === active)?.sharedWithMe}
              listedMultiplayer={railChats.find((c) => c.id === active)?.multiplayer}
              listedEmpty={railChats.find((c) => c.id === active)?.preview === ""}
              onRename={(title) => renameChat(active, title)}
              onLeave={() => navigate(blank)}
              assistant={answeredBy}
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

/** shortWhen is how long ago, in the fewest characters a row can spare: now, 5m, 3h, 2d, Nov 2. */
function shortWhen(ms: number, now = Date.now()): string {
  const ago = now - ms;
  if (ago < 60_000) return "now";
  if (ago < 3_600_000) return `${Math.floor(ago / 60_000)}m`;
  if (ago < 86_400_000) return `${Math.floor(ago / 3_600_000)}h`;
  if (ago < 7 * 86_400_000) return `${Math.floor(ago / 86_400_000)}d`;
  const d = new Date(ms);
  return d.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    ...(d.getFullYear() === new Date(now).getFullYear() ? {} : { year: "2-digit" }),
  });
}

function originKey(origin: string | undefined): string {
  if (!origin || origin === "web") return "web";
  return origin;
}

function originLabel(key: string): string {
  if (key === "web") return "Web";
  if (key === "shared") return "Shared with me";
  if (key === "workspace") return "Workspace";
  if (key === "slack") return "Slack";
  if (key === "github") return "GitHub";
  return key.slice(0, 1).toUpperCase() + key.slice(1);
}

const ORIGIN_ORDER = ["web", "shared", "slack", "github", "workspace"];

/** Web chats and Slack threads are separate lists. Anything else keeps its own heading. */
function groupByOrigin(chats: Chat[]): { key: string; label: string; chats: Chat[] }[] {
  const by = new Map<string, Chat[]>();
  for (const c of chats) {
    // A web chat somebody else owns and the viewer takes part in — a chat with somebody
    // else's personal assistant among them — is its own list, labelled with that assistant.
    //
    // Phase 5: a chat somebody shared with the viewer (by name, or that they opened from a
    // link) is "Shared with me" wherever it lives, and a members-shared chat they have not
    // opened is "Workspace". A Slack thread or PR they spoke in stays under its surface.
    const key =
      c.accessReason === "workspace"
        ? "workspace"
        : c.accessReason === "shared" || (c.sharedWithMe && originKey(c.origin) === "web")
          ? "shared"
          : originKey(c.origin);
    const list = by.get(key);
    if (list) list.push(c);
    else by.set(key, [c]);
  }
  return [...by.keys()]
    .sort((a, b) => {
      const ia = ORIGIN_ORDER.indexOf(a);
      const ib = ORIGIN_ORDER.indexOf(b);
      if (ia === -1 && ib === -1) return a.localeCompare(b);
      if (ia === -1) return 1;
      if (ib === -1) return -1;
      return ia - ib;
    })
    .map((key) => ({ key, label: originLabel(key), chats: by.get(key) ?? [] }));
}

/** RAIL_PAGE is how many chats one origin lists before "Show more", and how many more each
 * press adds. A busy repository's pull requests would otherwise push the web chats out of
 * sight. */
const RAIL_PAGE = 8;

/** The origins somebody folded away, remembered per browser: a convenience, not state. */
const COLLAPSED_KEY = "podium.chatRail.collapsed";

function readCollapsed(): Set<string> {
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY);
    const keys: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(keys) ? keys.filter((k): k is string => typeof k === "string") : []);
  } catch {
    return new Set();
  }
}

function writeCollapsed(keys: Set<string>) {
  try {
    window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...keys]));
  } catch {
    // Private window or blocked storage: the fold lasts until the page reloads.
  }
}

/** visibleChats is the head of one origin's list: limit chats, and always the open one, so
 * opening a chat from a link never leaves it hidden under "Show more". */
function visibleChats(chats: Chat[], limit: number, active: string): Chat[] {
  const at = chats.findIndex((c) => c.id === active);
  return chats.slice(0, Math.max(limit, at + 1));
}

/** The source the rail is narrowed to — "all", or an origin key — remembered per browser. */
const SOURCE_KEY = "podium.chatRail.source";

function readSource(): string {
  try {
    return window.localStorage.getItem(SOURCE_KEY) || "all";
  } catch {
    return "all";
  }
}

function writeSource(key: string) {
  try {
    window.localStorage.setItem(SOURCE_KEY, key);
  } catch {
    // The choice lasts until the page reloads.
  }
}

/** OriginMark is the sign of where a list of chats lives: the service's own mark on its white
 * tile, as Connections draws it, or a drawn icon for what is Podium's own. */
function OriginMark({ origin }: { origin: string }) {
  if (origin === "github" || origin === "slack" || origin === "linear") {
    return <BrandTile name={origin} size="chip" />;
  }
  if (origin === "web") return <PodiumMark className="size-[22px] rounded-sm" />;
  const Icon = origin === "shared" ? Users : Building2;
  return (
    <span aria-hidden className="grid size-[22px] shrink-0 place-items-center rounded-sm border border-line-strong bg-panel">
      <Icon className="size-[13px] text-soft" strokeWidth={2} />
    </span>
  );
}

function ChatRail({
  chats,
  active,
  loading,
  onNew,
  creating,
  newDisabled,
  onOpen,
  onRename,
  onDelete,
  onLeave,
  deletingId,
  viewerLogin,
  voices,
  voiceId,
  onPickVoice,
}: {
  chats: Chat[];
  active: string;
  loading: boolean;
  onNew: () => void;
  creating: boolean;
  newDisabled: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
  onDelete: (chat: Chat) => void;
  onLeave: (chat: Chat) => void;
  deletingId?: string;
  viewerLogin: string;
  voices: VoiceRow[];
  voiceId: string;
  onPickVoice: (id: string) => void;
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
  const origins = useMemo(() => groupByOrigin(filtered), [filtered]);
  // The sources the selector offers are every one the viewer has chats in, whatever the
  // search says, so typing does not reshuffle the control under the cursor.
  const sources = useMemo(() => groupByOrigin(ordered), [ordered]);
  const [sourcePick, setSourcePick] = useState(readSource);
  const source = sources.some((o) => o.key === sourcePick) ? sourcePick : "all";
  const pickSource = (key: string) => {
    setSourcePick(key);
    writeSource(key);
  };
  const listed = source === "all" ? origins : origins.filter((o) => o.key === source);
  const split = listed.length > 1;
  const searching = query.trim() !== "";
  const [collapsed, setCollapsed] = useState<Set<string>>(readCollapsed);
  const toggleOrigin = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      writeCollapsed(next);
      return next;
    });
  // How many chats each origin lists. A search lists every match: the point of searching is
  // finding the one that was not on screen.
  const [shown, setShown] = useState<Record<string, number>>({});

  return (
    <div className="flex w-full min-h-0 min-w-0 shrink-0 flex-col border-b border-border bg-rail sm:w-[264px] sm:self-stretch sm:border-r sm:border-b-0">
      <div className="shrink-0 border-b border-border">
      <nav aria-label="Assistants">
        <RailHeading
          title="Assistants"
          action={
            <Tooltip label="New assistant">
              <Button variant="outline" size="icon-sm" asChild>
                <Link to="/agent/assistants/new" aria-label="New assistant">
                  <Plus />
                </Link>
              </Button>
            </Tooltip>
          }
        />
        {/* 16px under the list, the same as above the heading, before Chats begins. */}
        <ul className="hidden max-h-48 flex-col gap-0.5 overflow-y-auto px-2 pt-2 pb-4 sm:flex">
          {voices.map((voice) => (
            <li key={voice.id || "podium"}>
              {voice.gone ? (
                <span
                  aria-current="page"
                  className={cn(railRow, "truncate border-line-strong bg-panel text-fg")}
                >
                  {voice.label}
                </span>
              ) : (
                <Link
                  to={chatPath(voice.id)}
                  aria-current={voice.id === voiceId ? "page" : undefined}
                  onClick={(e) => {
                    if (voice.id === voiceId) e.preventDefault();
                  }}
                  className={cn(
                    railRow,
                    voice.id === voiceId
                      ? "border-line-strong bg-panel text-fg"
                      : "text-soft hover:border-border hover:bg-panel hover:text-fg",
                  )}
                >
                  <span className="min-w-0 truncate">{voice.label}</span>
                </Link>
              )}
            </li>
          ))}
        </ul>
      </nav>
        <div className="mx-3 my-3 sm:hidden">
          <select
            aria-label="Assistant"
            value={voiceId}
            onChange={(e) => onPickVoice(e.target.value)}
            className="min-w-0 w-full rounded-lg border border-border bg-bg px-2.5 py-2 text-sm text-fg"
          >
            {voices.map((voice) => (
              <option key={voice.id || "podium"} value={voice.id}>
                {voice.label}
              </option>
            ))}
          </select>
        </div>
      </div>

      <div className="flex shrink-0 flex-col">
        <RailHeading
          title="Chats"
          action={
            <Tooltip label={creating ? "Opening…" : "New chat"}>
              <Button
                type="button"
                variant="outline"
                size="icon-sm"
                data-testid="chat-new"
                aria-label="New chat"
                disabled={creating || newDisabled}
                onClick={onNew}
              >
                <Plus />
              </Button>
            </Tooltip>
          }
        />
        {ordered.length > 4 ? (
          <label className="relative mx-2 mt-2 block">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-faint" />
            <input
              data-testid="chat-search"
              aria-label="Search chats"
              value={query}
              placeholder="Search chats"
              onChange={(e) => setQuery(e.target.value)}
              className="min-h-11 w-full rounded-md border border-line-strong bg-panel pr-3 pl-9 text-base text-fg placeholder:text-muted transition-colors hover:border-muted"
            />
          </label>
        ) : null}
        {sources.length > 1 ? (
          <div
            role="radiogroup"
            aria-label="Show chats from"
            data-testid="chat-sources"
            className="mx-2 mt-2 hidden gap-0.5 rounded-lg border border-border bg-bg p-0.5 sm:flex"
          >
            {[{ key: "all", label: "All" }, ...sources].map((o) => {
              const on = o.key === source;
              const count =
                o.key === "all" ? filtered.length : (origins.find((x) => x.key === o.key)?.chats.length ?? 0);
              return (
                <Tooltip key={o.key} label={`${o.label} · ${count} ${count === 1 ? "chat" : "chats"}`}>
                  <button
                    type="button"
                    role="radio"
                    aria-checked={on}
                    aria-label={o.label}
                    data-testid={`chat-source-${o.key}`}
                    onClick={() => pickSource(o.key)}
                    className={cn(
                      "flex min-h-9 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md border px-2 text-xs font-semibold transition-[background-color,border-color,color] duration-150 ease-out",
                      on
                        ? "border-line-strong bg-panel text-fg shadow-[0_1px_2px_#0003]"
                        : "border-transparent text-muted hover:bg-panel/60 hover:text-fg",
                    )}
                  >
                    {o.key === "all" ? <span>All</span> : <OriginMark origin={o.key} />}
                    <span className={cn("tabular-nums", on ? "text-soft" : "text-faint")}>{count}</span>
                  </button>
                </Tooltip>
              );
            })}
          </div>
        ) : null}
      </div>

      <div className="mx-3 mt-2 mb-3 flex min-w-0 items-center gap-2 sm:hidden">
        <select
          aria-label="Chat"
          value={active}
          onChange={(e) => onOpen(e.target.value)}
          className="min-w-0 w-full flex-1 rounded-lg border border-border bg-bg px-2.5 py-2 text-sm text-fg"
        >
          <option value="">Pick a chat…</option>
          {split
            ? origins.map((origin) => (
                <optgroup key={origin.key} label={origin.label}>
                  {origin.chats.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.title}
                    </option>
                  ))}
                </optgroup>
              ))
            : ordered.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.title}
                </option>
              ))}
        </select>
        {activeChat && (roleOf(activeChat) === "" || roleOf(activeChat) === "owner") ? (
          <Tooltip label={`Delete ${activeChat.title}`}>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              data-testid="chat-delete-mobile"
              aria-label={`Delete ${activeChat.title}`}
              disabled={deletingId === activeChat.id}
              onClick={() => onDelete(activeChat)}
              className="hover:bg-err-bg hover:text-err"
            >
              <Trash2 />
            </Button>
          </Tooltip>
        ) : null}
      </div>

      <ul
        data-testid="chat-list"
        className="hidden min-h-0 flex-1 overflow-y-auto px-2 pt-2 pb-3 sm:block sm:h-0"
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
            {newDisabled
              ? "That assistant was deleted."
              : "No chats yet. Start one and it appears here, newest first."}
          </li>
        ) : null}
        {!loading && ordered.length > 0 && filtered.length === 0 ? (
          <li className="px-2.5 py-2 text-sm text-muted">No chats match.</li>
        ) : null}
        {listed.map((origin) => {
          const folded = split && !searching && collapsed.has(origin.key);
          const limit = searching ? origin.chats.length : (shown[origin.key] ?? RAIL_PAGE);
          const visible = folded ? [] : visibleChats(origin.chats, limit, active);
          const hidden = origin.chats.length - visible.length;
          const listId = `chat-origin-list-${origin.key}`;
          const unreadIn = origin.chats.reduce((n, c) => n + (c.id === active ? 0 : unread(c)), 0);
          return (
            <li key={origin.key} className="mt-2 border-t border-border pt-1 first:mt-0 first:border-t-0 first:pt-0">
              {split ? (
                // The heading stays at the top of the list while its own chats scroll under
                // it, so a long GitHub list never leaves the reader wondering whose rows
                // these are. It bleeds over the list's padding to cover what scrolls under it.
                <button
                  type="button"
                  data-testid={`chat-origin-${origin.key}`}
                  aria-expanded={!folded}
                  aria-controls={listId}
                  onClick={() => toggleOrigin(origin.key)}
                  className="group/origin sticky top-0 z-10 -mx-2 flex min-h-11 w-[calc(100%+1rem)] items-center gap-2.5 border-b border-transparent bg-rail px-5 text-left transition-colors duration-150 hover:text-fg"
                >
                  <OriginMark origin={origin.key} />
                  <span className="min-w-0 flex-1 truncate text-xs font-bold tracking-[0.08em] text-soft uppercase group-hover/origin:text-fg">
                    {origin.label}
                  </span>
                  {folded && unreadIn > 0 ? (
                    <span className="shrink-0 rounded-full bg-accent px-1.5 text-2xs font-semibold text-accent-ink tabular-nums">
                      {unreadIn > 99 ? "99+" : unreadIn}
                      <span className="sr-only"> unread</span>
                    </span>
                  ) : null}
                  <span className="shrink-0 text-xs font-semibold text-faint tabular-nums">
                    {origin.chats.length}
                    <span className="sr-only"> chats</span>
                  </span>
                  <ChevronDown
                    aria-hidden
                    className={cn(
                      "size-4 shrink-0 stroke-[1.8] text-muted transition-transform duration-200 ease-out motion-reduce:transition-none",
                      folded && "-rotate-90",
                    )}
                  />
                </button>
              ) : null}
              {folded ? null : (
                <ul
                  id={listId}
                  className="space-y-px animate-in fade-in-0 slide-in-from-top-1 duration-200 ease-out motion-reduce:animate-none"
                >
                  {visible.map((c) => (
                    <ChatRow
                      key={c.id}
                      chat={c}
                      active={c.id === active}
                      onOpen={onOpen}
                      onRename={onRename}
                      onDelete={onDelete}
                      onLeave={onLeave}
                      deleting={deletingId === c.id}
                      viewerLogin={viewerLogin}
                    />
                  ))}
                  {hidden > 0 ? (
                    <li className="pt-1.5">
                      <button
                        type="button"
                        data-testid={`chat-more-${origin.key}`}
                        onClick={() =>
                          setShown((prev) => ({ ...prev, [origin.key]: visible.length + RAIL_PAGE }))
                        }
                        className="flex min-h-9 w-full items-center justify-center gap-1.5 rounded-md border border-dashed border-border text-xs font-semibold text-muted transition-colors duration-150 hover:border-line-strong hover:bg-panel hover:text-fg"
                      >
                        <ChevronDown aria-hidden className="size-3.5 stroke-[2]" />
                        Show {Math.min(hidden, RAIL_PAGE)} more
                        <span className="font-normal text-faint tabular-nums">· {hidden} left</span>
                      </button>
                    </li>
                  ) : null}
                </ul>
              )}
            </li>
          );
        })}
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
  onLeave,
  deleting,
  viewerLogin,
}: {
  chat: Chat;
  active: boolean;
  onOpen: (id: string) => void;
  onRename: (id: string, title: string) => Promise<void>;
  onDelete: (chat: Chat) => void;
  onLeave: (chat: Chat) => void;
  deleting: boolean;
  viewerLogin: string;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(chat.title);
  const [saving, setSaving] = useState(false);
  const ignoreBlur = useRef(false);
  // Someone else's web chat is readable by the dev token and not writable. A mirrored
  // thread has no login, so its rename and delete stay.
  //
  // Phase 5: the server says the viewer's role, and the menu follows it — the owner renames
  // and deletes, an editor renames and leaves, a viewer leaves, and a Workspace chat nobody
  // opened yet has nothing to do but open. An older server says no role; the old rule stands.
  const role = roleOf(chat);
  const foreign = role === "" && chat.login !== "" && viewerLogin !== "" && chat.login !== viewerLogin;
  const workspaceOnly = chat.accessReason === "workspace";
  const mayRename = role === "" ? !foreign : canWrite(role) && !workspaceOnly;
  const mayDelete = role === "" ? !foreign : role === "owner";
  const mayLeave = !workspaceOnly && (role === "editor" || role === "viewer");
  const menu = mayRename || mayDelete || mayLeave;
  const unreadCount = active ? 0 : unread(chat);
  const shared = sharedByLabel(chat);

  const start = () => {
    if (!mayRename) return;
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
  // A pull request's repository is its GitHub surface's label without the number.
  const repo = (chat.surfaces ?? []).find((s) => s.kind === "github")?.label.replace(/#\d+$/, "") ?? "";
  const where = mirrored ? (chat.channel ? `#${chat.channel}` : repo || chat.origin) : "";
  // The line under the title. A pull request's title is its number and its own title, and
  // its group already says GitHub: the line names the repository and whose it is.
  const subtitle = chat.origin === "github" ? [repo, chat.startedBy].filter(Boolean) : [where].filter(Boolean);
  const hint = (
    <span className="block max-w-64 space-y-0.5 py-0.5">
      {where || chat.startedBy ? (
        <span className="block font-medium text-fg">
          {[where, chat.startedBy && `started by ${chat.startedBy}`].filter(Boolean).join(" · ")}
        </span>
      ) : null}
      <span className="line-clamp-2 block text-muted">{chat.preview || "Nothing said yet"}</span>
      <span className="block text-faint">
        {running ? "Running · " : ""}
        {relative(chat.lastMessageAt ?? chat.createdAt)}
      </span>
    </span>
  );

  // A pull request's title leads with its number. The number is the handle people say out
  // loud, so it is set apart — tabular and quieter — and the PR's own words carry the row.
  const pr = chat.origin === "github" ? /^#(\d+)(?::\s*(.+))?$/.exec(chat.title) : null;
  const at = when(chat);

  return (
    <li
      className={cn(
        "group relative flex min-h-[52px] items-center rounded-md border transition-colors duration-150",
        active ? "border-line-strong bg-panel shadow-[0_1px_2px_#0002]" : "border-transparent hover:border-border hover:bg-panel",
      )}
    >
      <Tooltip label={hint} side="right">
        <button
          type="button"
          onClick={() => onOpen(chat.id)}
          onDoubleClick={(e) => {
            e.preventDefault();
            start();
          }}
          aria-current={active ? "true" : undefined}
          className="flex min-h-[52px] min-w-0 flex-1 items-center gap-2 rounded-md py-2 pr-2 pl-3 text-left"
        >
          <span className="min-w-0 flex-1">
            <span
                className={cn(
                  "block truncate text-sm transition-colors duration-150",
                  active
                    ? "font-bold text-fg"
                    : unreadCount > 0
                      ? "font-semibold text-fg"
                      : "font-medium text-soft group-hover:text-fg",
                )}
              >
                {pr && pr[2] ? (
                  <>
                    <span className="font-semibold text-muted tabular-nums">#{pr[1]}</span> {pr[2]}
                  </>
                ) : (
                  chat.title
                )}
              </span>
            {subtitle.length > 0 ? (
              <span data-testid="chat-row-where" className="mt-0.5 flex min-w-0 items-center gap-1.5 text-2xs text-muted">
                {subtitle.map((part, i) => (
                  <Fragment key={i}>
                    {i > 0 ? <span aria-hidden className="size-[3px] shrink-0 rounded-full bg-current opacity-70" /> : null}
                    <span className="truncate">{part}</span>
                  </Fragment>
                ))}
              </span>
            ) : null}
            {foreign && !chat.sharedWithMe ? (
              <span className="mt-0.5 block truncate text-2xs text-muted">{chat.login}</span>
            ) : null}
            {shared ? (
              <span data-testid="chat-row-shared" className="mt-0.5 block truncate text-2xs text-muted">
                {chat.personalityDisplay ? `${chat.personalityDisplay} · ` : ""}
                {shared}
              </span>
            ) : chat.sharedWithMe && !mirrored ? (
              <span data-testid="chat-row-shared" className="mt-0.5 block truncate text-2xs text-muted">
                {chat.personalityDisplay ? `${chat.personalityDisplay} · ` : ""}shared by {chat.login}
              </span>
            ) : null}
          </span>
          {/* The right column is the row's own at all times, so nothing moves on hover: when it
              was last spoken in (or that it is working now) and what is waiting. The actions
              button takes this same slot on hover, and the column steps out of its way. */}
          <span
            className={cn(
              "flex w-8 shrink-0 flex-col items-end justify-center gap-1 self-stretch transition-opacity duration-150",
              menu && "group-focus-within:opacity-0 group-hover:opacity-0 group-has-[[data-state=open]]:opacity-0",
            )}
          >
            {running ? (
              <span className="relative inline-flex size-2" aria-hidden>
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-run opacity-60" />
                <span className="relative inline-flex size-2 rounded-full bg-run" />
              </span>
            ) : at > 0 ? (
              <time
                dateTime={new Date(at).toISOString()}
                className={cn(
                  "text-2xs whitespace-nowrap tabular-nums",
                  unreadCount > 0 ? "font-semibold text-fg" : "text-faint",
                )}
              >
                {shortWhen(at)}
              </time>
            ) : null}
            {unreadCount > 0 ? (
              <span
                data-testid="chat-row-unread"
                className="min-w-5 rounded-full bg-accent px-1.5 text-center text-2xs leading-4 font-semibold text-accent-ink tabular-nums"
              >
                {unreadCount > 99 ? "99+" : unreadCount}
                <span className="sr-only"> unread</span>
              </span>
            ) : null}
          </span>
          {running ? <span className="sr-only">running</span> : null}
          {chat.startedBy ? <span className="sr-only">started by {chat.startedBy}</span> : null}
          {chat.preview ? <span className="sr-only">{chat.preview}</span> : null}
        </button>
      </Tooltip>
      {!menu ? null : (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            data-testid="chat-actions"
            aria-label={`Actions for ${chat.title}`}
            className="absolute top-1/2 right-1.5 grid size-7 -translate-y-1/2 place-items-center rounded-md text-muted opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 hover:bg-raised hover:text-fg focus-visible:opacity-100 data-[state=open]:bg-raised data-[state=open]:opacity-100"
          >
            <Ellipsis className="size-4" />
          </button>
        </DropdownMenuTrigger>
        {/* Rename swaps the row for an input; handing focus back to the trigger would blur
            it the moment it appears. */}
        <DropdownMenuContent align="start" className="min-w-36" onCloseAutoFocus={(e) => e.preventDefault()}>
          {mayRename ? (
            <DropdownMenuItem data-testid="chat-rename" aria-label={`Rename ${chat.title}`} onSelect={start}>
              <Pencil />
              Rename
            </DropdownMenuItem>
          ) : null}
          {mayLeave ? (
            <DropdownMenuItem data-testid="chat-leave" aria-label={`Leave ${chat.title}`} onSelect={() => onLeave(chat)}>
              <LogOut />
              Leave
            </DropdownMenuItem>
          ) : null}
          {mayDelete ? (
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
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
      )}
    </li>
  );
}

function ConversationTitle({
  title,
  channel,
  onRename,
  fallback = "Chat",
  canRename = true,
  actions,
}: {
  title: string;
  channel?: string;
  onRename: (title: string) => Promise<void>;
  /** canRename is false for a viewer: the title is shown, not editable. */
  canRename?: boolean;
  /** actions sit at the right end of the bar: the Share button. */
  actions?: ReactNode;
  /** fallback is the title slot when the chat has no name yet. */
  fallback?: string;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(title);
  const [saving, setSaving] = useState(false);
  const ignoreBlur = useRef(false);

  const start = () => {
    if (!title || !canRename) return;
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
    <div className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-panel px-4 sm:px-5">
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
          {title || fallback}
        </button>
      )}
      {actions ? <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div> : null}
    </div>
  );
}

function Conversation({
  chatId,
  title,
  remembered,
  listedOrigin,
  listedChannel,
  listedLogin,
  listedEmpty,
  listedRole,
  listedSharedWithMe,
  listedMultiplayer,
  onRename,
  onLeave,
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
  /** listedLogin is the owner the list row already has. Empty for a mirrored thread. */
  listedLogin?: string;
  /**
   * listedEmpty is true when the list row has no preview yet — a new chat. A chat the list does
   * not have yet (still loading, or opened from a link) is not empty. The connecting
   * skeleton is a fake user bubble and would flash before the greeting.
   */
  listedEmpty?: boolean;
  /**
   * listedRole and listedSharedWithMe are what the list row says this viewer may do, for the
   * same reason as listedOrigin: the stream's own chat frame lands after the first render, and
   * a viewer was shown the composer until it did.
   */
  listedRole?: string;
  listedSharedWithMe?: boolean;
  /** listedMultiplayer is whether the list row says other people are in it: faces to come. */
  listedMultiplayer?: boolean;
  onRename: (title: string) => Promise<void>;
  onLeave: () => void;
  assistant?: Assistant;
  playbookNames: string[];
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const viewer = useViewer();
  const location = useLocation();
  // k= is a share link's token (phase 5). It is only needed for the first open — that writes
  // the viewer a row of their own — and harmless after.
  const [search] = useSearchParams();
  const stream = useChatStream(chatId, search.get("k") ?? "");
  // A desktop turn's desktop, beside the conversation while its task runs — including the wait
  // for the next message, which is when a person works on it. Hidden is per conversation.
  const desktopTaskId = desktopCandidate(stream.messages, stream.taskId);
  const desktop = useDesktopTask(desktopTaskId);
  const [desktopHidden, setDesktopHidden] = useState(false);
  const desktopShown = desktop.desktop && !desktopHidden && desktopTaskId !== undefined;
  // The task whose pane is mounted: the one shown, and, while it animates out after the
  // desktop goes — hidden, or its session ended — the one that was. The pane says when it has
  // finished closing.
  const [paneTask, setPaneTask] = useState<string | undefined>(undefined);
  if (desktopShown && desktopTaskId !== paneTask) {
    setPaneTask(desktopTaskId);
  }
  const split = useRef<HTMLDivElement>(null);
  const [desktopWidth, setDesktopWidth] = useState<number | undefined>(undefined);
  const desktopStyle =
    desktopWidth === undefined ? undefined : ({ "--desktop-width": `${desktopWidth}px` } as CSSProperties);
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
  const owner = stream.chat?.login || listedLogin || "";
  // Somebody else's chat is writable by the people taking part in it (phase 4): the server
  // marks those shared_with_me. Anybody else reading it — the dev token — gets the note.
  const sharedWithMe = stream.chat ? stream.chat.sharedWithMe : listedSharedWithMe === true;
  // Phase 5: the server says what this viewer may do. A viewer reads; an editor and the owner
  // write. The dev token is "owner" of everything but writes nobody's chat but its own, so the
  // note for somebody else's chat still stands for it.
  const role = roleOf(stream.chat ?? { myRole: listedRole ?? "" });
  // Until either says, the bottom of the screen is held rather than guessed: offering the
  // composer and then taking it away is the flash this avoids.
  const roleKnown = stream.chat !== undefined || role !== "" || stream.gone === true;
  const viewOnly = role === "viewer";
  const foreign =
    owner !== "" &&
    (viewer?.login ?? "") !== "" &&
    owner !== viewer?.login &&
    !sharedWithMe &&
    role !== "editor" &&
    role !== "viewer";
  const multiplayer = stream.chat?.multiplayer === true || mirrored;
  const personalityName = stream.chat?.personalityDisplay ?? "";
  const me = viewer?.login ?? "";
  const queue = useMemo(() => {
    const turns = stream.queue;
    let firstMine = -1;
    const mine: bigint[] = [];
    turns.forEach((t, i) => {
      if (me !== "" && t.login === me && t.seq > 0n) {
        if (firstMine < 0) firstMine = i;
        mine.push(t.seq);
      }
    });
    // Ahead of my first waiting turn: everything queued before it, and the turn running.
    const ahead = firstMine < 0 ? 0 : firstMine + (stream.running ? 1 : 0);
    return { ahead, mine, waiting: turns.length };
  }, [stream.queue, stream.running, me]);
  useEffect(() => {
    if (!stream.chat) return;
    void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
  }, [qc, stream.chat]);

  // Reading the chat moves the viewer's read mark to the newest message (phase 5), so its
  // unread count clears here and on every other screen that lists it. Only forward, and only
  // once per new message.
  const newestSeq = stream.messages.length > 0 ? stream.messages[stream.messages.length - 1].seq : 0n;
  const markedSeq = useRef(0n);
  useEffect(() => {
    if (newestSeq === 0n || newestSeq <= markedSeq.current || role === "") return;
    markedSeq.current = newestSeq;
    void agent
      .markChatRead({ chatId, seq: newestSeq })
      .then(() => qc.invalidateQueries({ queryKey: ["agent", "chats"] }))
      .catch(() => {
        // A read mark that did not land is retried by the next message.
        markedSeq.current = 0n;
      });
  }, [chatId, newestSeq, role, qc]);

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
  // gap between the RPC returning and the stream catching up. A question sent from the
  // arrival arrives as navigation state: the chat was created and the message stored
  // before this conversation mounted.
  const seeded = (location.state as { pendingText?: string } | null)?.pendingText;
  const [pendingUser, setPendingUser] = useState<{ text: string; before: number } | null>(
    seeded ? { text: seeded, before: 0 } : null,
  );

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
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
      // A message nobody put to Podium is not waiting for an answer.
      if (!res.addressed) setPendingUser(null);
    },
    onError: (err) => {
      setPendingUser(null);
      if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
        if (err.rawMessage === "That assistant was deleted.") {
          toast("That assistant was deleted.");
          return;
        }
      }
      toast(errorMessage(err));
    },
  });

  const stop = useMutation({
    mutationFn: () => agent.cancelChatTurn({ chatId }),
    onSuccess: (res) => {
      if (!res.stopped) toast("Nothing was running.");
    },
    onError: (err) => toast(errorMessage(err)),
  });
  const takeBack = useMutation({
    mutationFn: (seq: bigint) => agent.cancelQueuedTurn({ chatId, seq }),
    onError: (err) => toast(errorMessage(err)),
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
  // The composer is only held while a send is in flight. A turn running no longer blocks it:
  // what is sent meanwhile is queued as a turn of its own (phase 4).
  const sending = send.isPending;
  // Still loading until the stream says the replay is done: the chat's own frames arrive
  // before its messages, so going by the stream's phase showed the new-chat welcome for a
  // moment on every chat that has history.
  const connecting =
    !stream.replayed && stream.messages.length === 0 && !stream.gone && !pendingVisible && !listedEmpty;
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
            <Button size="sm" onClick={onLeave}>
              Back to chats
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div ref={split} className="flex min-h-0 min-w-0 flex-1 flex-col lg:flex-row" style={desktopStyle}>
      <div className={cn("flex min-h-0 min-w-0 flex-1 flex-col", paneTask !== undefined && "lg:min-w-[480px]")}>
        <ConversationTitle
          title={title || stream.chat?.title || ""}
          channel={channel}
          onRename={onRename}
          canRename={role === "" || canWrite(role)}
          actions={
            <>
              {desktop.desktop && desktopHidden ? (
                <Button size="xs" variant="outline" onClick={() => setDesktopHidden(false)} aria-label="Show the desktop">
                  <Monitor aria-hidden />
                  Desktop
                </Button>
              ) : null}
              {role !== "" ? (
                <ShareButton
                  chatId={chatId}
                  title={title || stream.chat?.title || ""}
                  multiplayer={stream.chat?.multiplayer ?? false}
                />
              ) : null}
            </>
          }
        />
        {/* One header row: who is in it and where it lives, then what it produced. */}
        <ChatPullRequests
          chatId={chatId}
          pullRequests={stream.pullRequests}
          loading={!stream.pullRequestsKnown || stream.chat === undefined}
          expect={{ surface: mirrored, people: mirrored || listedMultiplayer === true }}
          leading={
            (stream.chat?.surfaces.length ?? 0) > 0 ? <ChatSurfaces surfaces={stream.chat?.surfaces ?? []} /> : undefined
          }
          trailing={
            (stream.chat?.people.length ?? 0) > 0 ? <ChatPeople people={stream.chat?.people ?? []} /> : undefined
          }
        />
        {/* While the transcript's code loads, the pane says it is loading. Empty, under the chat's
            title and above the composer, it read as a new chat. */}
        <Suspense fallback={<ThreadLoading />}>
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
                <ArrivalWell
                  botName={botName}
                  playbookNames={playbookNames}
                  onSuggest={(text) => send.mutate({ text, choice })}
                  suggesting={busy}
                />
              ) : null
            }
          />
        </Suspense>

        {!roleKnown ? (
          <ComposerPlaceholder />
        ) : viewOnly ? (
          <div
            data-testid="chat-view-only"
            className="flex items-center gap-2 border-t border-border bg-panel px-5 py-3 text-xs leading-relaxed text-muted"
          >
            <Users className="size-4 shrink-0" aria-hidden />
            You have view access. Ask {owner || "the owner"} to make you an editor to post here.
          </div>
        ) : foreign ? (
          <div
            data-testid="chat-foreign-note"
            className="border-t border-border bg-panel px-5 py-3 text-xs leading-relaxed text-muted"
          >
            This chat belongs to {owner}.
          </div>
        ) : (
          <ChatComposer
            disabled={sending}
            running={stream.running && !stream.awaiting}
            onStop={() => stop.mutate()}
            multiplayer={multiplayer}
            mirrored={mirrored ? origin : ""}
            personality={personalityName}
            queue={queue}
            onTakeBack={(seq) => takeBack.mutate(seq)}
            agents={agents}
            assistant={assistant}
            choice={choice}
            onChoiceChange={setChoice}
            onSend={(text, choice) => send.mutate({ text, choice })}
          />
        )}
      </div>
      {paneTask !== undefined ? (
        <DesktopPane
          key={paneTask}
          taskId={paneTask}
          shown={desktopShown && desktopTaskId === paneTask}
          onClosed={() => setPaneTask(undefined)}
          width={desktopWidth}
          onWidth={setDesktopWidth}
          onReset={() => setDesktopWidth(undefined)}
          container={split}
          onHide={() => setDesktopHidden(true)}
        />
      ) : null}
    </div>
  );
}

const SUGGESTIONS = [
  "Why did the nightly ETL fail?",
  "Summarise the last failed task",
  "What can you do on this stack?",
];

/**
 * Arrival is the chat before a conversation exists. It is the same composition as an
 * empty thread: a title slot, the fact of what a turn does, and the composer at the bottom.
 * Sending creates the chat.
 */
function Arrival({
  botName,
  playbookNames,
  sending,
  assistant,
  onSend,
}: {
  botName: string;
  playbookNames: string[];
  sending: boolean;
  assistant?: Assistant;
  onSend: (text: string, choice: AgentChoice) => void;
}) {
  const { agents } = useAgents();
  const [picked, setPicked] = useState<AgentChoice | undefined>(undefined);
  const choice = picked ?? INHERIT;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ConversationTitle title="" fallback="New chat" onRename={async () => {}} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex min-h-full w-full max-w-3xl flex-col px-4 py-8 sm:px-5">
          <ArrivalWell
            botName={botName}
            playbookNames={playbookNames}
            suggesting={sending}
            onSuggest={(text) => onSend(text, choice)}
          />
        </div>
      </div>
      <ChatComposer
        focusOnMount
        disabled={sending}
        agents={agents}
        assistant={assistant}
        choice={choice}
        onChoiceChange={setPicked}
        onSend={onSend}
      />
    </div>
  );
}

function ArrivalWell({
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
    <div className="flex flex-1 flex-col items-center justify-center gap-4 text-center">
      <div className="space-y-1.5">
        <p className="text-xl font-medium tracking-tight text-fg">Ask {botName} something</p>
        <p className="mx-auto max-w-md text-sm leading-relaxed text-muted">
          {botName} answers here. When something needs a machine it starts a task on your nodes and
          reports back. You will see each one it runs.
        </p>
      </div>
      <div className="flex max-w-lg flex-wrap justify-center gap-2">
        {SUGGESTIONS.map((prompt) => (
          <button
            key={prompt}
            type="button"
            disabled={suggesting}
            onClick={() => onSuggest(prompt)}
            className="rounded-full border border-border bg-panel px-3 py-1.5 text-xs text-fg shadow-xs outline-none transition-colors hover:border-line-strong hover:bg-raised focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
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

/** ThreadLoading is TranscriptSkeleton where ChatThread would draw it, for its Suspense fallback. */
function ThreadLoading() {
  return (
    <div className="relative min-h-0 flex-1 overflow-hidden">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-4 py-8 sm:px-5">
        <TranscriptSkeleton />
      </div>
    </div>
  );
}

/**
 * TranscriptSkeleton is the conversation before its history lands: a few exchanges in the
 * shape a real one has, each settling in after the one above it, and a highlight passing down
 * through them so it reads as one thing loading rather than a grid of blinking bars. Under
 * prefers-reduced-motion both are reduced to a still frame by the global rule in index.css.
 */
function TranscriptSkeleton() {
  // Widths vary so it does not look like a template; the user bubbles sit right like real ones.
  const turns = [
    { ask: "w-56", lines: ["w-full", "w-11/12", "w-2/3"] },
    { ask: "w-40", lines: ["w-full", "w-4/5"] },
    { ask: "w-64", lines: ["w-full", "w-full", "w-3/5"] },
  ];
  let row = 0;
  // Each bar's shimmer starts a beat after the one above it: a wave, top to bottom.
  const wave = () => ({ animationDelay: `${row++ * 90}ms` });
  return (
    <div aria-busy="true" aria-label="Loading the conversation" className="space-y-7">
      {turns.map((t, i) => (
        <div
          key={i}
          className="animate-in fade-in-0 slide-in-from-bottom-1 space-y-5 fill-mode-both duration-500"
          style={{ animationDelay: `${i * 120}ms` }}
        >
          <div className="flex justify-end">
            <Skeleton className={cn("h-10 rounded-2xl rounded-br-md", t.ask)} style={wave()} />
          </div>
          <div className="flex gap-3">
            <Skeleton className="size-7 shrink-0 rounded-lg" style={wave()} />
            <div className="min-w-0 flex-1 space-y-2.5 pt-1">
              <Skeleton className="h-3 w-20" style={wave()} />
              {t.lines.map((w, j) => (
                <Skeleton key={j} className={cn("h-3.5", w)} style={wave()} />
              ))}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

/**
 * ComposerPlaceholder holds the bottom of the screen while nothing yet says whether this viewer
 * may write here. It is inert and quiet, so whichever replaces it (the composer or the
 * view-only note) does not look like a control being taken away.
 */
function ComposerPlaceholder() {
  return (
    <div aria-hidden className="pt-4">
      <div className="mx-auto w-full max-w-3xl px-4 pb-3 sm:px-5">
        <Skeleton className="h-[52px] rounded-3xl opacity-60" />
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

function voiceRowsFor(
  personalities: Personality[],
  personalityId: string,
  gone: boolean,
  chats: Chat[],
): VoiceRow[] {
  const rows: VoiceRow[] = [
    { id: "", label: "Podium" },
    ...[...personalities]
      .sort(comparePersonalities)
      .map((p) => ({ id: p.id, label: p.displayName || p.name })),
  ];
  if (gone) {
    const label = chats.find((c) => c.personalityDisplay)?.personalityDisplay || "Deleted assistant";
    rows.push({ id: personalityId, label, gone: true });
  }
  return rows;
}
