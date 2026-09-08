import { useLayoutEffect, useRef, useState } from "react";
import { ArrowUp, ChevronDown, Loader2 } from "lucide-react";
import type { AgentBackend, Assistant } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { Button } from "../ui/button";
import { Kbd } from "../ui/kbd";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";
import { Tooltip } from "../ui/tooltip";
import { AgentPicker } from "./AgentPicker";
import { BackendMark } from "./BackendMark";

/** MIN_PX and MAX_PX bound the auto-growing box: one line, up to about eight. */
const MIN_PX = 44;
const MAX_PX = 200;

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
  const box = useRef<HTMLTextAreaElement>(null);

  // The box grows with what is in it rather than counting newlines, so a wrapped paragraph
  // is as tall as it looks. Measured before paint: doing it in an effect shows one frame of
  // the old height on every keystroke.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "0px";
    el.style.height = `${Math.max(MIN_PX, Math.min(el.scrollHeight, MAX_PX))}px`;
  }, [text]);

  const send = () => {
    const trimmed = text.trim();
    if (trimmed === "" || disabled) return;
    onSend(trimmed, choice);
    setText("");
  };

  return (
    <div className="border-t border-border bg-panel">
      <div className="mx-auto w-full max-w-3xl px-5 py-3">
        <div className="rounded-xl border border-border bg-bg shadow-xs transition-[border-color,box-shadow] duration-150 focus-within:border-accent/60 focus-within:ring-2 focus-within:ring-ring/30">
          <textarea
            ref={box}
            data-testid="chat-composer"
            aria-label="Message"
            value={text}
            rows={1}
            disabled={disabled}
            placeholder={disabled ? "Working…" : "Ask a question about your stack"}
            onChange={(e) => setText(e.target.value)}
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
            className="block w-full resize-none bg-transparent px-3.5 py-3 text-sm text-fg outline-none placeholder:text-faint disabled:opacity-60"
          />

          <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-2 py-2">
            {/* The only choice here is which model answers. Which playbook a piece of work
                runs in is not a choice a person makes per message any more: the turn picks
                one per task, and it may pick several. */}
            <RunConfig
              agents={agents}
              assistant={assistant}
              choice={choice}
              onChange={onChoiceChange}
              disabled={disabled}
            />

            <div className="ml-auto flex items-center gap-2">
              <p className="hidden items-center gap-1 text-2xs text-faint lg:flex">
                <Kbd>Enter</Kbd> sends
                <span className="px-0.5">·</span>
                <Kbd>Shift</Kbd>
                <Kbd>Enter</Kbd> for a new line
              </p>

              <Button
                type="button"
                size="sm"
                data-testid="chat-send"
                disabled={disabled || text.trim() === ""}
                onClick={send}
              >
                {disabled ? <Loader2 className="animate-spin" /> : null}
                {disabled ? "Working…" : "Send"}
                {disabled ? null : <ArrowUp />}
              </Button>
            </div>
          </div>
        </div>
        <p className="mt-1.5 px-1 text-2xs text-faint">
          One question at a time: a turn runs to an answer and exits.
        </p>
      </div>
    </div>
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
          <Button type="button" variant="outline" size="sm" disabled={disabled} data-testid="chat-run-config">
            <BackendMark id={backendID} />
            {effective.model === "" ? (
              <span className="max-w-40 truncate">Default model</span>
            ) : (
              <span className="max-w-40 truncate font-mono">{effective.model}</span>
            )}
            {effective.effort ? <span className="text-faint">· {effective.effort}</span> : null}
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
            inherit={{ label: "Default model", hint: inherited.model || "whatever the profile says" }}
            inherited={inherited}
          />
        </div>
      </PopoverContent>
    </Popover>
  );
}

/** assistantName reads as " Podium" mid-sentence, and as nothing at all before it loads. */
function assistantName(assistant?: Assistant): string {
  return assistant?.displayName ? ` ${assistant.displayName}` : "";
}
