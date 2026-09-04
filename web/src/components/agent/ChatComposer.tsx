import { useRef, useState } from "react";
import type { Skill } from "../../gen/podium/agent/v1/agent_pb";

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
  onSend: (text: string) => void;
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
    onSend(trimmed);
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
          className="min-h-9 flex-1 resize-none rounded border border-border bg-bg px-3 py-2 text-sm text-fg placeholder:text-muted focus-visible:ring-1 focus-visible:ring-accent disabled:opacity-60"
        />
        <SkillChip skills={skills} skill={skill} onChange={onSkillChange} disabled={disabled} />
        <button
          type="button"
          data-testid="chat-send"
          disabled={disabled || text.trim() === ""}
          onClick={send}
          className="rounded border border-accent/40 bg-accent/15 px-3 py-2 text-sm text-fg disabled:opacity-50 focus-visible:ring-1 focus-visible:ring-accent"
        >
          Send
        </button>
      </div>
      <p className="mt-1.5 text-xs text-muted">
        Enter sends · Shift+Enter for a new line. One question at a time: a turn runs to an
        answer and exits.
      </p>
    </div>
  );
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
