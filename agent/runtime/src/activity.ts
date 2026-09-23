// Activity: what a turn is doing between the things it says. Every finished tool call and
// every reasoning block the harness reports becomes one `activity` message, whose text is a
// small JSON document for a reader that draws it — the web chat — and not for a human.
//
// Everything in it is capped. The runner refuses a message over 32 KiB, a tool's output can
// be megabytes, and the reader only ever shows the head of it; the transcript artifact keeps
// every line whole for anyone who needs the rest.

import type { Event } from "./opencode.js";

/** MaxFieldChars caps a tool's input and output, each. */
export const MaxFieldChars = 4_000;

/** MaxReasoningChars caps one reasoning block. */
export const MaxReasoningChars = 16_000;

export type Activity =
  | {
      kind: "tool";
      id: string;
      tool: string;
      title?: string;
      status: "completed" | "error";
      input?: string;
      output?: string;
      error?: string;
    }
  | { kind: "reasoning"; text: string };

/**
 * activityOf reads one harness event, or undefined when it is not activity. `clean` is
 * applied to every string that leaves, so a token the turn holds never reaches the chat.
 */
export function activityOf(event: Event, clean: (s: string) => string = (s) => s): Activity | undefined {
  const part = event.part;
  if (event.type === "reasoning") {
    const text = (part?.text ?? "").trim();
    return text === "" ? undefined : { kind: "reasoning", text: cap(clean(text), MaxReasoningChars) };
  }
  if (event.type !== "tool_use" || !part?.tool) {
    return undefined;
  }
  const state = part.state ?? {};
  const failed = state.status === "error";
  const out: Activity = {
    kind: "tool",
    id: part.callID ?? part.id ?? "",
    tool: part.tool,
    status: failed ? "error" : "completed",
  };
  if (state.title) out.title = cap(clean(state.title), 200);
  if (state.input !== undefined) out.input = cap(clean(stringify(state.input)), MaxFieldChars);
  if (!failed && state.output !== undefined) out.output = cap(clean(stringify(state.output)), MaxFieldChars);
  if (failed && state.error !== undefined) out.error = cap(clean(stringify(state.error)), MaxFieldChars);
  return out;
}

function stringify(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v);
}

function cap(s: string, max: number): string {
  return s.length <= max ? s : `${s.slice(0, max)}…`;
}
