import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { ChatMessageSchema, type ChatMessage } from "../gen/podium/agent/v1/agent_pb";
import { ranAsLabel, toThread, type MessageCustom } from "./chatThread";

let seq = 0n;
function row(over: MessageInitShape<typeof ChatMessageSchema>): ChatMessage {
  seq += 1n;
  return create(ChatMessageSchema, { seq, text: "", ...over });
}

describe("somebody else's turn in a ticket's conversation", () => {
  it("counts the steps the server withheld, says whose turn it was, and keeps the answer", () => {
    const thread = toThread({
      messages: [
        row({ role: "user", text: "@Podium fix the login" }),
        row({ role: "activity", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        row({ role: "progress", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        row({ role: "activity", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        row({ role: "assistant", text: "Fixed in #42.", ranAs: "alice@acme.com", ranAsName: "Alice" }),
      ],
      busy: false,
      taskRunning: false,
    });
    expect(thread).toHaveLength(2);
    const answer = thread[1];
    expect(answer.content).toEqual([{ type: "text", text: "Fixed in #42." }]);
    const custom = answer.metadata?.custom as MessageCustom;
    expect(custom.hiddenSteps).toBe(3);
    expect(custom.ranAs).toBe("Alice");
  });

  it("draws the reader's own turn in full, still saying whose it was", () => {
    const thread = toThread({
      messages: [
        row({ role: "user", text: "go" }),
        row({ role: "progress", text: "Looking", ranAs: "bob@acme.com", ranAsName: "Bob" }),
        row({ role: "assistant", text: "Done.", ranAs: "bob@acme.com", ranAsName: "Bob" }),
      ],
      busy: false,
      taskRunning: false,
    });
    const custom = thread[1].metadata?.custom as MessageCustom;
    expect(custom.hiddenSteps).toBeUndefined();
    expect(custom.ranAs).toBe("Bob");
    expect(thread[1].content).toHaveLength(2);
  });

  it("names a routine's run by its routine, and a web chat's turn by nobody", () => {
    expect(ranAsLabel(create(ChatMessageSchema, { ranAsRoutine: "Nightly triage" }))).toBe("the routine Nightly triage");
    expect(ranAsLabel(create(ChatMessageSchema, { ranAs: "ada@acme.com" }))).toBe("ada@acme.com");
    expect(ranAsLabel(create(ChatMessageSchema, {}))).toBeUndefined();
  });
});
