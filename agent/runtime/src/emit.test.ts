import { describe, expect, it, vi } from "vitest";

import { MaxMessageBytes, emitMessage, messageArgv, splitMessage, type RunnerInvoke } from "./emit.js";

/** recorder is the mock: no child process and no socket is touched in a unit test. */
function recorder(): { calls: { argv: string[]; text: string }[]; invoke: RunnerInvoke } {
  const calls: { argv: string[]; text: string }[] = [];
  return {
    calls,
    invoke: vi.fn(async (argv: string[], text: string) => {
      calls.push({ argv, text });
    }),
  };
}

describe("messageArgv", () => {
  it("builds a progress call", () => {
    expect(messageArgv("progress", [])).toEqual(["message", "--type", "progress", "-"]);
  });

  it("builds a final call with no attachments", () => {
    expect(messageArgv("final", [])).toEqual(["message", "--type", "final", "-"]);
  });

  it("builds a final call with attachments", () => {
    expect(messageArgv("final", ["before.png", "after.png"])).toEqual([
      "message",
      "--type",
      "final",
      "--attach",
      "before.png",
      "--attach",
      "after.png",
      "-",
    ]);
  });

  it("always reads the text from stdin, so it never lands in the process list", () => {
    expect(messageArgv("final", []).at(-1)).toBe("-");
  });
});

describe("emitMessage", () => {
  it("sends one call for a short message", async () => {
    const r = recorder();
    await emitMessage("final", "all done", ["out.txt"], r.invoke);
    expect(r.calls).toHaveLength(1);
    expect(r.calls[0]?.text).toBe("all done");
    expect(r.calls[0]?.argv).toContain("out.txt");
  });

  it("sends nothing for an empty message", async () => {
    const r = recorder();
    await emitMessage("progress", "   \n\n ", [], r.invoke);
    expect(r.calls).toHaveLength(0);
  });

  it("splits a long message and puts the attachments on the last chunk only", async () => {
    const paragraph = `${"a".repeat(20_000)}\n\n`;
    const r = recorder();
    await emitMessage("final", paragraph.repeat(3), ["shot.png"], r.invoke);

    expect(r.calls.length).toBeGreaterThanOrEqual(2);
    for (const call of r.calls) {
      expect(Buffer.byteLength(call.text, "utf8")).toBeLessThanOrEqual(MaxMessageBytes);
    }
    for (const call of r.calls.slice(0, -1)) {
      expect(call.argv).not.toContain("--attach");
    }
    expect(r.calls.at(-1)?.argv).toEqual([
      "message",
      "--type",
      "final",
      "--attach",
      "shot.png",
      "-",
    ]);
  });

  it("stops at the first failed call rather than pretending it delivered", async () => {
    const invoke = vi.fn<RunnerInvoke>(async () => {
      throw new Error("no node is listening");
    });
    await expect(emitMessage("final", "x", [], invoke)).rejects.toThrow("no node is listening");
    expect(invoke).toHaveBeenCalledTimes(1);
  });
});

describe("splitMessage", () => {
  it("leaves a short message alone", () => {
    expect(splitMessage("hello")).toEqual(["hello"]);
  });

  it("trims trailing whitespace, which the runner would trim anyway", () => {
    expect(splitMessage("hello\n\n")).toEqual(["hello"]);
  });

  it("splits on a paragraph boundary when it can", () => {
    const a = "a".repeat(20_000);
    const b = "b".repeat(20_000);
    expect(splitMessage(`${a}\n\n${b}`)).toEqual([a, b]);
  });

  it("falls back to line, then word, then codepoint boundaries", () => {
    const line = "l".repeat(20_000);
    expect(splitMessage(`${line}\n${line}`)).toEqual([line, line]);

    const word = "w".repeat(20_000);
    expect(splitMessage(`${word} ${word}`)).toEqual([word, word]);

    const solid = "s".repeat(MaxMessageBytes + 10);
    const chunks = splitMessage(solid);
    expect(chunks).toHaveLength(2);
    expect(chunks.join("")).toBe(solid);
  });

  it("never cuts a multi-byte character in half", () => {
    const chunks = splitMessage("é".repeat(MaxMessageBytes));
    expect(chunks.length).toBeGreaterThan(1);
    for (const chunk of chunks) {
      expect(Buffer.byteLength(chunk, "utf8")).toBeLessThanOrEqual(MaxMessageBytes);
    }
    expect(chunks.join("")).toBe("é".repeat(MaxMessageBytes));
  });

  it("counts bytes, not characters", () => {
    // 16385 two-byte characters is 32770 bytes: one byte over the cap.
    const text = "é".repeat(MaxMessageBytes / 2 + 1);
    expect(text.length).toBeLessThan(MaxMessageBytes);
    expect(splitMessage(text).length).toBeGreaterThan(1);
  });
});
