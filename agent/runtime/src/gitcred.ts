// The GitHub credential, minted on demand.
//
// When the conductor has a GitHub App, a turn is not given a token. It is given a
// CAPABILITY — a string the conductor signed, naming this turn and the repositories its
// playbook listed — and it redeems that capability for a token whenever one is needed.
//
// The redeeming is done by git itself. `credential.helper` is a program git runs every time
// it needs a password, so the script installed here is called once per clone, once per
// fetch and once per push, and each call returns a token minted seconds earlier. That is
// the whole point: an installation token lives one hour, an agent pushes at the END of a
// turn, and this playbook's timeout is two.
//
// The capability never appears in an argv, in .git/config or in the helper script. The
// script holds the NAME of the environment variable it lives in and reads it at the moment
// git asks, exactly as the older static-token helper did.

import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** Service is the conductor's minting service, as Connect addresses it. */
export const Service = "podium.agent.v1.GitCredentialService";

/** TurnTokenHeader must match internal/agent/api.TurnTokenHeader. */
export const TurnTokenHeader = "X-Podium-Turn";

/** HelperName is the script git runs. The extension matters: node needs it to load as ESM. */
export const HelperName = "podium-git-credential.mjs";

/**
 * mintTimeoutMs bounds one mint. It is two GitHub round trips at worst and git is blocked
 * on it, so a hang here is a clone that never starts rather than one that fails.
 */
export const mintTimeoutMs = 20_000;

/**
 * renewBeforeMs is how long before expiry the copy kept for `gh` is replaced. It is about the
 * ONE consumer that cannot ask again on its own, because it reads a plain environment
 * variable; git needs none of it, because its helper mints per ask.
 *
 * It is paired with the conductor's own cache margin — internal/agent/github.renewBefore,
 * fifteen minutes — and MUST stay below it. That cache answers a request for a token it
 * already holds; if its margin were the smaller of the two, the refresh below would be
 * handed back the very token it was trying to replace. Go's
 * TestRenewBeforeExceedsTheRuntimeRefreshMargin is the other half of this pair.
 */
export const renewBeforeMs = 10 * 60 * 1000;

/**
 * Minted is one token and the identity commits made with it should carry.
 *
 * The field names are Connect's JSON mapping of MintTokenResponse — lowerCamelCase, which
 * is protojson's default and what internal/agent/api's TestMintTokenWireShape pins.
 */
export interface Minted {
  token: string;
  username: string;
  expiresAt: string;
  authorName: string;
  authorEmail: string;
}

/** MintError is a refusal from the conductor, with the Connect code it came with. */
export class MintError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "MintError";
    this.code = code;
  }
}

/** mint redeems a capability for a GitHub token. */
export async function mint(
  url: string,
  capability: string,
  fetchImpl: typeof fetch = fetch,
): Promise<Minted> {
  const endpoint = `${url.replace(/\/+$/, "")}/${Service}/MintToken`;
  const res = await fetchImpl(endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/json", [TurnTokenHeader]: capability },
    body: "{}",
    signal: AbortSignal.timeout(mintTimeoutMs),
  });
  const body: unknown = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = body as { code?: string; message?: string };
    throw new MintError(err.code ?? String(res.status), err.message ?? res.statusText);
  }
  const out = body as Partial<Minted>;
  if (out.token === undefined || out.token === "") {
    throw new MintError("internal", "the conductor returned no token");
  }
  return {
    token: out.token,
    username: out.username ?? "x-access-token",
    expiresAt: out.expiresAt ?? "",
    authorName: out.authorName ?? "",
    authorEmail: out.authorEmail ?? "",
  };
}

/**
 * helperScript is the program git runs. It is written out rather than imported because git
 * runs it as its own process, with its own argv, and it must not depend on where this
 * runtime's build put anything.
 *
 * git speaks a line protocol: it writes `key=value` lines on stdin, and for `get` it reads
 * `username=` and `password=` back. Anything else on stdout is an error, so failures go to
 * stderr and exit non-zero — with GIT_TERMINAL_PROMPT=0 that is a clean "authentication
 * failed" rather than a prompt nobody is there to answer.
 */
export function helperScript(url: string, tokenEnv: string): string {
  return `#!/usr/bin/env node
// Written by podium-agent-runtime for one turn. Do not edit: it is rewritten every turn.
//
// git runs this as credential.helper, with the operation as argv[2]. Only "get" does
// anything: there is nothing to store — the token is minted again next time — and nothing
// to erase.
const Url = ${JSON.stringify(url)};
const Env = ${JSON.stringify(tokenEnv)};
const Service = ${JSON.stringify(Service)};
const Header = ${JSON.stringify(TurnTokenHeader)};

async function main() {
  // Drain stdin whatever the operation is: git writes the request there and closing the
  // pipe unread is an EPIPE on its side.
  for await (const _ of process.stdin) {
    // the request describes the URL git wants a credential for; the capability already
    // fixes which repositories this turn may have one for, so there is nothing to read.
  }
  if (process.argv[2] !== "get") {
    return;
  }
  const capability = process.env[Env];
  if (capability === undefined || capability === "") {
    throw new Error(Env + " is not set: this turn was given no capability to mint with");
  }
  const res = await fetch(Url.replace(/\\/+$/, "") + "/" + Service + "/MintToken", {
    method: "POST",
    headers: { "Content-Type": "application/json", [Header]: capability },
    body: "{}",
    signal: AbortSignal.timeout(${mintTimeoutMs}),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error("the conductor refused to mint a token: " + (body.message ?? res.statusText));
  }
  if (!body.token) {
    throw new Error("the conductor returned no token");
  }
  process.stdout.write("username=" + (body.username ?? "x-access-token") + "\\n");
  process.stdout.write("password=" + body.token + "\\n");
}

main().catch((err) => {
  process.stderr.write("podium-git-credential: " + (err && err.message ? err.message : String(err)) + "\\n");
  process.exit(1);
});
`;
}

/**
 * installHelper writes the script and returns the value `credential.helper` should be set
 * to. A leading `!` is git's own syntax for "run this as a command", which is what a script
 * outside git's exec path needs.
 */
export function installHelper(dir: string, url: string, tokenEnv: string): string {
  mkdirSync(dir, { recursive: true });
  const path = join(dir, HelperName);
  writeFileSync(path, helperScript(url, tokenEnv), { mode: 0o700 });
  // Written and then set: writeFileSync's mode is masked by the process umask, and a
  // helper git cannot execute is a clone that fails with nothing useful said.
  chmodSync(path, 0o700);
  return "!" + path;
}

/**
 * expiresInMs is how long a minted token has left, or 0 when it did not say. Absent is
 * treated as expired on purpose: the consumer that asks is the one keeping a copy, and a
 * copy of unknown age is one to replace.
 */
export function expiresInMs(minted: Minted, now = Date.now()): number {
  if (minted.expiresAt === "") {
    return 0;
  }
  const at = Date.parse(minted.expiresAt);
  if (Number.isNaN(at)) {
    return 0;
  }
  return Math.max(0, at - now);
}

/**
 * refreshGhToken keeps GH_TOKEN alive for the `gh` CLI.
 *
 * git needs none of this — its helper mints per ask — but `gh` reads a plain environment
 * variable once per process, and the agent's shell calls spawn a new `gh` throughout a turn
 * that may run for hours. So one token is kept in this process's environment and replaced
 * before it expires; every shell the harness spawns after that inherits the new one.
 *
 * It returns a function that stops the refreshing. The timer is unref'd, so a turn that
 * forgets to call it still exits.
 */
export function refreshGhToken(
  url: string,
  tokenEnv: string,
  first: Minted,
  opts: { fetchImpl?: typeof fetch; onError?: (message: string) => void; now?: () => number } = {},
): () => void {
  const fetchImpl = opts.fetchImpl ?? fetch;
  const now = opts.now ?? Date.now;
  let timer: NodeJS.Timeout | undefined;
  let stopped = false;

  const schedule = (minted: Minted) => {
    if (stopped) {
      return;
    }
    // A token that did not say when it expires, or one already inside the margin, is
    // retried on the margin itself rather than immediately: a hot loop against the
    // conductor is worse than a slightly stale copy.
    const delay = Math.max(renewBeforeMs, expiresInMs(minted, now()) - renewBeforeMs);
    timer = setTimeout(() => {
      void (async () => {
        try {
          const next = await mint(url, process.env[tokenEnv] ?? "", fetchImpl);
          process.env.GH_TOKEN = next.token;
          schedule(next);
        } catch (err) {
          // The turn is not failed over this: git still mints per ask, so only `gh` is
          // affected, and the next attempt may well work.
          opts.onError?.(err instanceof Error ? err.message : String(err));
          schedule(minted);
        }
      })();
    }, delay);
    timer.unref();
  };

  schedule(first);
  return () => {
    stopped = true;
    if (timer !== undefined) {
      clearTimeout(timer);
    }
  };
}
