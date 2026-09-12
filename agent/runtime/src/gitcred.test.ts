import { execFile, execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it, vi } from "vitest";

import {
  HelperName,
  MintError,
  Service,
  TurnTokenHeader,
  expiresInMs,
  helperScript,
  installHelper,
  mint,
  refreshGhToken,
  renewBeforeMs,
  type Minted,
} from "./gitcred.js";

const minted: Minted = {
  token: "ghs_minted",
  username: "x-access-token",
  expiresAt: "2026-09-09T13:00:00Z",
  authorName: "podium-agent[bot]",
  authorEmail: "987654+podium-agent[bot]@users.noreply.github.com",
};

/** ok is a fetch that answers one mint. */
function ok(body: Partial<Minted> = minted) {
  return vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
}

describe("mint", () => {
  it("posts to the conductor's minting method with the capability in a header", async () => {
    const fetchImpl = ok();
    const got = await mint("http://conductor:8090/", "turn_01.scope.mac", fetchImpl);

    expect(got).toEqual(minted);
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`http://conductor:8090/${Service}/MintToken`);
    expect(init.method).toBe("POST");
    // A credential in a request body is a credential in every log that prints one.
    expect(init.body).toBe("{}");
    expect((init.headers as Record<string, string>)[TurnTokenHeader]).toBe("turn_01.scope.mac");
  });

  it("carries the conductor's refusal back with its code", async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(JSON.stringify({ code: "permission_denied", message: "not valid for a running turn" }), {
          status: 403,
        }),
    );
    await expect(mint("http://conductor:8090", "forged", fetchImpl)).rejects.toThrow(MintError);
    await expect(mint("http://conductor:8090", "forged", fetchImpl)).rejects.toThrow("not valid");
  });

  it("refuses a success that carries no token rather than returning an empty one", async () => {
    const fetchImpl = vi.fn(async () => new Response("{}", { status: 200 }));
    await expect(mint("http://conductor:8090", "turn_01.scope.mac", fetchImpl)).rejects.toThrow(MintError);
  });
});

describe("the credential helper git runs", () => {
  it("is executable and named so node loads it as ESM", () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-gitcred-"));
    const value = installHelper(dir, "http://conductor:8090", "PODIUM_GIT_CAPABILITY");

    // A leading ! is git's own syntax for "run this as a command".
    expect(value).toBe(`!${join(dir, HelperName)}`);
    expect(HelperName.endsWith(".mjs")).toBe(true);
    expect(statSync(join(dir, HelperName)).mode & 0o100).toBe(0o100);
  });

  // The capability is a credential. The script may name the variable and must never hold
  // the value, exactly as the older static-token helper did not.
  it("holds the name of the capability variable and never a capability", () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-gitcred-"));
    installHelper(dir, "http://conductor:8090", "PODIUM_GIT_CAPABILITY");
    const script = readFileSync(join(dir, HelperName), "utf8");

    expect(script).toContain("PODIUM_GIT_CAPABILITY");
    expect(script).toContain("process.env[Env]");
    expect(script).not.toContain("turn_01");
  });

  it("is valid JavaScript", () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-gitcred-"));
    installHelper(dir, "http://conductor:8090", "PODIUM_GIT_CAPABILITY");
    // --check parses without running: the script talks to a conductor, and this asserts
    // only that a syntax error in the generated source cannot ship.
    expect(() => execFileSync(process.execPath, ["--check", join(dir, HelperName)])).not.toThrow();
  });

  // What git actually does: run the helper with `get`, write the request on its stdin, and
  // read username= and password= back. This drives the real script against a real server.
  it("answers git's get with the minted credential", async () => {
    const { createServer } = await import("node:http");
    const server = createServer((req, res) => {
      let seen = "";
      req.on("data", () => {});
      req.on("end", () => {
        seen = req.headers[TurnTokenHeader.toLowerCase()] as string;
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify(seen === "turn_01.scope.mac" ? minted : { message: "no" }));
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = (server.address() as { port: number }).port;

    try {
      const dir = mkdtempSync(join(tmpdir(), "podium-gitcred-"));
      installHelper(dir, `http://127.0.0.1:${port}`, "PODIUM_GIT_CAPABILITY");
      // Asynchronously, and this is not a detail: the fake conductor above listens on this
      // process's event loop, so a synchronous execFileSync here would block the very loop
      // that has to answer the helper's request.
      const out = await new Promise<string>((resolve, reject) => {
        const child = execFile(
          join(dir, HelperName),
          ["get"],
          { env: { ...process.env, PODIUM_GIT_CAPABILITY: "turn_01.scope.mac" }, encoding: "utf8" },
          (err, stdout) => (err ? reject(err) : resolve(stdout)),
        );
        child.stdin?.end("protocol=https\nhost=github.com\n\n");
      });

      expect(out).toBe("username=x-access-token\npassword=ghs_minted\n");
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it("fails loudly rather than printing a broken credential", async () => {
    const dir = mkdtempSync(join(tmpdir(), "podium-gitcred-"));
    installHelper(dir, "http://127.0.0.1:1", "PODIUM_GIT_CAPABILITY");

    const failed = await new Promise<boolean>((resolve) => {
      const child = execFile(
        join(dir, HelperName),
        ["get"],
        { env: { ...process.env, PODIUM_GIT_CAPABILITY: "" }, encoding: "utf8" },
        (err) => resolve(err !== null),
      );
      child.stdin?.end("protocol=https\nhost=github.com\n\n");
    });
    expect(failed).toBe(true);
  });

  it("does nothing for store and erase: there is no token to keep", () => {
    const script = helperScript("http://conductor:8090", "PODIUM_GIT_CAPABILITY");
    expect(script).toContain('process.argv[2] !== "get"');
  });
});

describe("expiresInMs", () => {
  it("is what is left on the clock", () => {
    const now = Date.parse("2026-09-09T12:30:00Z");
    expect(expiresInMs(minted, now)).toBe(30 * 60 * 1000);
  });

  it("treats a token that did not say, or one already dead, as expired", () => {
    expect(expiresInMs({ ...minted, expiresAt: "" })).toBe(0);
    expect(expiresInMs({ ...minted, expiresAt: "not a date" })).toBe(0);
    expect(expiresInMs(minted, Date.parse("2026-09-09T14:00:00Z"))).toBe(0);
  });
});

describe("refreshGhToken", () => {
  it("replaces GH_TOKEN before the token it holds expires", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(Date.parse("2026-09-09T12:00:00Z"));
      const fetchImpl = ok({ ...minted, token: "ghs_second", expiresAt: "2026-09-09T14:00:00Z" });
      const stop = refreshGhToken("http://conductor:8090", "PODIUM_GIT_CAPABILITY", minted, {
        fetchImpl,
      });

      // One hour to expiry, ten minutes of margin: nothing yet at forty-nine minutes.
      await vi.advanceTimersByTimeAsync(49 * 60 * 1000);
      expect(fetchImpl).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(2 * 60 * 1000);
      expect(fetchImpl).toHaveBeenCalledOnce();
      expect(process.env.GH_TOKEN).toBe("ghs_second");
      stop();
    } finally {
      vi.useRealTimers();
      delete process.env.GH_TOKEN;
    }
  });

  // git still mints per ask, so a failed refresh costs `gh` and nothing else. It must not
  // become a hot loop against the conductor either.
  it("keeps the old token and tries again when a refresh fails", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(Date.parse("2026-09-09T12:00:00Z"));
      process.env.GH_TOKEN = "ghs_first";
      const errors: string[] = [];
      const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ message: "down" }), { status: 503 }));
      const stop = refreshGhToken("http://conductor:8090", "PODIUM_GIT_CAPABILITY", minted, {
        fetchImpl,
        onError: (m) => errors.push(m),
      });

      await vi.advanceTimersByTimeAsync(51 * 60 * 1000);
      expect(errors).toHaveLength(1);
      // A failed refresh costs `gh` nothing it already had.
      expect(process.env.GH_TOKEN).toBe("ghs_first");

      // The token it still holds now has nine minutes left, so the next attempt is the
      // margin away rather than another fifty minutes: no hot loop, no silent giving up.
      await vi.advanceTimersByTimeAsync(renewBeforeMs + 1000);
      expect(fetchImpl).toHaveBeenCalledTimes(2);
      stop();
    } finally {
      vi.useRealTimers();
      delete process.env.GH_TOKEN;
    }
  });

  it("stops when it is told to", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(Date.parse("2026-09-09T12:00:00Z"));
      const fetchImpl = ok();
      const stop = refreshGhToken("http://conductor:8090", "PODIUM_GIT_CAPABILITY", minted, { fetchImpl });
      stop();

      await vi.advanceTimersByTimeAsync(2 * 60 * 60 * 1000);
      expect(fetchImpl).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

// The pair with internal/agent/github.renewBefore. That cache will answer with a token it
// already holds while it has more than ITS margin left, so this one has to be smaller —
// otherwise the refresh above is handed back the token it was replacing.
describe("the refresh margin", () => {
  it("stays below the conductor's cache margin", () => {
    const conductorRenewBeforeMs = 15 * 60 * 1000; // internal/agent/github.renewBefore
    expect(renewBeforeMs).toBeLessThan(conductorRenewBeforeMs);
  });
});
