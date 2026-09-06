import { useRef, useState } from "react";
import type { AgentBackend, Skill } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { AgentPicker } from "./AgentPicker";

/** MAX_ROWS is how tall the textarea grows before it scrolls. */
const MAX_ROWS = 8;

/** SKILL_PREFIX matches a leading /skill, the same rule the conductor applies. */
const SKILL_PREFIX = /^\/([a-z][a-z0-9-]{0,31})(\s+|$)/;

export interface ChatComposerProps {
  skills: Skill[];
  /** skill is the skill the next message will use. */
  skill: string;
  onSkillChange: (name: string) => void;
  /** disabled is true while a turn runs: turn-based, one in flight per conversation. */
  disabled: boolean;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  /**
   * choice is what the next message runs on, overriding the skill's own. INHERIT — the
   * default — means whatever the skill says, which is what the picker shows.
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
  skills,
  skill,
  onSkillChange,
  disabled,
  agents,
  choice,
  onChoiceChange,
  onSend,
}: ChatComposerProps) {
  const [text, setText] = useState("");
  const box = useRef<HTMLTextAreaElement>(null);

  // Typing /analyst … is the same choice as clicking the chip, so the chip follows the text.
  // It happens on the keystroke rather than in an effect: an effect here would render the
  // composer twice for every character typed.
  const retype = (next: string) => {
    setText(next);
    const m = SKILL_PREFIX.exec(next);
    if (m && m[1] !== skill && skills.some((s) => s.name === m[1])) onSkillChange(m[1]);
  };

  const send = () => {
    const trimmed = text.trim();
    if (trimmed === "" || disabled) return;
    onSend(trimmed, choice);
    setText("");
  };

  const rows = Math.min(MAX_ROWS, Math.max(1, text.split("\n").length));

  return (
    <div className="border-t border-border bg-panel p-3">
      <div className="flex items-end gap-2">
        <textarea
          ref={box}
          data-testid="chat-composer"
          aria-label="Message"
          value={text}
          rows={rows}
          disabled={disabled}
          placeholder={disabled ? "Working…" : "Ask a question about the data"}
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
          className="min-h-9 flex-1 resize-none rounded-md border border-border bg-background px-3 py-2 text-sm text-fg placeholder:text-muted focus-visible:ring-2 focus-visible:ring-ring/60 disabled:opacity-60"
        />
        <SkillChip skills={skills} skill={skill} onChange={onSkillChange} disabled={disabled} />
        <button
          type="button"
          data-testid="chat-send"
          disabled={disabled || text.trim() === ""}
          onClick={send}
          className="rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50 focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          Send
        </button>
      </div>
      {/* The model is chosen per MESSAGE, beside the skill and not inside it. A skill is
          "which job"; the model is "what runs it". Folding the second into the first is what
          makes a profile fill up with skills that differ by one field. */}
      <div className="mt-2">
        <AgentPicker
          label="This message"
          value={choice}
          onChange={onChoiceChange}
          agents={agents}
          disabled={disabled}
          inherit={{ label: "The skill's model", hint: skillRuns(skills, skill) }}
          inherited={skillChoice(skills, skill)}
        />
      </div>
      <p className="mt-1.5 text-xs text-muted">
        Enter sends · Shift+Enter for a new line. One question at a time: a turn runs to an
        answer and exits.
      </p>
    </div>
  );
}

/** skillChoice is what the chosen skill runs on with no override, for the inherit row. */
function skillChoice(skills: Skill[], skill: string): AgentChoice {
  const s = skills.find((x) => x.name === skill);
  return s ? { agent: s.agent, model: s.model, effort: s.effort } : INHERIT;
}

/** skillRuns is the same, as one line of prose for the closed control. */
function skillRuns(skills: Skill[], skill: string): string {
  const c = skillChoice(skills, skill);
  return c.model === "" ? "whatever the skill says" : c.model;
}

/**
 * SkillChip shows which skill the next message runs and cycles through them on click. It is
 * a cycle rather than a dropdown because there are two or three skills, and a menu for three
 * items is a menu nobody opens.
 */
function SkillChip({
  skills,
  skill,
  onChange,
  disabled,
}: {
  skills: Skill[];
  skill: string;
  onChange: (name: string) => void;
  disabled: boolean;
}) {
  if (skills.length === 0) return null;
  const at = skills.findIndex((s) => s.name === skill);
  const current = at >= 0 ? skills[at] : skills[0];
  const next = skills[(Math.max(at, 0) + 1) % skills.length];

  return (
    <button
      type="button"
      data-testid="chat-skill"
      disabled={disabled || skills.length === 1}
      title={current.hint ? `${current.hint}\n${current.image}` : current.image}
      aria-label={`Skill: ${current.name}. Click to switch to ${next.name}.`}
      onClick={() => onChange(next.name)}
      className="rounded border border-border bg-raised px-2 py-2 font-mono text-xs text-muted hover:text-fg disabled:opacity-60 focus-visible:ring-1 focus-visible:ring-accent"
    >
      /{current.name}
    </button>
  );
}
