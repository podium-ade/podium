import { haystack, severityRank, SEVERITIES, type Entry, type Severity } from "./entries";

/**
 * entryQuery is the explorer's search box: whitespace-separated terms, all of which must hold.
 *
 *   timeout            the entry's text contains it, ignoring case
 *   "two words"        a quoted phrase
 *   /time.?out/        a regular expression, ignoring case
 *   source:tool        the source starts with it — `source:tool` is every tool, `source:tool:bash` one
 *   kind:exited        the kind is exactly it
 *   severity:error     the severity is exactly it; `severity>=warn` is warn and worse
 *   -term              any of the above, negated
 *
 * A term that does not parse — an unknown severity, a broken regex — is reported, and the
 * rest of the query still applies.
 */

export interface Query {
  test: (e: Entry) => boolean;
  /** error is the first term that did not parse. */
  error?: string;
  empty: boolean;
}

type Pred = (e: Entry) => boolean;

export function tokenize(q: string): string[] {
  const out: string[] = [];
  // A phrase and a regular expression may hold spaces; everything else ends at one.
  const re = /(-?)(?:"([^"]*)"|(\/(?:[^/\\]|\\.)+\/)(?=\s|$)|(\S+))/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(q)) !== null) {
    out.push(m[2] !== undefined ? `${m[1]}"${m[2]}"` : `${m[1]}${m[3] ?? m[4]}`);
  }
  return out;
}

function term(t: string): Pred | string {
  if (t.startsWith('"') && t.endsWith('"') && t.length >= 2) {
    const needle = t.slice(1, -1).toLowerCase();
    return (e) => haystack(e).toLowerCase().includes(needle);
  }
  if (t.length > 2 && t.startsWith("/") && t.endsWith("/")) {
    let re: RegExp;
    try {
      re = new RegExp(t.slice(1, -1), "i");
    } catch {
      return `not a valid regular expression: ${t}`;
    }
    return (e) => re.test(haystack(e));
  }
  const sev = /^severity(>=|:|=)(\w+)$/.exec(t);
  if (sev) {
    const want = sev[2].toLowerCase() as Severity;
    if (!SEVERITIES.includes(want)) return `no severity called ${sev[2]}: use info, warn or error`;
    const rank = severityRank(want);
    return sev[1] === ">=" ? (e) => severityRank(e.severity) >= rank : (e) => e.severity === want;
  }
  const field = /^(source|kind):(.+)$/.exec(t);
  if (field) {
    const v = field[2].toLowerCase();
    return field[1] === "source" ? (e) => e.source.toLowerCase().startsWith(v) : (e) => e.kind === v;
  }
  const needle = t.toLowerCase();
  return (e) => haystack(e).toLowerCase().includes(needle);
}

export function parseQuery(q: string): Query {
  const preds: Pred[] = [];
  let error: string | undefined;
  for (const raw of tokenize(q)) {
    const negate = raw.startsWith("-") && raw.length > 1;
    const p = term(negate ? raw.slice(1) : raw);
    if (typeof p === "string") {
      error ??= p;
      continue;
    }
    preds.push(negate ? (e) => !p(e) : p);
  }
  return { test: (e) => preds.every((p) => p(e)), error, empty: preds.length === 0 };
}
