import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowUp,
  AtSign,
  Check,
  ChevronDown,
  FileText,
  ListOrdered,
  Loader2,
  Paperclip,
  Pencil,
  SkipForward,
  Square,
  Users,
  X,
} from "lucide-react";
import type { AgentBackend, Assistant } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import {
  acceptList,
  addFiles,
  composeWithFiles,
  modelAcceptsImages,
  revokePreview,
  type PendingFile,
} from "../../lib/chatFiles";
import { agent } from "../../lib/client";
import { humanBytes } from "../../lib/format";
import {
  PODIUM_MENTION,
  insertMention,
  mentionQuery,
  mentionSuggestions,
  mentionsPodium,
} from "../../lib/mentions";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Kbd } from "../ui/kbd";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";
import { Tooltip } from "../ui/tooltip";
import { AgentPicker } from "./AgentPicker";
import { BackendMark } from "./BackendMark";

/** MIN_PX and MAX_PX bound the auto-growing box: one line, up to about eight. */
const MIN_PX = 44;
const MAX_PX = 220;

const EFFORT_HINT: Record<string, string> = {
  "": "The model's own default",
  low: "Faster, shorter answers",
  medium: "Balanced",
  high: "Thorough",
  xhigh: "Deeper reasoning",
  max: "Most thorough. Uses the limit sooner",
};

/**
 * QueuedItem is one message waiting for its turn in the chat's shared queue — anybody's, in
 * the order Podium will answer them.
 */
export interface QueuedItem {
  /** id names it for onRemoveQueued. */
  id: string;
  text: string;
  /** author is who asked, as shown; mine is whether that is the person at this composer. */
  author: string;
  mine: boolean;
  /**
   * editable is true for one of this person's messages that is not in the record yet: taking
   * it back into the box and sending it again leaves no copy behind.
   */
  editable: boolean;
}

export interface ChatComposerProps {
  /**
   * disabled is true while a send is in flight. A turn running does not disable the box any
   * more (phase 4): what is sent meanwhile is queued as a turn of its own.
   */
  disabled: boolean;
  /** running is true while a turn of this chat is in flight; the send button can stop it. */
  running?: boolean;
  /** onStop stops the running turn. Absent hides the control. */
  onStop?: () => void;
  /**
   * multiplayer is true once the chat has more than one person in it: a message must
   * mention Podium (or the chat's assistant) to start a turn, and the composer says so.
   */
  multiplayer?: boolean;
  /** mirrored is where a mirrored conversation lives ("slack", "github", "linear"); empty for a web chat. */
  mirrored?: string;
  /** personality is the chat's personal assistant's name, which also starts a turn. */
  personality?: string;
  /**
   * queued is the chat's shared queue (the server's turn queue): what anybody put to Podium
   * while it was answering, oldest first. Each goes out as a turn of its own, in order.
   */
  queued?: readonly QueuedItem[];
  /**
   * queueing is true when a message for Podium sent now would wait in the queue rather than
   * be answered at once: something is being answered, or something is already waiting.
   */
  queueing?: boolean;
  /** onInterrupt stops the answer in progress; the queue then goes on, in order. */
  onInterrupt?: () => void;
  /** onRemoveQueued takes back one of this person's queued messages. */
  onRemoveQueued?: (id: string) => void;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  /** assistant is what answers this conversation, and what choice overrides. */
  assistant?: Assistant;
  /**
   * choice is what ANSWERS the next message, overriding the assistant's own model. INHERIT
   * — the default — means whatever profile.yaml says, which is what the picker shows.
   */
  choice: AgentChoice;
  onChoiceChange: (next: AgentChoice) => void;
  onSend: (text: string, choice: AgentChoice) => void;
  /** focusOnMount puts the caret in the box. The arrival uses it; a thread does not. */
  focusOnMount?: boolean;
}

/**
 * ChatComposer is the box a question is typed into.
 *
 * Enter sends and Shift+Enter makes a newline, which is what a chat box does; ⌘/Ctrl+Enter
 * sends too, for the people whose muscle memory says so. While Podium is answering, a message
 * for it is queued above the box (QueuedList) and goes out after the answer, one per turn.
 *
 * Files paste, drop or pick. A text file is inlined into the stored message; an image is
 * previewed here and named in the message, because the send RPC is still text.
 */
export function ChatComposer({
  disabled,
  running = false,
  onStop,
  multiplayer = false,
  mirrored = "",
  personality = "",
  queued = [],
  queueing = false,
  onInterrupt,
  onRemoveQueued,
  agents,
  assistant,
  choice,
  onChoiceChange,
  onSend,
  focusOnMount = false,
}: ChatComposerProps) {
  const [text, setText] = useState("");
  const [files, setFiles] = useState<PendingFile[]>([]);
  const [fileError, setFileError] = useState("");
  const [dragging, setDragging] = useState(false);
  const box = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);

  const inherited: AgentChoice = assistant
    ? { agent: assistant.agent, model: assistant.model, effort: assistant.effort }
    : INHERIT;
  const effective = choice.model === "" ? inherited : choice;
  const acceptImages = modelAcceptsImages(effective.model);
  const efforts = effortsFor(agents, effective);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "0px";
    el.style.height = `${Math.max(MIN_PX, Math.min(el.scrollHeight, MAX_PX))}px`;
  }, [text]);

  useEffect(() => {
    return () => {
      files.forEach(revokePreview);
    };
    // Intentionally once: each chip revokes itself on remove, and this only catches unmount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const takeFiles = async (incoming: File[]) => {
    if (disabled || incoming.length === 0) return;
    const { added, errors } = await addFiles(incoming, files, { acceptImages });
    if (added.length > 0) setFiles((cur) => [...cur, ...added]);
    setFileError(errors[0] ?? "");
  };

  const removeFile = (id: string) => {
    setFiles((cur) => {
      const gone = cur.find((f) => f.id === id);
      if (gone) revokePreview(gone);
      return cur.filter((f) => f.id !== id);
    });
  };

  // The @-mention list: Podium, the chat's assistant, and people, filtered by what is typed.
  const [caret, setCaret] = useState(0);
  const [pick, setPick] = useState(0);
  const [mentionClosed, setMentionClosed] = useState(false);
  const typing = mentionClosed ? undefined : mentionQuery(text, caret);
  const members = useQuery({
    queryKey: ["agent", "members", typing?.query ?? ""],
    queryFn: () => agent.searchMembers({ query: typing?.query ?? "", limit: 8 }),
    enabled: typing !== undefined && multiplayer,
    staleTime: 30_000,
  });
  const suggestions = mentionSuggestions(typing?.query, personality, multiplayer ? (members.data?.members ?? []) : []);
  const choose = (token: string) => {
    if (!typing) return;
    const next = insertMention(text, typing.start, caret, token);
    setText(next.text);
    setCaret(next.caret);
    setPick(0);
    requestAnimationFrame(() => {
      box.current?.focus();
      box.current?.setSelectionRange(next.caret, next.caret);
    });
  };

  const canSend = !disabled && (text.trim() !== "" || files.length > 0);
  // In a multiplayer chat only a mention is a turn; the button says which this will be.
  const toPodium = !multiplayer || mentionsPodium(text, personality);
  // A message for Podium sent while it is answering waits in the queue; the button says so.
  const willQueue = queueing && toPodium;
  const sendLabel = disabled
    ? "Working"
    : willQueue
      ? multiplayer
        ? "Queue for Podium"
        : "Queue"
      : multiplayer
        ? toPodium
          ? "Send to Podium"
          : "Send"
        : "Send";
  // Editing a queued message takes it out of the queue and back into the box, ahead of
  // anything already typed there.
  const editQueued = (id: string) => {
    const item = queued.find((q) => q.id === id);
    if (!item) return;
    onRemoveQueued?.(id);
    const next = text.trim() === "" ? item.text : `${item.text}\n\n${text}`;
    setText(next);
    requestAnimationFrame(() => {
      box.current?.focus();
      box.current?.setSelectionRange(item.text.length, item.text.length);
    });
  };

  const send = () => {
    if (!canSend) return;
    const payload = composeWithFiles(text, files);
    if (payload === "") return;
    onSend(payload, choice);
    setText("");
    files.forEach(revokePreview);
    setFiles([]);
    setFileError("");
  };

  return (
    <div className="bg-gradient-to-t from-bg via-bg/95 to-transparent pt-4">
      <div className="mx-auto w-full max-w-3xl px-4 pb-3 sm:px-5">
        {multiplayer ? (
          <p
            data-testid="chat-multiplayer-hint"
            className="mb-1.5 flex items-center gap-1.5 px-2 text-2xs text-muted"
          >
            <Users className="size-3" aria-hidden />
            Shared chat — mention <span className="font-medium text-fg">{PODIUM_MENTION}</span> to ask it
            something.
          </p>
        ) : null}
        {queued.length > 0 ? (
          <QueuedList
            items={queued}
            multiplayer={multiplayer}
            running={running}
            onInterrupt={onInterrupt}
            onEdit={editQueued}
            onRemove={onRemoveQueued}
          />
        ) : null}
        <div
          onDragEnter={(e) => {
            e.preventDefault();
            dragDepth.current++;
            if (!disabled) setDragging(true);
          }}
          onDragOver={(e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = disabled ? "none" : "copy";
          }}
          onDragLeave={() => {
            dragDepth.current = Math.max(0, dragDepth.current - 1);
            if (dragDepth.current === 0) setDragging(false);
          }}
          onDrop={(e) => {
            e.preventDefault();
            dragDepth.current = 0;
            setDragging(false);
            void takeFiles([...e.dataTransfer.files]);
          }}
          className={cn(
            "chat-composer relative rounded-3xl border bg-panel shadow-lg transition-[border-color,box-shadow] duration-150",
            dragging ? "border-accent ring-2 ring-accent" : "border-border",
          )}
        >
          {dragging ? (
            <div className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-3xl bg-raised text-sm font-medium text-accent">
              Drop files to attach
            </div>
          ) : null}

          {typing && suggestions.length > 0 ? (
            <ul
              role="listbox"
              aria-label="Mention"
              data-testid="chat-mentions"
              className="absolute bottom-full left-3 z-20 mb-2 max-h-64 w-72 overflow-y-auto rounded-xl border border-border bg-panel p-1 shadow-lg"
            >
              {suggestions.map((sug, i) => (
                <li key={sug.key} role="option" aria-selected={i === pick}>
                  <button
                    type="button"
                    data-testid="chat-mention-option"
                    onMouseDown={(e) => {
                      e.preventDefault();
                      choose(sug.token);
                    }}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm",
                      i === pick ? "bg-raised text-fg" : "text-muted hover:bg-raised",
                    )}
                  >
                    <AtSign className="size-3.5 shrink-0" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">{sug.label}</span>
                    {sug.hint ? <span className="truncate text-2xs text-faint">{sug.hint}</span> : null}
                  </button>
                </li>
              ))}
            </ul>
          ) : null}

          {files.length > 0 ? (
            <ul
              data-testid="chat-pending-files"
              className="flex flex-wrap gap-2 px-3 pt-3"
            >
              {files.map((f) => (
                <FileChip
                  key={f.id}
                  file={f}
                  disabled={disabled}
                  onRemove={() => removeFile(f.id)}
                />
              ))}
            </ul>
          ) : null}

          <textarea
            ref={box}
            data-testid="chat-composer"
            aria-label="Message"
            autoFocus={focusOnMount}
            value={text}
            rows={1}
            disabled={disabled}
            placeholder={
              disabled
                ? "Working…"
                : multiplayer
                  ? `Message the chat, or mention ${PODIUM_MENTION}`
                  : "Ask anything about your stack"
            }
            onChange={(e) => {
              setText(e.target.value);
              setCaret(e.target.selectionStart ?? e.target.value.length);
              setMentionClosed(false);
              setPick(0);
            }}
            onSelect={(e) => setCaret(e.currentTarget.selectionStart ?? 0)}
            onPaste={(e) => {
              const pasted = [...e.clipboardData.files];
              if (pasted.length === 0) return;
              void takeFiles(pasted);
            }}
            onKeyDown={(e) => {
              if (typing && suggestions.length > 0) {
                if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  const step = e.key === "ArrowDown" ? 1 : -1;
                  setPick((p) => (p + step + suggestions.length) % suggestions.length);
                  return;
                }
                if (e.key === "Enter" || e.key === "Tab") {
                  e.preventDefault();
                  choose(suggestions[Math.min(pick, suggestions.length - 1)].token);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setMentionClosed(true);
                  return;
                }
              }
              if (e.key === "Escape") {
                box.current?.blur();
                return;
              }
              if (e.key !== "Enter") return;
              if (e.shiftKey) return;
              e.preventDefault();
              send();
            }}
            className="block w-full resize-none bg-transparent px-4 py-3.5 text-base leading-6 text-fg outline-none placeholder:text-faint disabled:opacity-60"
          />

          <div className="@container flex flex-nowrap items-center gap-1.5 overflow-x-auto px-2 pb-2">
            <input
              ref={fileInput}
              data-testid="chat-attach-input"
              type="file"
              multiple
              hidden
              accept={acceptList(acceptImages)}
              disabled={disabled}
              onChange={(e) => {
                void takeFiles([...(e.target.files ?? [])]);
                e.target.value = "";
              }}
            />
            <Tooltip
              label={
                acceptImages
                  ? "Attach files or paste an image"
                  : "Attach files. This model does not take images"
              }
            >
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                data-testid="chat-attach"
                disabled={disabled}
                aria-label="Attach files"
                onClick={() => fileInput.current?.click()}
                className="rounded-full"
              >
                <Paperclip />
              </Button>
            </Tooltip>

            <RunConfig
              agents={agents}
              assistant={assistant}
              choice={choice}
              onChange={onChoiceChange}
              disabled={disabled}
            />

            {efforts.length > 0 ? (
              <EffortMenu
                efforts={efforts}
                value={choice.effort}
                inherited={inherited.effort}
                disabled={disabled}
                onChange={(effort) => onChoiceChange({ ...choice, effort })}
              />
            ) : null}

            <div className="ml-auto flex items-center gap-2">
              {/* By the composer's width, not the window's: beside the desktop the composer is narrow on a wide screen. */}
              <p className="hidden items-center gap-1 text-2xs text-faint @[40rem]:flex">
                <Kbd>Enter</Kbd> sends
                <span className="px-0.5">·</span>
                <Kbd>Shift</Kbd>
                <Kbd>Enter</Kbd> newline
              </p>

              {running && onStop ? (
                <Tooltip label="Stop the answer">
                  <Button
                    type="button"
                    variant="outline"
                    size="icon-sm"
                    data-testid="chat-stop"
                    aria-label="Stop the answer"
                    onClick={onStop}
                    className="rounded-full"
                  >
                    <Square className="fill-current" />
                  </Button>
                </Tooltip>
              ) : null}
              {multiplayer ? (
                <span data-testid="chat-send-label" className="text-2xs text-faint">
                  {sendLabel}
                </span>
              ) : null}
              <Tooltip label={sendLabel}>
                <Button
                  type="button"
                  size="icon-sm"
                  data-testid="chat-send"
                  data-to-podium={toPodium ? "true" : "false"}
                  disabled={!canSend}
                  aria-label={sendLabel}
                  onClick={send}
                  className="rounded-full"
                >
                  {disabled ? <Loader2 className="animate-spin" /> : willQueue ? <ListOrdered /> : <ArrowUp />}
                </Button>
              </Tooltip>
            </div>
          </div>
        </div>
        <p data-testid="chat-composer-note" className="mt-1.5 px-2 text-2xs text-faint">
          {fileError !== ""
            ? fileError
            : mirrored !== ""
              ? `Messages here stay in Podium — they aren't posted to ${surfaceLabel(mirrored)}.`
              : running
                ? multiplayer
                  ? `A message mentioning ${PODIUM_MENTION} is queued until the answer in progress is done. Anything else is posted now.`
                  : "A message sent now is queued, and gets its own turn once the answer in progress is done."
                : "Paste or drop files to attach them."}
        </p>
      </div>
    </div>
  );
}

/**
 * QueuedList is the chat's shared queue, oldest first, above the box: what everybody put to
 * Podium while it was answering, in the order it will be answered. A person can take their own
 * back into the box to edit it, or drop it; anybody who may write the chat can interrupt the
 * answer in progress, and the queue goes on, in order.
 */
function QueuedList({
  items,
  multiplayer,
  running,
  onInterrupt,
  onEdit,
  onRemove,
}: {
  items: readonly QueuedItem[];
  multiplayer: boolean;
  running: boolean;
  onInterrupt?: () => void;
  onEdit: (id: string) => void;
  onRemove?: (id: string) => void;
}) {
  const firstMine = items.findIndex((q) => q.mine);
  // On a phone the queue and the box would otherwise leave no room for the conversation: the
  // first few rows show, one line each, and the rest are a tap away.
  const [expanded, setExpanded] = useState(false);
  const hidden = expanded ? 0 : Math.max(0, items.length - PHONE_ROWS);
  return (
    <div data-testid="chat-pending" className="mb-2 overflow-hidden rounded-2xl border border-border bg-panel">
      <div className="flex items-center gap-2 border-b border-hairline px-3 py-1.5 text-2xs text-muted">
        <ListOrdered className="size-3.5" aria-hidden />
        <span data-testid="chat-pending-status" className="min-w-0 flex-1 truncate">
          {items.length} queued
          {multiplayer && firstMine >= 0 ? ` · yours is ${ordinal(firstMine + 1)}` : ""}
        </span>
        {running && onInterrupt ? (
          <Tooltip label="Stop the answer in progress; the queue goes next, in order">
            <Button
              type="button"
              variant="outline"
              size="xs"
              data-testid="chat-pending-interrupt"
              onClick={onInterrupt}
              className="min-h-6 shrink-0 rounded-full px-2.5 text-2xs [&_svg]:size-3"
            >
              <SkipForward aria-hidden />
              Interrupt &amp; send
            </Button>
          </Tooltip>
        ) : null}
      </div>
      <ol className="max-h-40 divide-y divide-hairline overflow-y-auto">
        {items.map((item, i) => (
          <li
            key={item.id}
            data-testid="chat-pending-item"
            data-mine={item.mine ? "true" : "false"}
            className={cn(
              "flex items-center gap-2 py-1 pr-1.5 pl-3 text-sm",
              item.mine ? "text-fg" : "text-muted",
              i >= PHONE_ROWS && !expanded && "max-sm:hidden",
            )}
          >
            <span className="tabular w-4 shrink-0 text-right text-2xs text-faint">{i + 1}</span>
            <span className="min-w-0 flex-1">
              {multiplayer && item.author ? (
                <span data-testid="chat-pending-author" className="block text-2xs font-medium text-muted">
                  {item.mine ? "You" : item.author}
                </span>
              ) : null}
              <span className="line-clamp-1 whitespace-pre-wrap break-words sm:line-clamp-2">
                {item.text || <span className="italic text-faint">A message</span>}
              </span>
            </span>
            {item.mine ? (
              <span className="flex shrink-0 items-center gap-0.5">
                {item.editable ? (
                  <Tooltip label="Edit">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label="Edit queued message"
                      className="size-7"
                      data-testid="chat-pending-edit"
                      onClick={() => onEdit(item.id)}
                    >
                      <Pencil />
                    </Button>
                  </Tooltip>
                ) : null}
                {onRemove ? (
                  <Tooltip label="Remove">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label="Remove queued message"
                      className="size-7"
                      data-testid="chat-pending-remove"
                      onClick={() => onRemove(item.id)}
                    >
                      <X />
                    </Button>
                  </Tooltip>
                ) : null}
              </span>
            ) : null}
          </li>
        ))}
      </ol>
      {hidden > 0 || (expanded && items.length > PHONE_ROWS) ? (
        <button
          type="button"
          data-testid="chat-pending-more"
          onClick={() => setExpanded((v) => !v)}
          className="w-full border-t border-hairline px-3 py-1.5 text-left text-2xs text-muted hover:text-fg sm:hidden"
        >
          {expanded ? "Show less" : `+${hidden} more`}
        </button>
      ) : null}
    </div>
  );
}

/** PHONE_ROWS is how many queued messages a phone shows before "+N more". */
const PHONE_ROWS = 2;

/** ordinal is 1st, 2nd, 3rd, 4th… for the "yours is" line. */
function ordinal(n: number): string {
  const tens = n % 100;
  if (tens >= 11 && tens <= 13) return `${n}th`;
  return `${n}${["th", "st", "nd", "rd"][n % 10] ?? "th"}`;
}

function FileChip({
  file,
  disabled,
  onRemove,
}: {
  file: PendingFile;
  disabled: boolean;
  onRemove: () => void;
}) {
  return (
    <li
      data-testid="chat-pending-file"
      className="flex max-w-56 items-center gap-2 rounded-xl border border-border bg-bg py-1 pr-1 pl-1"
    >
      {file.kind === "image" && file.previewUrl ? (
        <img
          src={file.previewUrl}
          alt=""
          className="size-9 shrink-0 rounded-lg object-cover"
        />
      ) : (
        <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-raised text-muted">
          <FileText className="size-3.5" />
        </span>
      )}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-2xs font-medium text-fg">{file.name}</span>
        <span className="tabular block text-2xs text-faint">{humanBytes(file.size)}</span>
      </span>
      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        disabled={disabled}
        aria-label={`Remove ${file.name}`}
        onClick={onRemove}
        className="rounded-full"
      >
        <X />
      </Button>
    </li>
  );
}

/**
 * RunConfig is the one choice this composer offers: which model answers.
 *
 * The catalogue is inlined here rather than nested behind a second trigger: a menu inside
 * a menu is what used to paint the list off the bottom of the window. One click opens a
 * portaled sheet that flips above the composer and scrolls inside the remaining viewport.
 */
function RunConfig({
  agents,
  assistant,
  choice,
  onChange,
  disabled,
}: {
  agents: AgentBackend[];
  assistant?: Assistant;
  choice: AgentChoice;
  onChange: (next: AgentChoice) => void;
  disabled: boolean;
}) {
  const inherited: AgentChoice = assistant
    ? { agent: assistant.agent, model: assistant.model, effort: assistant.effort }
    : INHERIT;
  const effective = choice.model === "" ? inherited : choice;
  const backendID = choice.model === "" ? inherited.agent : choice.agent;

  return (
    <Popover modal={false}>
      <Tooltip label="Which model answers">
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            data-testid="chat-run-config"
            className="max-w-56 shrink-0 rounded-full"
          >
            <BackendMark id={backendID} />
            {effective.model === "" ? (
              <span className="max-w-40 truncate">Default model</span>
            ) : (
              <span className="max-w-40 truncate font-mono">{effective.model}</span>
            )}
            <ChevronDown />
          </Button>
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent
        align="start"
        side="top"
        className="flex w-80 flex-col gap-2 overflow-hidden p-0"
      >
        <div className="shrink-0 space-y-0.5 px-3 pt-2.5 pb-1">
          <p className="text-xs font-medium text-fg">Which model answers</p>
          <p className="text-2xs leading-relaxed text-faint">
            It applies to the messages you send from now on and is never saved. Tasks
            {assistantName(assistant)} starts keep their own playbook&apos;s model.
          </p>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
          <AgentPicker
            label="This message"
            value={choice}
            onChange={onChange}
            agents={agents}
            disabled={disabled}
            embedded
            hideEffort
            inherit={{ label: "Default model", hint: inherited.model || "whatever the profile says" }}
            inherited={inherited}
          />
        </div>
      </PopoverContent>
    </Popover>
  );
}

const EFFORT_LABEL: Record<string, string> = {
  "": "Auto",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};

function effortLabel(level: string): string {
  return EFFORT_LABEL[level] ?? level;
}

/**
 * EffortMenu is how Claude, Gemini and ChatGPT expose depth on the composer: a chip
 * that names the current level, opening a list with one-line tradeoffs. A drag slider
 * on the bar fights the layout and reads as a volume control, which this is not.
 */
function EffortMenu({
  efforts,
  value,
  inherited,
  disabled,
  onChange,
}: {
  efforts: string[];
  value: string;
  inherited: string;
  disabled: boolean;
  onChange: (effort: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const options = ["", ...efforts];

  return (
    <Popover open={open} onOpenChange={setOpen} modal={false}>
      <Tooltip label="How thoroughly this message is answered">
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            data-testid="chat-effort"
            aria-label="Reasoning effort"
            className="shrink-0 rounded-full"
          >
            {effortLabel(value)}
            <ChevronDown />
          </Button>
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent align="start" side="top" className="w-64 p-1">
        <p className="px-2.5 pt-1.5 pb-1 text-2xs font-medium text-faint">Effort</p>
        <div role="radiogroup" aria-label="Effort levels">
          {options.map((level) => {
            const selected = value === level;
            const hint =
              level === "" && inherited
                ? `currently ${effortLabel(inherited).toLowerCase()}`
                : (EFFORT_HINT[level] ?? "");
            return (
              <button
                key={level || "auto"}
                type="button"
                role="radio"
                aria-checked={selected}
                aria-label={effortLabel(level)}
                onClick={() => {
                  onChange(level);
                  setOpen(false);
                }}
                className={cn(
                  "flex w-full items-start gap-2 rounded-md px-2.5 py-1.5 text-left outline-none",
                  "hover:bg-raised focus-visible:ring-2 focus-visible:ring-ring/50",
                )}
              >
                <Check
                  aria-hidden
                  className={cn("mt-0.5 size-3.5 shrink-0 text-accent", selected ? "" : "opacity-0")}
                />
                <span className="min-w-0 flex-1">
                  <span className="block text-xs font-medium text-fg">{effortLabel(level)}</span>
                  {hint ? <span className="block text-2xs leading-snug text-muted">{hint}</span> : null}
                </span>
              </button>
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
}

/** Shown while ListAgents is still in flight, or for a model id typed by hand. */
const FALLBACK_EFFORTS = ["low", "medium", "high", "xhigh", "max"];

function effortsFor(agents: AgentBackend[], effective: AgentChoice): string[] {
  for (const b of agents) {
    const m = b.models.find((x) => x.id === effective.model);
    if (m) return [...m.efforts];
  }
  const backend = agents.find((a) => a.id === effective.agent);
  if (backend) {
    const out: string[] = [];
    for (const m of backend.models) {
      for (const e of m.efforts) {
        if (!out.includes(e)) out.push(e);
      }
    }
    if (out.length > 0) return out;
  }
  return FALLBACK_EFFORTS;
}

/** assistantName reads as " Podium" mid-sentence, and as nothing at all before it loads. */
function assistantName(assistant?: Assistant): string {
  return assistant?.displayName ? ` ${assistant.displayName}` : "";
}

/** surfaceLabel is a source kind as a person reads it. */
function surfaceLabel(kind: string): string {
  switch (kind) {
    case "slack":
      return "Slack";
    case "github":
      return "GitHub";
    case "linear":
      return "Linear";
    default:
      return kind;
  }
}

