/** Activity is the JSON an `activity` row carries (agent/runtime/src/activity.ts). */
export type Activity =
  | {
      kind: "tool";
      id?: string;
      tool: string;
      title?: string;
      status?: string;
      input?: string;
      output?: string;
      error?: string;
    }
  | { kind: "reasoning"; text: string };

export type ToolActivity = Extract<Activity, { kind: "tool" }>;

/**
 * parseActivity reads an activity row, or undefined for one this build does not understand.
 * The text is a task's and untrusted: only the fields named here are read, only as strings.
 */
export function parseActivity(text: string): Activity | undefined {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof v !== "object" || v === null) return undefined;
  const o = v as Record<string, unknown>;
  const str = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : undefined);
  if (o.kind === "reasoning") {
    const t = str("text");
    return t ? { kind: "reasoning", text: t } : undefined;
  }
  if (o.kind === "tool" && str("tool")) {
    return {
      kind: "tool",
      id: str("id"),
      tool: str("tool")!,
      title: str("title"),
      status: str("status"),
      input: str("input"),
      output: str("output"),
      error: str("error"),
    };
  }
  return undefined;
}

/**
 * The input field each harness tool is best summarised by: the command a shell ran, the
 * file an edit touched, the pattern a search looked for. A tool not named here falls back
 * to the harness's own title.
 */
const SUMMARY_FIELD: Record<string, readonly string[]> = {
  bash: ["command"],
  read: ["filePath", "path"],
  edit: ["filePath", "path"],
  write: ["filePath", "path"],
  patch: ["filePath", "path"],
  list: ["path"],
  grep: ["pattern"],
  glob: ["pattern"],
  webfetch: ["url"],
  task: ["description"],
};

/** toolSummary is the one line a tool call is scanned by. */
export function toolSummary(a: ToolActivity): string {
  const input = inputObject(a.input);
  for (const field of SUMMARY_FIELD[a.tool] ?? []) {
    const v = input[field];
    if (typeof v === "string" && v.trim() !== "") return oneLine(v);
  }
  if (a.title) return oneLine(a.title);
  return a.input ? oneLine(a.input) : "";
}

function inputObject(input?: string): Record<string, unknown> {
  if (!input) return {};
  try {
    const v: unknown = JSON.parse(input);
    return typeof v === "object" && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

export interface ToolArgs {
  /** primary is the argument the tool is summarised by — a command, a path — when it has one. */
  primary?: { name: string; value: string };
  /** rest is every other argument, as text. */
  rest: [string, string][];
  /** raw is the input as it came, when it is not a JSON object. */
  raw?: string;
}

/** toolArgs splits a tool call's input into what it was pointed at and everything else. */
export function toolArgs(a: ToolActivity): ToolArgs {
  if (!a.input) return { rest: [] };
  const input = inputObject(a.input);
  if (Object.keys(input).length === 0) return a.input.trim() === "{}" ? { rest: [] } : { rest: [], raw: a.input };
  const primaryName = (SUMMARY_FIELD[a.tool] ?? []).find((f) => typeof input[f] === "string");
  const rest: [string, string][] = [];
  for (const [k, v] of Object.entries(input)) {
    if (k === primaryName) continue;
    rest.push([k, typeof v === "string" ? v : JSON.stringify(v)]);
  }
  return {
    primary: primaryName ? { name: primaryName, value: input[primaryName] as string } : undefined,
    rest,
  };
}

/** oneLine collapses a multi-line value so a row stays a row. */
function oneLine(s: string): string {
  return s.replace(/\s*\n\s*/g, " ⏎ ").trim();
}

/** pretty indents a JSON value and leaves anything else as it came. */
export function pretty(text: string): string {
  if (text === "") return "";
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
