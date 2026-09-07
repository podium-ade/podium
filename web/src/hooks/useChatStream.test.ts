import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ChatFrameSchema,
  ChatMessageSchema,
  type ChatFrame,
} from "../gen/podium/agent/v1/agent_pb";
import { useChatStream } from "./useChatStream";

const streamChat = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: { streamChat: (...a: unknown[]) => streamChat(...a) },
  };
});

/** feed is a stream a test pushes frames into; it ends only when told to. */
function feed(frames: ChatFrame[] = []) {
  const queued = frames.slice();
  let wake: (() => void) | undefined;
  let ended: Error | "clean" | undefined;
  return {
    push(f: ChatFrame) {
      queued.push(f);
      wake?.();
    },
    end(err?: Error) {
      ended = err ?? "clean";
      wake?.();
    },
    async *[Symbol.asyncIterator]() {
      for (;;) {
        while (queued.length > 0) yield queued.shift() as ChatFrame;
        if (ended === "clean") return;
        if (ended instanceof Error) throw ended;
        await new Promise<void>((resolve) => {
          wake = resolve;
        });
      }
    },
  };
}

function message(seq: number, role = "user", text = "hello") {
  return create(ChatFrameSchema, {
    frame: {
      case: "message",
      value: create(ChatMessageSchema, {
        chatId: "chat_01abc",
        seq: BigInt(seq),
        role,
        text,
        ts: timestampFromDate(new Date()),
      }),
    },
  });
}

const resync = create(ChatFrameSchema, { frame: { case: "resync", value: true } });

/** fromSeqs is the from_seq of every streamChat call so far. */
function fromSeqs(): bigint[] {
  return streamChat.mock.calls.map((c) => (c[0] as { fromSeq: bigint }).fromSeq);
}

describe("useChatStream", () => {
  beforeEach(() => {
    streamChat.mockReset();
  });

  it("does not connect without a chat", () => {
    renderHook(() => useChatStream(""));
    expect(streamChat).not.toHaveBeenCalled();
  });

  it("collects messages in seq order", async () => {
    const stream = feed();
    streamChat.mockImplementation(() => stream);
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    stream.push(message(2, "assistant", "second"));
    stream.push(message(1, "user", "first"));
    await waitFor(() => expect(result.current.messages).toHaveLength(2));
    expect(result.current.messages.map((m) => m.text)).toEqual(["first", "second"]);
    expect(result.current.phase).toBe("streaming");
  });

  it("replaces a message that arrives twice on the same seq", async () => {
    const stream = feed();
    streamChat.mockImplementation(() => stream);
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    stream.push(message(1, "assistant", "See report.csv."));
    await waitFor(() => expect(result.current.messages).toHaveLength(1));
    stream.push(message(1, "assistant", "See report.csv. (attached)"));
    await waitFor(() =>
      expect(result.current.messages[0].text).toBe("See report.csv. (attached)"),
    );
    expect(result.current.messages).toHaveLength(1);
  });

  it("reconnects from the last seq it saw", async () => {
    const stream = feed();
    streamChat.mockImplementation(() => stream);
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    stream.push(message(1));
    stream.push(message(7));
    await waitFor(() => expect(result.current.messages).toHaveLength(2));

    // A dropped connection is not the end of the conversation.
    const next = feed();
    streamChat.mockImplementation(() => next);
    stream.end(new ConnectError("connection reset", Code.Unavailable));

    await waitFor(() => expect(result.current.phase).toBe("error"));
    expect(result.current.error).toContain("connection reset");
    // from_seq is exclusive, so resuming at the highest seq seen is no gap and no repeat.
    await waitFor(() => expect(fromSeqs()).toEqual([0n, 7n]), { timeout: 5000 });

    next.push(message(8));
    await waitFor(() => expect(result.current.messages).toHaveLength(3));
    expect(result.current.phase).toBe("streaming");
    expect(result.current.error).toBeUndefined();
  });

  it("reconnects immediately on a resync frame", async () => {
    const first = feed();
    const second = feed();
    let call = 0;
    streamChat.mockImplementation(() => (call++ === 0 ? first : second));
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    first.push(message(3));
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    // The server dropped something durable because this browser fell behind. The whole
    // remedy is to re-read from the last seq seen, which is what the replay delivers.
    first.push(resync);
    await waitFor(() => expect(fromSeqs()).toEqual([0n, 3n]));
    // A resync is not an error: nothing on screen should say the stream broke.
    expect(result.current.error).toBeUndefined();
  });

  it("tracks a turn's status and drops progress when it ends", async () => {
    const stream = feed();
    streamChat.mockImplementation(() => stream);
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    stream.push(
      create(ChatFrameSchema, {
        frame: { case: "status", value: { state: "started", taskId: "task_01xyz" } },
      }),
    );
    await waitFor(() => expect(result.current.running).toBe(true));
    expect(result.current.taskId).toBe("task_01xyz");

    stream.push(create(ChatFrameSchema, { frame: { case: "progress", value: "reading" } }));
    await waitFor(() => expect(result.current.progress).toBe("reading"));

    stream.push(
      create(ChatFrameSchema, { frame: { case: "status", value: { state: "finished" } } }),
    );
    await waitFor(() => expect(result.current.running).toBe(false));
    expect(result.current.progress).toBeUndefined();
    expect(result.current.taskId).toBeUndefined();
  });

  it("drops progress as soon as the answer lands", async () => {
    const stream = feed();
    streamChat.mockImplementation(() => stream);
    const { result } = renderHook(() => useChatStream("chat_01abc"));

    stream.push(create(ChatFrameSchema, { frame: { case: "progress", value: "thinking" } }));
    await waitFor(() => expect(result.current.progress).toBe("thinking"));
    stream.push(message(1, "assistant", "the answer"));
    await waitFor(() => expect(result.current.progress).toBeUndefined());
  });

  it("shows nothing from the previous chat while the next one connects", async () => {
    const first = feed();
    const second = feed();
    let call = 0;
    streamChat.mockImplementation(() => (call++ === 0 ? first : second));
    const { result, rerender } = renderHook(({ id }) => useChatStream(id), {
      initialProps: { id: "chat_01abc" },
    });
    first.push(message(1, "user", "in the first chat"));
    await waitFor(() => expect(result.current.messages).toHaveLength(1));

    rerender({ id: "chat_02def" });
    expect(result.current.messages).toHaveLength(0);
    expect(result.current.phase).toBe("connecting");
    await waitFor(() => expect(fromSeqs()).toEqual([0n, 0n]));
  });

  it("does not reconnect when the chat is not there", async () => {
    streamChat.mockImplementation(() => {
      throw new ConnectError("agent store: not found: chat chat_01abc", Code.NotFound);
    });
    const { result } = renderHook(() => useChatStream("chat_01abc"));
    await waitFor(() => expect(result.current.gone).toBe(true));
    expect(result.current.error).toContain("not found");
    await new Promise((r) => setTimeout(r, 800));
    expect(streamChat).toHaveBeenCalledTimes(1);
  });

  it("cancels the stream on unmount and on a chat switch", async () => {
    const signals: AbortSignal[] = [];
    streamChat.mockImplementation((_req: unknown, opts: { signal: AbortSignal }) => {
      signals.push(opts.signal);
      return feed();
    });
    const { rerender, unmount } = renderHook(({ id }) => useChatStream(id), {
      initialProps: { id: "chat_01abc" },
    });
    await waitFor(() => expect(signals).toHaveLength(1));

    rerender({ id: "chat_02def" });
    await waitFor(() => expect(signals[0].aborted).toBe(true));
    await waitFor(() => expect(signals).toHaveLength(2));

    unmount();
    await waitFor(() => expect(signals[1].aborted).toBe(true));
  });
});
