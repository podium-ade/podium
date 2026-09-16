import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowUp, Check, ChevronDown, FileText, Loader2, Paperclip, X } from "lucide-react";
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
import { humanBytes } from "../../lib/format";
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

export interface ChatComposerProps {
  /** disabled is true while a turn runs: turn-based, one in flight per conversation. */
  disabled: boolean;
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
}

/**
 * ChatComposer is the box a question is typed into.
 *
 * Enter sends and Shift+Enter makes a newline, which is what a chat box does; ⌘/Ctrl+Enter
 * sends too, for the people whose muscle memory says so. While a turn runs it is disabled —
 * the server refuses a concurrent send anyway (FailedPrecondition), and saying so before the
 * click is the friendlier half of the same rule.
 *
 * Files paste, drop or pick. A text file is inlined into the stored message; an image is
 * previewed here and named in the message, because the send RPC is still text.
 */
export function ChatComposer({
  disabled,
  agents,
  assistant,
  choice,
  onChoiceChange,
  onSend,
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

  const canSend = !disabled && (text.trim() !== "" || files.length > 0);

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
            "relative rounded-3xl border bg-panel shadow-lg transition-[border-color,box-shadow] duration-150",
            dragging
              ? "border-accent ring-2 ring-accent/30"
              : "border-border focus-within:border-accent/55 focus-within:ring-2 focus-within:ring-ring/25",
          )}
        >
          {dragging ? (
            <div className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-3xl bg-accent/8 text-sm font-medium text-accent">
              Drop files to attach
            </div>
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
            value={text}
            rows={1}
            disabled={disabled}
            placeholder={disabled ? "Working…" : "Ask anything about your stack"}
            onChange={(e) => setText(e.target.value)}
            onPaste={(e) => {
              const pasted = [...e.clipboardData.files];
              if (pasted.length === 0) return;
              void takeFiles(pasted);
            }}
            onKeyDown={(e) => {
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

          <div className="flex flex-nowrap items-center gap-1.5 overflow-x-auto px-2 pb-2">
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
              <p className="hidden items-center gap-1 text-2xs text-faint lg:flex">
                <Kbd>Enter</Kbd> sends
                <span className="px-0.5">·</span>
                <Kbd>Shift</Kbd>
                <Kbd>Enter</Kbd> newline
              </p>

              <Tooltip label={disabled ? "Working" : "Send"}>
                <Button
                  type="button"
                  size="icon-sm"
                  data-testid="chat-send"
                  disabled={!canSend}
                  aria-label={disabled ? "Working" : "Send"}
                  onClick={send}
                  className="rounded-full"
                >
                  {disabled ? <Loader2 className="animate-spin" /> : <ArrowUp />}
                </Button>
              </Tooltip>
            </div>
          </div>
        </div>
        <p className="mt-1.5 px-2 text-2xs text-faint">
          {fileError !== ""
            ? fileError
            : "One question at a time: a turn runs to an answer and exits. Paste or drop files to attach them."}
        </p>
      </div>
    </div>
  );
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
        <p className="px-2.5 pt-1.5 pb-1 text-2xs font-medium tracking-wide text-faint uppercase">
          Effort
        </p>
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
