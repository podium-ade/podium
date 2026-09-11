import { createServer } from "node:net";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { AskHub, newHandler, waitOnSocket } from "./ask.js";

describe("ask tool", () => {
  it("emits a question and returns the inbox reply", async () => {
    const emitted: { type: string; text: string }[] = [];
    const emit = async (argv: string[], text: string) => {
      const typeIdx = argv.indexOf("--type");
      emitted.push({ type: argv[typeIdx + 1] ?? "", text });
    };
    const wait = async () => "use main";
    const handler = newHandler(emit, wait, 1_000);
    const result = (await handler("tools/call", {
      name: "ask",
      arguments: { question: "which branch?" },
    })) as { content: { text: string }[]; isError?: boolean };
    expect(emitted).toEqual([{ type: "question", text: "which branch?" }]);
    expect(result.isError).toBeUndefined();
    expect(result.content[0]?.text).toBe("use main");
  });

  it("returns an error result when nobody answers", async () => {
    const handler = newHandler(
      async () => {},
      async () => {
        throw new Error("nobody answered in 1s; continue without it or ask again");
      },
      1_000,
    );
    const result = (await handler("tools/call", {
      name: "ask",
      arguments: { question: "hello?" },
    })) as { content: { text: string }[]; isError?: boolean };
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("nobody answered");
  });
});

describe("AskHub", () => {
  it("delivers to a waiter and otherwise refuses", async () => {
    const hub = new AskHub();
    expect(hub.tryDeliver("nope")).toBe(false);
    const dir = mkdtempSync(join(tmpdir(), "ask-hub-"));
    const path = join(dir, "ask.sock");
    const stop = hub.listen(path);
    try {
      const pending = waitOnSocket(path)(2_000);
      await new Promise((r) => setTimeout(r, 50));
      expect(hub.tryDeliver("use main")).toBe(true);
      expect(await pending).toBe("use main");
      expect(hub.tryDeliver("again")).toBe(false);
    } finally {
      stop();
    }
  });
});

describe("waitOnSocket", () => {
  it("reads one inbox line", async () => {
    const dir = mkdtempSync(join(tmpdir(), "ask-"));
    const path = join(dir, "inbox.sock");
    const ln = createServer((c) => {
      c.write(JSON.stringify({ v: 1, text: "ship it" }) + "\n");
    });
    await new Promise<void>((resolve, reject) => ln.listen(path, (err?: Error) => (err ? reject(err) : resolve())));
    try {
      const text = await waitOnSocket(path)(2_000);
      expect(text).toBe("ship it");
    } finally {
      ln.close();
    }
  });
});
