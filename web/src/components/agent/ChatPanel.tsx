import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Chat, ChatMessage, Skill } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { useChatStream } from "../../hooks/useChatStream";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { renderMarkdown } from "../../lib/markdown";
import { Empty } from "../Empty";
import { useToast } from "../Toast";
import { ChatAttachments } from "./ChatAttachments";
import { ChatComposer } from "./ChatComposer";

/** SCROLL_SLACK_PX is how far off the bottom still counts as "at the bottom". */
const SCROLL_SLACK_PX = 40;

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
  const skills = useQuery({
    queryKey: ["agent", "skills"],
    queryFn: () => agent.listSkills({}),
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
      <Empty
        title="The chat is not available on this conductor"
        hint="podium-agent serves it with no extra configuration. See docs/agent.md#chat."
      />
    );
  }
  if (connectCode(chats.error) === Code.Unauthenticated) {
    return (
      <Empty
        title="The conductor does not know who you are"
        hint="A chat belongs to a login, and podium-server asserts it. Reload, and check that this control plane's identity middleware is configured."
      />
    );
  }
  if (chats.isError && !isAgentUnreachable(chats.error)) {
    return <Empty title="Could not read your chats" hint={errorMessage(chats.error)} />;
  }

  const list = chats.data?.chats ?? [];

  return (
    <div className="flex h-full min-h-0 flex-col">
      {isAgentUnreachable(chats.error) ? (
        <p className="mx-3 mt-3 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}

      <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
        <ChatRail
          chats={list}
          active={active}
          loading={chats.isPending}
          onNew={() => create.mutate("")}
          creating={create.isPending}
          onOpen={(id) => navigate(`/agent/chat/${id}`)}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-panel">
          {active === "" ? (
            <div className="p-4">
              <Empty
                title={list.length === 0 ? "No chats yet" : "Pick a chat, or start a new one"}
                hint="Ask the bot a question and it answers from the data warehouse, showing the SQL it ran."
              />
            </div>
          ) : (
            // Keyed on the chat: a switch remounts the conversation, so its skill choice
            // and scroll position start fresh without an effect resetting them.
            <Conversation
              key={active}
              chatId={active}
              botName={skills.data?.profileDisplayName ?? "Podium"}
              skills={skills.data?.skills ?? []}
              chatDefaultSkill={
                skills.data?.skills.find((s) => s.chatDefault)?.name ?? ""
              }
            />
          )}
        </div>
      </div>
    </div>
  );
}

function ChatRail({
  chats,
  active,
  loading,
  onNew,
  creating,
  onOpen,
}: {
  chats: Chat[];
  active: string;
  loading: boolean;
  onNew: () => void;
  creating: boolean;
  onOpen: (id: string) => void;
}) {
  return (
    <div className="flex w-full shrink-0 flex-col gap-2 border-b border-border bg-panel p-3 sm:w-64 sm:border-r sm:border-b-0">
      <button
        type="button"
        data-testid="chat-new"
        disabled={creating}
        onClick={onNew}
        className="rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 focus-visible:ring-2 focus-visible:ring-ring/60"
      >
        {creating ? "Opening…" : "New chat"}
      </button>

      {/* On a narrow viewport the rail is a select rather than a drawer: one control, no
          overlay, and the keyboard works. */}
      <select
        aria-label="Chat"
        value={active}
        onChange={(e) => onOpen(e.target.value)}
        className="rounded border border-border bg-bg px-2 py-1.5 text-sm text-fg sm:hidden"
      >
        <option value="">Pick a chat…</option>
        {chats.map((c) => (
          <option key={c.id} value={c.id}>
            {c.title}
          </option>
        ))}
      </select>

      <ul data-testid="chat-list" className="hidden min-w-0 flex-1 space-y-1 sm:block">
        {loading ? <li className="px-2 py-1 text-xs text-muted">Loading…</li> : null}
        {!loading && chats.length === 0 ? (
          <li className="px-2 py-1 text-xs text-muted">No chats yet.</li>
        ) : null}
        {chats.map((c) => (
          <li key={c.id}>
            <button
              type="button"
              onClick={() => onOpen(c.id)}
              aria-current={c.id === active ? "true" : undefined}
              className={`w-full rounded px-2 py-1.5 text-left focus-visible:ring-1 focus-visible:ring-accent ${
                c.id === active ? "bg-raised" : "hover:bg-raised/60"
              }`}
            >
              <span className="block truncate text-sm text-fg">{c.title}</span>
              <span className="block truncate text-xs text-muted">
                {c.preview || "nothing said yet"}
              </span>
              <span className="block text-xs text-muted">
                {c.turnRunning ? "working…" : relative(c.lastMessageAt ?? c.createdAt)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Conversation({
  chatId,
  botName,
  skills,
  chatDefaultSkill,
}: {
  chatId: string;
  botName: string;
  skills: Skill[];
  chatDefaultSkill: string;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const stream = useChatStream(chatId);
  // Undefined means "whatever the profile says", which is not known until ListSkills
  // answers — so the choice is derived rather than copied into state on arrival.
  const [chosen, setChosen] = useState<string>();
  const skill = chosen ?? chatDefaultSkill;
  const [pinned, setPinned] = useState(true);
  const scroller = useRef<HTMLDivElement>(null);

  // The choice is sticky across messages, the way every chat that has a model picker
  // behaves: you pick once and keep asking. It is still sent per message, so nothing is
  // remembered server-side and a reload goes back to the skill's own model.
  const [choice, setChoice] = useState<AgentChoice>(INHERIT);
  const { agents } = useAgents();

  const send = useMutation({
    mutationFn: (v: { text: string; choice: AgentChoice }) =>
      agent.sendChatMessage({
        chatId,
        text: v.text,
        skill,
        // Empty fields mean "the skill's", which is exactly what the server does with them.
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

  return (
    <>
      <div
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          setPinned(el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_SLACK_PX);
        }}
        className="relative min-h-0 flex-1 space-y-3 overflow-y-auto p-4"
      >
        {stream.phase === "connecting" && stream.messages.length === 0 ? (
          <p className="text-xs text-muted">Connecting…</p>
        ) : null}
        {stream.error ? (
          <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
            The chat stream dropped and is reconnecting: {stream.error}
          </p>
        ) : null}

        {stream.messages.map((m, i) => (
          <Bubble
            key={String(m.seq)}
            message={m}
            botName={botName}
            firstOfRun={runs[i]}
          />
        ))}

        {stream.progress !== undefined ? (
          <p data-testid="chat-progress" className="flex items-center gap-2 pl-1 text-xs text-run">
            <span className="inline-block size-1.5 animate-pulse rounded-full bg-run" />
            <span className="min-w-0 truncate">{stream.progress}</span>
          </p>
        ) : busy ? (
          <p data-testid="chat-progress" className="flex items-center gap-2 pl-1 text-xs text-run">
            <span className="inline-block size-1.5 animate-pulse rounded-full bg-run" />
            <span>working…</span>
          </p>
        ) : null}
      </div>

      {!pinned ? (
        <button
          type="button"
          onClick={() => setPinned(true)}
          className="mx-auto -mt-8 mb-2 w-fit rounded-full border border-border bg-raised px-3 py-1 text-xs text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
        >
          ↓ new messages
        </button>
      ) : null}

      <ChatComposer
        skills={skills}
        skill={skill}
        onSkillChange={setChosen}
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
 * runsOf marks the first message of each run by one speaker, so the bot's name is a label
 * above a run rather than a repeat above every bubble.
 */
function runsOf(messages: ChatMessage[]): boolean[] {
  return messages.map((m, i) => i === 0 || messages[i - 1].role !== m.role);
}

function Bubble({
  message,
  botName,
  firstOfRun,
}: {
  message: ChatMessage;
  botName: string;
  firstOfRun: boolean;
}) {
  const mine = message.role === "user";
  return (
    <div className={`flex flex-col ${mine ? "items-end" : "items-start"}`}>
      {!mine && firstOfRun ? <span className="mb-1 text-xs text-muted">{botName}</span> : null}
      <div
        data-testid="chat-message"
        data-role={message.role}
        title={absolute(message.ts)}
        className={`max-w-full min-w-0 rounded-lg border px-3 py-2 text-sm ${
          mine ? "border-accent/30 bg-accent/15" : "border-border bg-background"
        }`}
      >
        <div className="min-w-0">{renderMarkdown(message.text, `m${message.seq}-`)}</div>
        <ChatAttachments attachments={message.attachments} />
      </div>
    </div>
  );
}
