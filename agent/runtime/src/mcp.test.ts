import { PassThrough } from "node:stream";

import { describe, expect, it, vi } from "vitest";

import { Client, DelegateError } from "./delegate.js";
import { ProtocolVersion, ServerName, newHandler, serve, tools, urlFrom } from "./mcp.js";

/** stubClient is the conductor, scripted. Nothing in these tests speaks HTTP. */
function stubClient(overrides: Partial<Record<keyof Client, unknown>> = {}): Client {
  const base = {
    delegate: vi.fn(async (playbook: string) => ({
      delegation: { id: "dlg_01", playbook, instruction: "do it", task_id: "task_01", status: "running" },
    })),
    get: vi.fn(async (id: string) => ({
      delegation: { id, playbook: "podium", instruction: "do it", task_id: "task_01", status: "running" },
      progress: "reading the handler",
    })),
    list: vi.fn(async () => ({ delegations: [] })),
    inject: vi.fn(async (id: string) => ({
      delegation: { id, playbook: "podium", instruction: "do it", task_id: "task_01", status: "running" },
    })),
    cancel: vi.fn(async (id: string) => ({
      delegation: { id, playbook: "podium", instruction: "do it", task_id: "task_01", status: "running" },
    })),
    releasePreview: vi.fn(async (id: string) => ({
      delegation: { id, playbook: "podium", instruction: "do it", task_id: "task_01", status: "succeeded" },
    })),
    startChat: vi.fn(async (args: { title: string; share_with?: string[] }) => ({
      chatId: "chat_new",
      title: args.title,
      url: "https://podium.example/agent/chat/chat_new",
      sharedWith: args.share_with ?? [],
      access: "restricted",
    })),
    finishRoutine: vi.fn(async () => ({ message: "Recorded as idle." })),
    createRoutine: vi.fn(async () => ({ message: 'Created the routine "Digest".' })),
  };
  return { ...base, ...overrides } as unknown as Client;
}

/** text is the text of a tool result, which is the only shape these tools return. */
function text(result: unknown): string {
  return ((result as { content: { text: string }[] }).content ?? []).map((c) => c.text).join("\n");
}

function isError(result: unknown): boolean {
  return (result as { isError?: boolean }).isError === true;
}

describe("initialize", () => {
  it("echoes a protocol version it speaks, because that is what a server is asked to do", async () => {
    const h = newHandler(stubClient());
    const res = (await h("initialize", { protocolVersion: "2024-11-05" })) as Record<string, any>;
    expect(res.protocolVersion).toBe("2024-11-05");
    expect(res.capabilities).toEqual({ tools: {} });
    expect(res.serverInfo.name).toBe(ServerName);
  });

  it("echoes the version opencode 1.19.5 actually asks for", async () => {
    // Traced against the real client: it sends 2025-11-25 on initialize.
    const h = newHandler(stubClient());
    const res = (await h("initialize", { protocolVersion: "2025-11-25" })) as Record<string, any>;
    expect(res.protocolVersion).toBe("2025-11-25");
  });

  it("answers with its own version for one it does not know, and lets the client decide", async () => {
    const h = newHandler(stubClient());
    const res = (await h("initialize", { protocolVersion: "1999-01-01" })) as Record<string, any>;
    expect(res.protocolVersion).toBe(ProtocolVersion);
  });

  it("does not require a version at all", async () => {
    const h = newHandler(stubClient());
    const res = (await h("initialize", {})) as Record<string, any>;
    expect(res.protocolVersion).toBe(ProtocolVersion);
  });
});

describe("tools/list", () => {
  it("offers exactly the delegation tools, each with a schema", async () => {
    const h = newHandler(stubClient());
    const res = (await h("tools/list", {})) as { tools: typeof tools };
    expect(res.tools.map((t) => t.name)).toEqual([
      "delegate",
      "check_delegation",
      "list_delegations",
      "inject_delegation",
      "cancel_delegation",
      "start_chat",
      "release_preview",
    ]);
    for (const tool of res.tools) {
      expect(tool.description.length).toBeGreaterThan(40);
      expect(tool.inputSchema.type).toBe("object");
      // additionalProperties: false, so a model that invents an argument is told, rather
      // than having it silently dropped.
      expect(tool.inputSchema.additionalProperties).toBe(false);
    }
  });

  it("tells the model the answer arrives in the conversation on its own", async () => {
    // The one thing a delegating agent gets wrong is repeating the task's answer as its
    // own. If this sentence goes, that behaviour comes back.
    const delegate = tools.find((t) => t.name === "delegate");
    expect(delegate?.description).toContain("appear in this conversation");
  });
});

describe("tools/call delegate", () => {
  it("passes the playbook and the instruction through and reports the id", async () => {
    const client = stubClient();
    const h = newHandler(client);
    const res = await h("tools/call", {
      name: "delegate",
      arguments: { playbook: "podium", instruction: "fix the alignment" },
    });
    expect(client.delegate).toHaveBeenCalledWith("podium", "fix the alignment", false);
    expect(text(res)).toContain("dlg_01");
    expect(text(res)).toContain("task_01");
    expect(isError(res)).toBe(false);
  });

  it("asks for a read-only task when the model says so", async () => {
    const client = stubClient();
    const h = newHandler(client);
    await h("tools/call", {
      name: "delegate",
      arguments: { playbook: "podium", instruction: "why is it racy?", read_only: true },
    });
    expect(client.delegate).toHaveBeenCalledWith("podium", "why is it racy?", true);
  });

  it("refuses a call with no instruction rather than starting an empty task", async () => {
    const h = newHandler(stubClient());
    await expect(h("tools/call", { name: "delegate", arguments: { playbook: "podium" } })).rejects.toThrow(
      /instruction is required/,
    );
  });

  it("hands the conductor's own refusal back as a failed result, so the model can correct itself", async () => {
    const client = stubClient({
      delegate: vi.fn(async () => {
        throw new DelegateError("invalid_argument", 'that playbook was not offered to this turn: "nope"');
      }),
    });
    const res = await h_call(client, { name: "delegate", arguments: { playbook: "nope", instruction: "x" } });
    expect(isError(res)).toBe(true);
    expect(text(res)).toContain("was not offered to this turn");
    expect(text(res)).toContain("invalid_argument");
  });
});

describe("tools/call check_delegation", () => {
  it("reports status and the last thing the task said while it is running", async () => {
    const res = await h_call(stubClient(), { name: "check_delegation", arguments: { delegation_id: "dlg_01" } });
    expect(text(res)).toContain("running");
    expect(text(res)).toContain("reading the handler");
  });

  it("reports the answer once there is one, and drops the stale progress", async () => {
    const client = stubClient({
      get: vi.fn(async (id: string) => ({
        delegation: {
          id,
          playbook: "podium",
          instruction: "do it",
          task_id: "task_01",
          status: "succeeded",
          final_text: "opened #51",
        },
        progress: "still working",
      })),
    });
    const res = await h_call(client, { name: "check_delegation", arguments: { delegation_id: "dlg_01" } });
    expect(text(res)).toContain("succeeded");
    expect(text(res)).toContain("opened #51");
    expect(text(res)).not.toContain("still working");
  });
});

describe("tools/call list_delegations", () => {
  it("says so plainly when there are none", async () => {
    const res = await h_call(stubClient(), { name: "list_delegations", arguments: {} });
    expect(text(res)).toContain("No delegated tasks in this conversation");
  });

  it("lists each one with its status", async () => {
    const client = stubClient({
      list: vi.fn(async () => ({
        delegations: [
          { id: "dlg_01", playbook: "podium", instruction: "a", status: "succeeded", final_text: "done" },
          { id: "dlg_02", playbook: "podium", instruction: "b", status: "running", task_id: "task_02" },
        ],
      })),
    });
    const res = await h_call(client, { name: "list_delegations", arguments: {} });
    expect(text(res)).toContain("dlg_01");
    expect(text(res)).toContain("dlg_02");
    expect(text(res)).toContain("task_02");
  });
});

describe("tools/call inject_delegation", () => {
  it("passes the id and the text through", async () => {
    const client = stubClient();
    const res = await h_call(client, {
      name: "inject_delegation",
      arguments: { delegation_id: "dlg_01", text: "use main" },
    });
    expect(client.inject).toHaveBeenCalledWith("dlg_01", "use main");
    expect(text(res)).toContain("dlg_01");
    expect(text(res)).toContain("same container");
    expect(isError(res)).toBe(false);
  });
});

describe("tools/call release_preview", () => {
  it("passes the id through and says the preview is gone", async () => {
    const client = stubClient();
    const res = await h_call(client, { name: "release_preview", arguments: { delegation_id: "dlg_01" } });
    expect(client.releasePreview).toHaveBeenCalledWith("dlg_01");
    expect(text(res)).toContain("Released the preview of dlg_01");
    expect(isError(res)).toBe(false);
  });
});

describe("routine_finish", () => {
  it("is listed only for a turn in a routine's chat", async () => {
    const plain = (await newHandler(stubClient())("tools/list", {})) as { tools: { name: string }[] };
    expect(plain.tools.map((t) => t.name)).not.toContain("routine_finish");
    const routine = (await newHandler(stubClient(), { routine: true })("tools/list", {})) as { tools: { name: string }[] };
    expect(routine.tools.map((t) => t.name)).toContain("routine_finish");
  });

  it("hands the conductor the outcome, with the state as JSON", async () => {
    const client = stubClient();
    const res = await newHandler(client, { routine: true })("tools/call", {
      name: "routine_finish",
      arguments: { status: "idle", summary: "nothing new", state: { seen: [1] }, propose_note: "skip drafts" },
    });
    expect(client.finishRoutine).toHaveBeenCalledWith({
      status: "idle",
      summary: "nothing new",
      stateJson: '{"seen":[1]}',
      proposeNote: "skip drafts",
    });
    expect(text(res)).toBe("Recorded as idle.");
  });

  it("refuses a state that is not an object", async () => {
    await expect(
      newHandler(stubClient(), { routine: true })("tools/call", {
        name: "routine_finish",
        arguments: { status: "done", summary: "x", state: [1, 2] },
      }),
    ).rejects.toThrow("state must be a JSON object");
  });
});

describe("create_routine", () => {
  it("is listed only where the person may ask for one, and alone when there is nothing to delegate to", async () => {
    const names = async (opts: Parameters<typeof newHandler>[1]) =>
      ((await newHandler(stubClient(), opts)("tools/list", {})) as { tools: { name: string }[] }).tools.map((t) => t.name);
    expect(await names({})).not.toContain("create_routine");
    expect(await names({ createRoutine: true })).toContain("create_routine");
    expect(await names({ createRoutine: true, delegate: false })).toEqual(["create_routine"]);
  });

  it("hands the conductor the routine to set up and answers with what it said", async () => {
    const client = stubClient();
    const res = await newHandler(client, { createRoutine: true })("tools/call", {
      name: "create_routine",
      arguments: { name: "Digest", instruction: "Report failing PRs.", schedule: "0 9 * * 1-5", timezone: "UTC" },
    });
    expect(client.createRoutine).toHaveBeenCalledWith({
      name: "Digest",
      instruction: "Report failing PRs.",
      schedule: "0 9 * * 1-5",
      timezone: "UTC",
      notes: undefined,
    });
    expect(text(res)).toBe('Created the routine "Digest".');
  });
});

describe("tools/call start_chat", () => {
  it("hands the conductor the chat to open and answers with the link to put in the reply", async () => {
    const client = stubClient();
    const res = await h_call(client, {
      name: "start_chat",
      arguments: {
        title: "Triage: login outage",
        opening: "Starting a triage.",
        share_with: ["dana@acme.com"],
        access: "members",
      },
    });
    expect(client.startChat).toHaveBeenCalledWith({
      title: "Triage: login outage",
      opening: "Starting a triage.",
      share_with: ["dana@acme.com"],
      access: "members",
      role: undefined,
    });
    expect(text(res)).toContain("https://podium.example/agent/chat/chat_new");
    expect(text(res)).toContain("click here to join");
    expect(text(res)).toContain("dana@acme.com");
    expect(isError(res)).toBe(false);
  });

  it("refuses a share_with that is not a list of addresses", async () => {
    const client = stubClient();
    await expect(
      h_call(client, { name: "start_chat", arguments: { title: "t", opening: "o", share_with: "dana" } }),
    ).rejects.toThrow(/share_with/);
    expect(client.startChat).not.toHaveBeenCalled();
  });

  it("relays the conductor's refusal as a failed tool result", async () => {
    const client = stubClient({
      startChat: vi.fn(async () => {
        throw new DelegateError("failed_precondition", "a new chat needs a Podium person to own it");
      }),
    });
    const res = await h_call(client, { name: "start_chat", arguments: { title: "t", opening: "o" } });
    expect(isError(res)).toBe(true);
    expect(text(res)).toContain("needs a Podium person");
  });
});

describe("tools/call cancel_delegation", () => {
  it("passes the reason on", async () => {
    const client = stubClient();
    await h_call(client, {
      name: "cancel_delegation",
      arguments: { delegation_id: "dlg_01", reason: "no longer needed" },
    });
    expect(client.cancel).toHaveBeenCalledWith("dlg_01", "no longer needed");
  });

  it("does not require one", async () => {
    const client = stubClient();
    await h_call(client, { name: "cancel_delegation", arguments: { delegation_id: "dlg_01" } });
    expect(client.cancel).toHaveBeenCalledWith("dlg_01", "");
  });
});

describe("unknown methods and tools", () => {
  it("answers a method it does not know with an error, not a crash", async () => {
    const h = newHandler(stubClient());
    await expect(h("resources/list", {})).rejects.toThrow(/unknown method/);
  });

  it("answers a tool it does not know the same way", async () => {
    const h = newHandler(stubClient());
    await expect(h("tools/call", { name: "rm_rf", arguments: {} })).rejects.toThrow(/unknown tool/);
  });

  it("answers ping", async () => {
    const h = newHandler(stubClient());
    await expect(h("ping", {})).resolves.toEqual({});
  });
});

describe("the stdio transport", () => {
  it("answers a request and stays open for the next one", async () => {
    const { lines, done, input } = pipe(newHandler(stubClient()));
    input.write(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "ping" }) + "\n");
    input.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list" }) + "\n");
    input.end();
    await done;
    expect(lines).toHaveLength(2);
    expect(JSON.parse(lines[0]!)).toEqual({ jsonrpc: "2.0", id: 1, result: {} });
    expect(JSON.parse(lines[1]!).result.tools).toHaveLength(tools.length);
  });

  it("says NOTHING to a notification, because answering one breaks the client", async () => {
    const { lines, done, input } = pipe(newHandler(stubClient()));
    input.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
    input.write(JSON.stringify({ jsonrpc: "2.0", id: 7, method: "ping" }) + "\n");
    input.end();
    await done;
    expect(lines).toHaveLength(1);
    expect(JSON.parse(lines[0]!).id).toBe(7);
  });

  it("drops a line that is not JSON and keeps reading", async () => {
    const warnings: string[] = [];
    const { lines, done, input } = pipe(newHandler(stubClient()), (m) => warnings.push(m));
    input.write("this is not json\n");
    input.write("\n");
    input.write(JSON.stringify({ jsonrpc: "2.0", id: 3, method: "ping" }) + "\n");
    input.end();
    await done;
    expect(lines).toHaveLength(1);
    expect(JSON.parse(lines[0]!).id).toBe(3);
    expect(warnings.join(" ")).toContain("undecodable");
  });

  it("returns a JSON-RPC error for a bad call, with the id it came in on", async () => {
    const { lines, done, input } = pipe(newHandler(stubClient()));
    input.write(
      JSON.stringify({ jsonrpc: "2.0", id: 9, method: "tools/call", params: { name: "delegate", arguments: {} } }) +
        "\n",
    );
    input.end();
    await done;
    const res = JSON.parse(lines[0]!);
    expect(res.id).toBe(9);
    expect(res.error.code).toBe(-32602);
    expect(res.error.message).toMatch(/playbook is required/);
  });
});

describe("urlFrom", () => {
  it("reads the address out of the arguments", () => {
    expect(urlFrom(["--url", "http://127.0.0.1:8090"])).toBe("http://127.0.0.1:8090");
    expect(urlFrom(["--other", "x", "--url", "http://h:1"])).toBe("http://h:1");
  });

  it("is empty when there is none, or when the flag has no value", () => {
    expect(urlFrom([])).toBe("");
    expect(urlFrom(["--url"])).toBe("");
  });
});

/** h_call runs one tools/call against a stub client. */
async function h_call(client: Client, params: Record<string, unknown>): Promise<unknown> {
  return newHandler(client)("tools/call", params);
}

/** pipe wires serve() to a stream pair and collects what it writes. */
function pipe(
  handler: ReturnType<typeof newHandler>,
  onWarn: (m: string) => void = () => {},
): { lines: string[]; done: Promise<void>; input: PassThrough } {
  const input = new PassThrough();
  const lines: string[] = [];
  const done = serve(input, (line) => lines.push(line), handler, onWarn);
  return { lines, done, input };
}
