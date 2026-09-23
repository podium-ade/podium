import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import {
  ArrowDown,
  Bot,
  Brain,
  Check,
  ChevronRight,
  Copy,
  LoaderCircle,
  Terminal,
  Wrench,
  X,
} from "lucide-react";
import { Link } from "react-router";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  AssistantRuntimeProvider,
  MessagePartPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  groupPartByType,
  useAuiState,
  useExternalStoreRuntime,
  type ThreadMessageLike,
  type ToolCallMessagePartProps,
} from "@assistant-ui/react";
import { TASK_TOOL, type MessageCustom, type TaskArgs } from "../../../lib/chatThread";
import { absolute, relative } from "../../../lib/format";
import { cn } from "../../../lib/utils";
import { Button } from "../../ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../../ui/collapsible";
import { Tooltip } from "../../ui/tooltip";
import { ChatAttachments } from "../ChatAttachments";
import { MarkdownText } from "./MarkdownText";

interface ChatContextValue {
  botName: string;
  /** busy is true while a turn is in flight and not waiting on a human. */
  busy: boolean;
  /** progress is the conductor's ephemeral line before the turn says anything. */
  progress?: string;
  /** taskId is the task the running turn is using, when the stream said so. */
  taskId?: string;
}

const ChatContext = createContext<ChatContextValue>({ botName: "Podium", busy: false });

/**
 * ChatThread draws a converted transcript (lib/chatThread) with assistant-ui. Podium owns
 * the conversation — the stream, the send, the composer — so the runtime here is an
 * external store over messages that are already in hand, and the library does the part it
 * is good at: grouping parts, nesting a subagent's conversation, and keeping the view on
 * the latest turn.
 */
export function ChatThread({
  messages,
  onSend,
  top,
  welcome,
  ...ctx
}: ChatContextValue & {
  messages: ThreadMessageLike[];
  onSend: (text: string) => void;
  /** top is drawn above the transcript: the connecting skeleton, a dropped-stream alert. */
  top?: ReactNode;
  /** welcome is drawn in place of an empty transcript. */
  welcome?: ReactNode;
}) {
  const runtime = useExternalStoreRuntime<ThreadMessageLike>({
    messages,
    isRunning: ctx.busy,
    convertMessage: (m) => m,
    onNew: async (m) => {
      const text = m.content.map((p) => (p.type === "text" ? p.text : "")).join("");
      if (text.trim() !== "") onSend(text);
    },
  });

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <ChatContext.Provider value={ctx}>
        <ThreadPrimitive.Root className="relative min-h-0 flex-1">
          <ThreadPrimitive.Viewport
            data-testid="chat-scroller"
            className="absolute inset-0 flex flex-col overflow-y-auto"
          >
            <div className="mx-auto w-full max-w-3xl flex-1 space-y-6 px-4 py-8 sm:px-5">
              {top}
              <ThreadPrimitive.Empty>{welcome}</ThreadPrimitive.Empty>
              <ThreadPrimitive.Messages components={{ UserMessage, AssistantMessage }} />
            </div>
            <ThreadPrimitive.ViewportFooter className="pointer-events-none sticky bottom-3 flex justify-center">
              <ThreadPrimitive.ScrollToBottom asChild>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="pointer-events-auto animate-in fade-in-0 slide-in-from-bottom-2 rounded-full shadow-md disabled:hidden"
                >
                  <ArrowDown />
                  Jump to latest
                </Button>
              </ThreadPrimitive.ScrollToBottom>
            </ThreadPrimitive.ViewportFooter>
          </ThreadPrimitive.Viewport>
        </ThreadPrimitive.Root>
      </ChatContext.Provider>
    </AssistantRuntimeProvider>
  );
}

/** ts turns a message's Date back into the Timestamp lib/format reads. */
const ts = (d?: Date) => (d ? timestampFromDate(d) : undefined);

const useCustom = () => useAuiState((s) => s.message.metadata.custom as MessageCustom);

/**
 * UserMessage is a question: short, scanned for, so a bubble on the right. The asker's name
 * is shown only when the message carries one — a mirrored thread, with more than one person
 * in it. In a web chat the only person asking is the person reading.
 */
function UserMessage() {
  const custom = useCustom();
  const createdAt = useAuiState((s) => s.message.createdAt);
  return (
    <MessagePrimitive.Root className="flex flex-col items-end gap-1">
      {custom?.author ? (
        <span data-testid="chat-author" className="pr-1 text-xs font-medium text-muted">
          {custom.author}
        </span>
      ) : null}
      <div
        data-testid="chat-message"
        data-role="user"
        data-author={custom?.author}
        title={absolute(ts(createdAt))}
        className="min-w-0 max-w-[min(36rem,85%)] rounded-2xl rounded-br-md bg-accent/14 px-4 py-2.5"
      >
        <MessagePrimitive.Parts components={{ Text: () => <MarkdownText /> }} />
        <ChatAttachments attachments={custom?.attachments ?? []} />
      </div>
    </MessagePrimitive.Root>
  );
}

/**
 * The assistant's thoughts and tool calls fold into one trail; a delegated task stands on
 * its own, because it is a unit of work somebody will want to open; answers are read.
 */
const groupBy = groupPartByType({
  reasoning: ["group-trail"],
  "tool-call": ["group-trail"],
  [`tool-call:${TASK_TOOL}`]: [],
});

/** Inside a task's block nothing folds again: the block is already the disclosure. */
const flat = groupPartByType({});

/**
 * AssistantMessage is everything said back between two questions — a document under a
 * name, the full measure of the column, where markdown has room to read as markdown.
 */
function AssistantMessage() {
  const { botName } = useContext(ChatContext);
  const custom = useCustom();
  const createdAt = useAuiState((s) => s.message.createdAt);
  const text = useAuiState((s) =>
    s.message.parts
      .map((p) => (p.type === "text" ? p.text : ""))
      .filter(Boolean)
      .join("\n\n"),
  );

  return (
    <MessagePrimitive.Root className="group/turn flex gap-3">
      <span
        aria-hidden
        className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-full border border-border bg-panel text-accent"
      >
        <Bot className="size-4" />
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex items-baseline gap-2">
          <span className="text-xs font-medium text-fg">{botName}</span>
          <span className="text-2xs text-faint" title={absolute(ts(createdAt))}>
            {relative(ts(createdAt))}
          </span>
        </div>
        <div data-testid="chat-message" data-role="assistant" className="min-w-0 space-y-3">
          <Parts />
        </div>
        <ChatAttachments attachments={custom?.attachments ?? []} />
        <CopyMessage text={text} />
      </div>
    </MessagePrimitive.Root>
  );
}

/** Parts renders one message's parts. The nested thread of a task uses it too, flat. */
function Parts({ nested = false }: { nested?: boolean }) {
  return (
    <MessagePrimitive.GroupedParts groupBy={nested ? flat : groupBy} indicator={nested ? "never" : "always"}>
      {({ part, children }) => {
        switch (part.type) {
          case "group-trail":
            return <Trail running={part.status.type === "running"} steps={part.indices.length}>{children}</Trail>;
          case "text":
            return <MarkdownText />;
          case "reasoning":
            return <MarkdownText className="text-sm leading-6 text-muted" />;
          case "tool-call":
            return part.toolName === TASK_TOOL ? <TaskBlock {...part} /> : <ToolCall {...part} />;
          case "indicator":
            return <Waiting />;
          default:
            return null;
        }
      }}
    </MessagePrimitive.GroupedParts>
  );
}

/**
 * Waiting is the turn before it has words, in the row the answer will land in. It shows
 * only on the last message of a busy chat: a message kept running by a task that outlived
 * its turn has the task's own block to say so.
 */
function Waiting() {
  const { busy, progress, taskId } = useContext(ChatContext);
  const isLast = useAuiState((s) => s.message.isLast);
  const empty = useAuiState((s) => s.message.parts.length === 0);
  if (!busy || !isLast) return null;
  return (
    <div data-testid="chat-progress" role="status" aria-live="polite" className="flex min-w-0 items-center gap-2 text-sm text-muted">
      <Dots />
      <span className="min-w-0 truncate animate-shimmer">{empty ? (progress ?? "Thinking") : "Working"}</span>
      {taskId ? (
        <Link to={`/tasks/${taskId}`} title={taskId} className="font-mono shrink-0 text-2xs text-accent hover:underline">
          {taskId}
        </Link>
      ) : null}
    </div>
  );
}

/**
 * Dots is the three-dot wait. index.css flattens every animation under
 * prefers-reduced-motion, so there is nothing to opt out of here.
 */
function Dots() {
  return (
    <span aria-hidden className="flex shrink-0 items-center gap-1">
      {[0, 150, 300].map((delay) => (
        <span key={delay} style={{ animationDelay: `${delay}ms` }} className="size-1.5 animate-pulse rounded-full bg-muted" />
      ))}
    </span>
  );
}

/**
 * useDisclosure is open while the work runs and closed once it is done, until a person
 * says otherwise — then their choice holds.
 */
function useDisclosure(running: boolean) {
  const [chosen, setChosen] = useState<boolean | undefined>(undefined);
  return [chosen ?? running, setChosen] as const;
}

/** Trail is a run of thoughts and tool calls: what the assistant did on its way to an answer. */
function Trail({ running, steps, children }: { running: boolean; steps: number; children: ReactNode }) {
  const [open, setOpen] = useDisclosure(running);
  return (
    <Collapsible open={open} onOpenChange={setOpen} data-testid="chat-trail">
      <CollapsibleTrigger className="group/trigger flex items-center gap-1.5 py-0.5 text-sm text-muted transition-colors hover:text-fg">
        <Brain className="size-3.5" />
        <span className={cn(running && "animate-shimmer")}>
          {running ? "Thinking" : `Worked through ${steps} ${steps === 1 ? "step" : "steps"}`}
        </span>
        <ChevronRight className="size-3.5 transition-transform group-data-[state=open]/trigger:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="data-[state=closed]:animate-collapsible-up data-[state=open]:animate-collapsible-down overflow-hidden">
        <div className="mt-2 ml-1.5 space-y-2 border-l border-hairline pl-4">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  );
}

/** ToolCall is one finished tool call: a line to scan, and its input and output to open. */
function ToolCall({ toolName, argsText, result, isError, artifact }: ToolCallMessagePartProps) {
  const title = (artifact as { title?: string } | undefined)?.title;
  const output = typeof result === "string" ? result : result === undefined ? "" : JSON.stringify(result, null, 2);
  const input = pretty(argsText);
  return (
    <Collapsible data-testid="chat-tool" className="min-w-0">
      <CollapsibleTrigger className="group/trigger flex w-full min-w-0 items-center gap-2 text-left text-sm text-muted transition-colors hover:text-fg">
        {isError ? <X className="size-3.5 shrink-0 text-err" /> : <Wrench className="size-3.5 shrink-0" />}
        <span className="shrink-0 font-mono text-xs text-fg">{toolName}</span>
        {title ? <span className="min-w-0 truncate text-xs">{title}</span> : null}
        <ChevronRight className="size-3.5 shrink-0 transition-transform group-data-[state=open]/trigger:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="data-[state=closed]:animate-collapsible-up data-[state=open]:animate-collapsible-down overflow-hidden">
        <div className="mt-1.5 space-y-1.5">
          {input ? <Pre label="Input">{input}</Pre> : null}
          {output ? <Pre label={isError ? "Error" : "Output"} error={isError}>{output}</Pre> : null}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

function Pre({ label, error, children }: { label: string; error?: boolean; children: string }) {
  return (
    <figure className="overflow-hidden rounded-lg border border-border bg-bg">
      <figcaption className="border-b border-hairline bg-panel/80 px-2.5 py-0.5 text-2xs text-faint">{label}</figcaption>
      <pre className={cn("max-h-64 overflow-auto px-2.5 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words", error && "text-err")}>
        {children}
      </pre>
    </figure>
  );
}

/** pretty indents a JSON input and leaves anything else as it came. */
function pretty(text: string): string {
  if (text === "") return "";
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

/**
 * TaskBlock is a delegated task: a subagent working in a container, with its own trail
 * nested inside the turn that started it. Its answer is not in here — it is said in the
 * turn, where the assistant's would be.
 */
function TaskBlock({ args, status, messages }: ToolCallMessagePartProps) {
  const { taskId } = args as TaskArgs;
  const running = status.type === "running";
  const failed = status.type === "incomplete";
  const [open, setOpen] = useDisclosure(running);
  const steps = messages?.[0]?.content.length ?? 0;
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      data-testid="chat-task"
      className="rounded-xl border border-border bg-panel/50"
    >
      <div className="flex items-center gap-2 px-3 py-2">
        <CollapsibleTrigger className="group/trigger flex min-w-0 flex-1 items-center gap-2 text-left text-sm">
          <Terminal className="size-4 shrink-0 text-muted" />
          <span className="font-medium text-fg">Task</span>
          <span className={cn("text-xs text-muted", running && "animate-shimmer")}>
            {running ? "working" : failed ? "stopped" : "done"}
            {steps > 0 ? ` · ${steps} ${steps === 1 ? "step" : "steps"}` : ""}
          </span>
          <ChevronRight className="size-3.5 shrink-0 text-muted transition-transform group-data-[state=open]/trigger:rotate-90" />
        </CollapsibleTrigger>
        <Link to={`/tasks/${taskId}`} title={taskId} className="font-mono shrink-0 text-2xs text-accent hover:underline">
          {taskId}
        </Link>
        {running ? (
          <LoaderCircle aria-label="Running" className="size-3.5 shrink-0 animate-spin text-run" />
        ) : failed ? (
          <X aria-label="Stopped" className="size-3.5 shrink-0 text-err" />
        ) : (
          <Check aria-label="Done" className="size-3.5 shrink-0 text-ok" />
        )}
      </div>
      <CollapsibleContent className="data-[state=closed]:animate-collapsible-up data-[state=open]:animate-collapsible-down overflow-hidden">
        <div className="space-y-2 border-t border-hairline px-4 py-3">
          {steps === 0 ? <p className="text-sm text-muted">Nothing yet.</p> : null}
          <MessagePartPrimitive.Messages>
            {() => (
              <MessagePrimitive.Root className="space-y-2">
                <Parts nested />
              </MessagePrimitive.Root>
            )}
          </MessagePartPrimitive.Messages>
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

function CopyMessage({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1600);
    return () => clearTimeout(id);
  }, [copied]);
  if (text.trim() === "") return null;
  return (
    <div className="flex opacity-0 transition-opacity group-hover/turn:opacity-100 group-focus-within/turn:opacity-100">
      <Tooltip label={copied ? "Copied" : "Copy"}>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={copied ? "Copied" : "Copy message"}
          onClick={() => {
            void navigator.clipboard?.writeText(text);
            setCopied(true);
          }}
        >
          {copied ? <Check className="text-ok" /> : <Copy />}
        </Button>
      </Tooltip>
    </div>
  );
}
