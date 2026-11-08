// The delegation tools, as the conductor's turn surface sees them.
//
// This is the client half: plain HTTP resources under /v1/delegations, made with fetch — a JSON
// body, a JSON body back, and {code, message} on a refusal (internal/agent/api.NewTurnREST).
//
// The token is a bearer for ONE turn: the conductor minted it for the turn this process is
// running, scoped to that turn's conversation and to the playbooks its brief listed, and it
// is revoked the moment the turn ends. It travels as `Authorization: Bearer` and never
// appears in an argument or an error message.

import { blockedInFront } from "./gitcred.js";

/** Resources is where delegations live, under the turn surface's URL. */
const Resources = "/v1/delegations";

/** callTimeoutMs bounds one call. Delegate creates a task and returns; nothing here waits
 * for a task to finish, so a call that takes this long is a conductor in trouble. */
const callTimeoutMs = 30_000;

/** Delegation is one delegated task, as podium.agent.v1.Delegation. */
export interface Delegation {
  id: string;
  playbook: string;
  instruction: string;
  task_id?: string;
  status: string;
  final_text?: string;
  created_at?: string;
  finished_at?: string;
}

/** StartChatArgs is what start_chat asks the conductor for (podium.agent.v1.StartChatRequest). */
export interface StartChatArgs {
  title: string;
  opening: string;
  share_with?: string[];
  access?: string;
  role?: string;
}

/** FinishRoutineArgs is what routine_finish tells the conductor (podium.agent.v1.FinishRoutineRequest). */
export interface FinishRoutineArgs {
  status: string;
  summary: string;
  stateJson?: string;
  proposeNote?: string;
}

/** CreateRoutineArgs is what create_routine asks for (podium.agent.v1.TurnServiceCreateRoutineRequest). */
export interface CreateRoutineArgs {
  name: string;
  instruction: string;
  schedule: string;
  timezone?: string;
  notes?: string;
}

/** StartedChat is the chat the conductor opened (podium.agent.v1.StartChatResponse). */
export interface StartedChat {
  chatId?: string;
  title?: string;
  url?: string;
  sharedWith?: string[];
  access?: string;
}

/** DelegationState is a delegation plus the last thing its task said. */
export interface DelegationState {
  delegation: Delegation;
  progress?: string;
}

/** DelegateError is a refusal from the conductor, with the Connect code it came with. The
 * message is the conductor's own words, which a model can act on: "that playbook was not
 * offered to this turn" is correctable, "invalid argument" is not. */
export class DelegateError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "DelegateError";
    this.code = code;
  }
}

/** Client is the conductor's turn API. */
export class Client {
  private readonly url: string;
  private readonly token: string;
  private readonly fetchImpl: typeof fetch;

  constructor(url: string, token: string, fetchImpl: typeof fetch = fetch) {
    this.url = url.replace(/\/+$/, "");
    this.token = token;
    this.fetchImpl = fetchImpl;
  }

  delegate(playbook: string, instruction: string, readOnly = false): Promise<{ delegation: Delegation }> {
    return this.call("POST", Resources, readOnly ? { playbook, instruction, read_only: true } : { playbook, instruction });
  }

  get(id: string): Promise<DelegationState> {
    return this.call("GET", `${Resources}/${encodeURIComponent(id)}`);
  }

  list(): Promise<{ delegations?: Delegation[] }> {
    return this.call("GET", Resources);
  }

  cancel(id: string, reason: string): Promise<{ delegation: Delegation }> {
    return this.call("POST", `${Resources}/${encodeURIComponent(id)}/cancel`, { reason });
  }

  inject(id: string, text: string): Promise<{ delegation: Delegation }> {
    return this.call("POST", `${Resources}/${encodeURIComponent(id)}/messages`, { text });
  }

  releasePreview(id: string): Promise<{ delegation: Delegation }> {
    return this.call("DELETE", `${Resources}/${encodeURIComponent(id)}/preview`);
  }

  startChat(args: StartChatArgs): Promise<StartedChat> {
    return this.call("POST", "/v1/chats", args);
  }

  finishRoutine(args: FinishRoutineArgs): Promise<{ message?: string }> {
    return this.call("POST", "/v1/routine-runs/finish", args);
  }

  createRoutine(args: CreateRoutineArgs): Promise<{ message?: string }> {
    return this.call("POST", "/v1/routines", args);
  }

  private async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}` };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
    }
    const res = await this.fetchImpl(`${this.url}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(callTimeoutMs),
    });
    const text = await res.text();
    if (!res.ok) {
      const blocked = blockedInFront(res);
      if (blocked !== undefined) {
        throw new DelegateError(String(res.status), blocked);
      }
      throw new DelegateError(...connectError(res.status, text));
    }
    if (text.trim() === "") {
      return {} as T;
    }
    try {
      return JSON.parse(text) as T;
    } catch (err) {
      throw new DelegateError("internal", `${method} ${path} answered with something that is not JSON: ${err}`);
    }
  }
}

/** connectError reads the conductor's {code, message} error document, falling back to the status line for a
 * body that is not one — which is what a proxy in front of the conductor would return. */
function connectError(status: number, body: string): [string, string] {
  try {
    const parsed = JSON.parse(body) as { code?: string; message?: string };
    if (parsed.code || parsed.message) {
      return [parsed.code ?? String(status), parsed.message ?? `HTTP ${status}`];
    }
  } catch {
    // Not the conductor's error document.
  }
  const trimmed = body.trim();
  return [String(status), trimmed === "" ? `HTTP ${status}` : `HTTP ${status}: ${trimmed}`];
}
