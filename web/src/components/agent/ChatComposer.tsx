import { useLayoutEffect, useRef, useState } from "react";
import { ArrowUp, ChevronDown, Loader2 } from "lucide-react";
import type { AgentBackend, Playbook } from "../../gen/podium/agent/v1/agent_pb";
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

/** PLAYBOOK_PREFIX matches a leading /playbook, the same rule the conductor applies. */
const PLAYBOOK_PREFIX = /^\/([a-z][a-z0-9-]{0,31})(\s+|$)/;

export interface ChatComposerProps {
  playbooks: Playbook[];
  /** playbook is the playbook the next message will use. */
  playbook: string;
  onPlaybookChange: (name: string) => void;
  /** disabled is true while a turn runs: turn-based, one in flight per conversation. */
  disabled: boolean;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  /**
   * choice is what the next message runs on, overriding the playbook's own. INHERIT — the
   * default — means whatever the playbook says, which is what the picker shows.
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
  playbooks,
  playbook,
  onPlaybookChange,
  disabled,
  agents,
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

  // Typing /analyst … is the same choice as clicking the chip, so the chip follows the text.
  // It happens on the keystroke rather than in an effect: an effect here would render the
  // composer twice for every character typed.
  const retype = (next: string) => {
    setText(next);
    const m = PLAYBOOK_PREFIX.exec(next);
    if (m && m[1] !== playbook && playbooks.some((s) => s.name === m[1])) onPlaybookChange(m[1]);
  };

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
            onChange={(e) => retype(e.target.value)}
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
            <PlaybookChip playbooks={playbooks} playbook={playbook} onChange={onPlaybookChange} disabled={disabled} />
            {/* The model is chosen per MESSAGE, beside the playbook and not inside it. A playbook
                is "which job"; the model is "what runs it". Folding the second into the
                first is what makes a profile fill up with playbooks that differ by one field. */}
            <RunConfig
              playbooks={playbooks}
              playbook={playbook}
              agents={agents}
              choice={choice}
              onChange={onChoiceChange}
              disabled={disabled}
            />

            <p className="ml-auto hidden items-center gap-1 text-2xs text-faint md:flex">
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
        <p className="mt-1.5 px-1 text-2xs text-faint">
          One question at a time: a turn runs to an answer and exits.
        </p>
      </div>
    </div>
  );
}

/**
 * RunConfig is the model half of the decision, folded into a popover.
 *
 * It is a popover rather than a row of controls because the answer is almost always "the
 * playbook's own", and a composer that spends three lines saying so is a composer with less
 * room to type in. The closed control still names what will actually run, so nothing is
 * hidden — only folded.
 */
function RunConfig({
  playbooks,
  playbook,
  agents,
  choice,
  onChange,
  disabled,
}: {
  playbooks: Playbook[];
  playbook: string;
  agents: AgentBackend[];
  choice: AgentChoice;
  onChange: (next: AgentChoice) => void;
  disabled: boolean;
}) {
  const inherited = playbookChoice(playbooks, playbook);
  const effective = choice.model === "" ? inherited : choice;
  const backendID = choice.model === "" ? inherited.agent : choice.agent;

  return (
    <Popover>
      <Tooltip label="What this message runs on">
        <PopoverTrigger asChild>
          <Button type="button" variant="outline" size="sm" disabled={disabled} data-testid="chat-run-config">
            <BackendMark id={backendID} />
            {effective.model === "" ? (
              <span className="max-w-40 truncate">The playbook&apos;s model</span>
            ) : (
              <span className="max-w-40 truncate font-mono">{effective.model}</span>
            )}
            {effective.effort ? (
              <span className="text-faint">· {effective.effort}</span>
            ) : null}
            <ChevronDown />
          </Button>
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent align="start" side="top" className="w-90 space-y-3">
        <div className="space-y-0.5">
          <p className="text-xs font-medium text-fg">What runs this message</p>
          <p className="text-2xs leading-relaxed text-faint">
            The playbook decides the image and the tools; this decides which model reads them. It
            applies to the messages you send from now on, and is never saved to the playbook.
          </p>
        </div>
        <AgentPicker
          label="This message"
          value={choice}
          onChange={onChange}
          agents={agents}
          disabled={disabled}
          inherit={{ label: "The playbook's model", hint: playbookRuns(playbooks, playbook) }}
          inherited={inherited}
        />
      </PopoverContent>
    </Popover>
  );
}

/** playbookChoice is what the chosen playbook runs on with no override, for the inherit row. */
function playbookChoice(playbooks: Playbook[], playbook: string): AgentChoice {
  const s = playbooks.find((x) => x.name === playbook);
  return s ? { agent: s.agent, model: s.model, effort: s.effort } : INHERIT;
}

/** playbookRuns is the same, as one line of prose for the closed control. */
function playbookRuns(playbooks: Playbook[], playbook: string): string {
  const c = playbookChoice(playbooks, playbook);
  return c.model === "" ? "whatever the playbook says" : c.model;
}

/**
 * PlaybookChip shows which playbook the next message runs and cycles through them on click. It is
 * a cycle rather than a dropdown because there are two or three playbooks, and a menu for three
 * items is a menu nobody opens.
 */
function PlaybookChip({
  playbooks,
  playbook,
  onChange,
  disabled,
}: {
  playbooks: Playbook[];
  playbook: string;
  onChange: (name: string) => void;
  disabled: boolean;
}) {
  if (playbooks.length === 0) return null;
  const at = playbooks.findIndex((s) => s.name === playbook);
  const current = at >= 0 ? playbooks[at] : playbooks[0];
  const next = playbooks[(Math.max(at, 0) + 1) % playbooks.length];

  return (
    <Tooltip label={current.hint ? `${current.hint} · ${current.image}` : current.image}>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        data-testid="chat-playbook"
        disabled={disabled || playbooks.length === 1}
        aria-label={`Playbook: ${current.name}. Click to switch to ${next.name}.`}
        onClick={() => onChange(next.name)}
        className="font-mono"
      >
        /{current.name}
      </Button>
    </Tooltip>
  );
}
