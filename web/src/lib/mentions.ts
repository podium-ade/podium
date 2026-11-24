/**
 * Mentions in the web chat (multiplayer sessions, docs/agent.md).
 *
 * The composer's chips insert exact tokens and the conductor matches exactly those
 * (internal/agent/chat/mention.go), so what the send button promises — "Send to Podium" —
 * and what the server does cannot drift apart. Keep the two in step.
 *
 * - `@<deployment name>` puts the message to the bot, and is what the composer offers. The
 *   deployment is "Podium" until an admin renames it (lib/brand.ts); `@Podium` keeps working
 *   after a rename, because the server still accepts the house name.
 * - `@<assistant name>` does the same in a chat bound to a personal assistant.
 * - `@[Name](mention:<login>)` names a Podium person. It is a Markdown link, so the thread
 *   renders it as one.
 */

import type { Member } from "../gen/podium/agent/v1/agent_pb";
import { HOUSE_NAME } from "./brand";

const PERSON = /@\[([^\]\n]{1,200})\]\(mention:([^)\s]{1,320})\)/g;

/**
 * plainMentions is text with each person chip shown as `@Name`, for the places that draw a
 * message as plain text — a chat's preview in the list, a queued message — where the Markdown
 * link would otherwise show as typed.
 */
export function plainMentions(text: string): string {
  return text.replace(PERSON, (_m, name: string) => `@${name}`);
}

/** isWordChar is what may not touch a mention: a letter, a digit, `_`, `-` or `@`. */
function isWordChar(ch: string | undefined): boolean {
  if (ch === undefined || ch === "") return false;
  return /[\p{L}\p{N}_\-@]/u.test(ch);
}

function hasMention(text: string, name: string): boolean {
  if (name === "") return false;
  const lower = text.toLowerCase();
  const want = `@${name.toLowerCase()}`;
  let from = 0;
  for (;;) {
    const at = lower.indexOf(want, from);
    if (at < 0) return false;
    const before = at === 0 ? undefined : [...lower.slice(0, at)].pop();
    const after = [...lower.slice(at + want.length)][0];
    if (!isWordChar(before) && !isWordChar(after)) return true;
    from = at + 1;
  }
}

/**
 * mentionsPodium reports whether text puts the message to the bot: by the deployment's name,
 * by the house name, or by the chat's assistant.
 */
export function mentionsPodium(text: string, personality = "", brand = HOUSE_NAME): boolean {
  const plain = text.replace(PERSON, " ");
  if (hasMention(plain, HOUSE_NAME) || hasMention(plain, brand)) return true;
  const p = personality.trim();
  return p !== "" && hasMention(plain, p);
}

/** personToken is what the person chip inserts. Brackets in a name would end the link early. */
export function personToken(name: string, login: string): string {
  const safe = (name.trim() || login).replace(/[[\]\n]/g, "");
  return `@[${safe}](mention:${login})`;
}

/**
 * mentionQuery is the `@word` the caret is at the end of, or undefined. It is what the
 * suggestion list filters on, and `start` is where the token being typed begins.
 */
export function mentionQuery(
  text: string,
  caret: number,
): { query: string; start: number } | undefined {
  const head = text.slice(0, caret);
  const m = /(^|[\s(])@([\p{L}\p{N}_.\- ]{0,40})$/u.exec(head);
  if (!m) return undefined;
  const query = m[2];
  // A space ends a mention unless it is still being typed as part of a name ("@Research B").
  if (query.endsWith("  ")) return undefined;
  return { query, start: head.length - query.length - 1 };
}

/** insertMention replaces the `@query` at start..caret with token and a space. */
export function insertMention(
  text: string,
  start: number,
  caret: number,
  token: string,
): { text: string; caret: number } {
  const next = `${text.slice(0, start)}${token} ${text.slice(caret)}`;
  return { text: next, caret: start + token.length + 1 };
}

export interface Suggestion {
  key: string;
  label: string;
  hint?: string;
  token: string;
}

/**
 * mentionSuggestions is what the @-list offers for query: the deployment's name, the chat's assistant,
 * and people. The two bot names are matched here; people were already matched by the
 * server's search.
 */
export function mentionSuggestions(
  query: string | undefined,
  personality: string,
  members: readonly Member[],
  brand = HOUSE_NAME,
): Suggestion[] {
  if (query === undefined) return [];
  const q = query.trim().toLowerCase();
  const out: Suggestion[] = [];
  const bots = [...new Set([brand, personality.trim()])].filter((n) => n !== "");
  for (const name of bots) {
    if (q === "" || name.toLowerCase().startsWith(q)) {
      out.push({ key: `bot:${name}`, label: name, hint: "starts a turn", token: `@${name}` });
    }
  }
  for (const m of members) {
    out.push({
      key: `person:${m.login}`,
      label: m.name || m.login,
      hint: m.name ? m.login : undefined,
      token: personToken(m.name, m.login),
    });
  }
  return out;
}
