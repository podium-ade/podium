import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import {
  endedOnModelTurn,
  errorOf,
  harnessTools,
  lostToNetwork,
  mcpCount,
  resetTakesThinking,
  serverEnv,
  takesThinking,
  AgentName,
  BrowserBinary,
  BrowserServer,
  DelegateServer,
  HumanServer,
  invocation,
  KnownTools,
  LegacyTools,
  MemoryServer,
  ReservedServers,
  TitleServer,
  resolveBrowserURL,
  skillRules,
  writeConfig,
  type Rule,
} from "./opencode.js";

function write(over: Partial<Parameters<typeof writeConfig>[0]> = {}) {
  const dir = mkdtempSync(join(tmpdir(), "octest-"));
  writeConfig({
    dir,
    systemPrompt: "be direct",
    tools: ["read", "shell"],
    providerID: "anthropic",
    ...over,
  });
  const config = JSON.parse(readFileSync(join(dir, "opencode.json"), "utf8")) as Record<string, any>;
  return {
    dir,
    config,
    prompt: readFileSync(join(dir, "system.md"), "utf8"),
    rules: config.agents[AgentName].permissions as Rule[],
    servers: (config.mcp?.servers ?? undefined) as Record<string, any> | undefined,
  };
}

/** denied is the set of actions the agent's rules deny outright. */
function denied(rules: Rule[]): Set<string> {
  return new Set(rules.filter((r) => r.resource === "*" && r.effect === "deny").map((r) => r.action));
}

describe("writeConfig", () => {
  it("puts the operating contract in its own file, verbatim", () => {
    // The prompt is the whole contract for the turn and runs to kilobytes. Inlining it into
    // JSON is how somebody eventually breaks it on an escape.
    const contract = 'Answer directly.\n\n"Quoted", \\backslashed\\, and \ttabbed.\n';
    const { config, prompt } = write({ systemPrompt: contract });
    expect(prompt).toBe(contract);
    expect(config.agents[AgentName].system).toBe("{file:./system.md}");
    expect(config.agents[AgentName].mode).toBe("primary");
  });

  it("turns off opencode's own titling and sharing", () => {
    const { config } = write();
    expect(config.agents.title).toEqual({ disabled: true });
    expect(config.share).toBe("disabled");
  });

  it("is an allow-list: a built-in tool not named is denied, not left to the harness's default", () => {
    const { rules } = write({ tools: ["read", "grep"] });
    const off = denied(rules);
    expect(off.has("read")).toBe(false);
    expect(off.has("grep")).toBe(false);
    // The ones the playbook did not name are explicitly off. Omitting them would leave the
    // harness's own defaults in place, which is the opposite of an allow-list.
    for (const t of KnownTools.filter((t) => t !== "read" && t !== "grep")) {
      expect(off.has(t)).toBe(true);
    }
  });

  it("always denies the question tool and Code Mode", () => {
    // Nobody answers a question at a headless turn, and Code Mode is the only way to the
    // harness's own session and worktree tools.
    const off = denied(write({ tools: [...KnownTools] }).rules);
    expect(off.has("question")).toBe(true);
    expect(off.has("execute")).toBe(true);
  });

  it("denies opencode's own tools, which no playbook may grant", () => {
    // session_move would take the session out of the workspace; worktree_create makes
    // directories outside it.
    expect(denied(write({ tools: [...KnownTools] }).rules).has("opencode_*")).toBe(true);
  });

  it("reads a playbook's opencode 1 tool names as the tools they became", () => {
    const off = denied(write({ tools: ["bash", "task", "list", "patch", "todoread"] }).rules);
    for (const t of ["shell", "subagent", "glob", "edit", "todowrite"]) {
      expect(off.has(t)).toBe(false);
    }
    expect(off.has("write")).toBe(true);
    for (const [legacy, now] of Object.entries(LegacyTools)) {
      expect(harnessTools(legacy)).toEqual(now);
      for (const t of now) expect(KnownTools).toContain(t);
    }
    expect(harnessTools("read")).toEqual(["read"]);
  });

  it("switches the experimental todo tool on only when the playbook asked for it", () => {
    expect(write({ tools: ["todowrite"] }).config.experimental).toEqual({ todo: true });
    expect(write({ tools: ["read"] }).config.experimental).toBeUndefined();
  });

  it("omits the provider block when there is no endpoint opinion and no key", () => {
    // An empty baseURL would override the harness's own default with nothing.
    expect(write().config.providers).toBeUndefined();
  });

  it("overrides the endpoint when one is given, under that provider's id", () => {
    const { config } = write({ providerID: "xai", baseURL: "https://xai.proxy.internal" });
    expect(config.providers.xai.settings.baseURL).toBe("https://xai.proxy.internal");
  });

  it("names the key variable for every provider, never its value", () => {
    for (const [providerID, env] of [
      ["google", "GOOGLE_API_KEY"],
      ["xai", "XAI_API_KEY"],
      ["anthropic", "ANTHROPIC_API_KEY"],
    ] as const) {
      const { config } = write({ providerID, apiKeyEnv: env });
      expect(config.providers[providerID].settings.apiKey).toBe(`{env:${env}}`);
    }
  });

  it("adds the ChatGPT account header when an OpenAI token is a JWT", () => {
    const payload = Buffer.from(
      JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "acct-9" } }),
    ).toString("base64url");
    const jwt = `eyJhbGciOiJub25lIn0.${payload}.sig`;
    const prev = process.env.OPENAI_API_KEY;
    process.env.OPENAI_API_KEY = jwt;
    try {
      const { config } = write({
        providerID: "openai",
        apiKeyEnv: "OPENAI_API_KEY",
        baseURL: "https://chatgpt.com/backend-api/codex",
      });
      expect(config.providers.openai.headers["ChatGPT-Account-Id"]).toBe("acct-9");
      expect(config.providers.openai.headers.originator).toBe("podium");
      expect(JSON.stringify(config)).not.toContain(jwt);
    } finally {
      if (prev === undefined) {
        delete process.env.OPENAI_API_KEY;
      } else {
        process.env.OPENAI_API_KEY = prev;
      }
    }
  });

  it("wires memory as a remote MCP server without writing the token to disk", () => {
    const { config, servers } = write({
      memory: { url: "http://host.docker.internal:8888/mcp/podium/", apiKeyEnv: "PODIUM_MEMORY_API_KEY" },
    });
    const mcp = servers![MemoryServer];
    expect(mcp.type).toBe("remote");
    // {env:...} is resolved by the harness at run time. The config file on disk holds the
    // NAME of the variable and never its value.
    expect(mcp.headers.Authorization).toBe("Bearer {env:PODIUM_MEMORY_API_KEY}");
    expect(JSON.stringify(config)).not.toContain("PODIUM_MEMORY_API_KEY=");
  });

  it("turns Code Mode off on every MCP server, so their tools are plain `<server>_<tool>` calls", () => {
    const { servers } = write({
      memory: { url: "http://x/mcp", apiKeyEnv: "K" },
      browser: { cdpURL: "http://chrome:9222" },
      delegation: { url: "http://127.0.0.1:8090", entry: "/opt/podium-agent/dist/mcp.js" },
      interactive: { entry: "/opt/podium-agent/dist/ask.js" },
      title: { entry: "/opt/podium-agent/dist/title.js" },
      mcpServers: [{ name: "linear", url: "https://mcp.linear.app/mcp" }],
    });
    expect(Object.keys(servers!).length).toBe(6);
    for (const s of Object.values(servers!)) {
      expect(s.codemode).toBe(false);
    }
  });

  it("leaves memory out entirely on a host that has none", () => {
    expect(write().config.mcp).toBeUndefined();
  });

  it("denies every skill when the playbook named none", () => {
    // Not an omission: with everything denied, the skills built into the harness itself
    // cannot be loaded either.
    const skill = write().rules.filter((r) => r.action === "skill");
    expect(skill).toEqual([{ action: "skill", resource: "*", effect: "deny" }]);
  });

  it("denies every skill and allows the ones the playbook named, in that order", () => {
    const skill = write({ skills: ["pr-review", "release-notes"] }).rules.filter((r) => r.action === "skill");
    // The harness evaluates the LAST matching rule, so the wildcard has to be written
    // first or it denies the skills that follow it.
    expect(skill).toEqual([
      { action: "skill", resource: "*", effect: "deny" },
      { action: "skill", resource: "pr-review", effect: "allow" },
      { action: "skill", resource: "release-notes", effect: "allow" },
    ]);
  });

  it("points the harness at the mounted skills directory, and nowhere without one", () => {
    const { config } = write({ skills: ["pr-review"], skillsDir: "/podium/files/skills" });
    expect(config.skills).toEqual(["/podium/files/skills"]);
    expect(write({ skills: ["pr-review"] }).config.skills).toBeUndefined();
  });

  it("counts the MCP servers it named, for the server start to wait on", () => {
    const { dir } = write({
      memory: { url: "http://x/mcp", apiKeyEnv: "K" },
      mcpServers: [{ name: "linear", url: "https://mcp.linear.app/mcp" }],
    });
    expect(mcpCount(dir)).toBe(2);
    expect(mcpCount(write().dir)).toBe(0);
    expect(mcpCount("/nonexistent")).toBe(0);
  });
});

describe("skillRules", () => {
  it("is deny-by-default with the wildcard first", () => {
    expect(skillRules([])).toEqual([{ action: "skill", resource: "*", effect: "deny" }]);
    expect(skillRules(["a", "b"]).map((r) => r.resource)).toEqual(["*", "a", "b"]);
  });
});

describe("writeConfig with a browser", () => {
  it("drives the sidecar browser with a local server that attaches over CDP", () => {
    const { servers } = write({ browser: { cdpURL: "http://chrome:9222" } });
    const mcp = servers![BrowserServer];
    // Local, not remote: the server runs in this container and the browser it drives is
    // the sidecar. That split is why no Chromium is installed here.
    expect(mcp.type).toBe("local");
    expect(mcp.command).toEqual([BrowserBinary, "--browserUrl", "http://chrome:9222"]);
  });

  // The combination the QA playbook uses: a browser puts an MCP server in the config, and
  // skills put rules in the agent's permissions, and neither replaces the other.
  it("carries a browser and Agent Skills together, keyed separately", () => {
    const { servers, rules } = write({ browser: { cdpURL: "http://chrome:9222" }, skills: ["pr-review"] });
    expect(Object.keys(servers!)).toEqual([BrowserServer]);
    expect(rules).toContainEqual({ action: "skill", resource: "pr-review", effect: "allow" });
  });
});

describe("writeConfig with delegation", () => {
  const delegation = { url: "http://127.0.0.1:8090", entry: "/opt/podium-agent/dist/mcp.js" };

  it("runs this runtime's own second entrypoint, with the address as an argument", () => {
    const { config, servers } = write({ delegation });
    expect(servers!.podium.type).toBe("local");
    // node, the entrypoint, then --url and the address. The TOKEN is not here: an argument
    // is visible in a process list, so it travels in the environment.
    expect(servers!.podium.command).toEqual([
      process.execPath,
      "/opt/podium-agent/dist/mcp.js",
      "--url",
      "http://127.0.0.1:8090",
    ]);
    expect(JSON.stringify(config)).not.toContain("PODIUM_TURN_TOKEN");
  });

  it("does not deny the podium tools, which a host turn's short tool list depends on", () => {
    const off = denied(write({ delegation, tools: ["webfetch"] }).rules);
    expect([...off].some((a) => a.startsWith("podium"))).toBe(false);
    expect(off.has("webfetch")).toBe(false);
    expect(off.has("shell")).toBe(true);
  });

  it("sits beside memory and the browser rather than replacing either", () => {
    const { servers } = write({
      delegation,
      memory: { url: "http://memory/mcp/", apiKeyEnv: "K" },
      browser: { cdpURL: "http://127.0.0.1:9222" },
    });
    expect(Object.keys(servers!).sort()).toEqual(["browser", "memory", "podium"]);
  });
});

describe("writeConfig with a title tool", () => {
  it("runs the title entrypoint, without putting the file path in the config", () => {
    const { config, servers } = write({ title: { entry: "/opt/podium-agent/dist/title.js" } });
    expect(servers![TitleServer].type).toBe("local");
    expect(servers![TitleServer].command).toEqual([process.execPath, "/opt/podium-agent/dist/title.js"]);
    expect(JSON.stringify(config)).not.toContain("PODIUM_CHAT_TITLE_FILE");
    expect(JSON.stringify(config)).not.toContain("chat-title.txt");
  });
});

describe("writeConfig with interactive", () => {
  it("runs the ask entrypoint as a local MCP server", () => {
    const { servers } = write({ interactive: { entry: "/opt/podium-agent/dist/ask.js" } });
    expect(servers![HumanServer].type).toBe("local");
    expect(servers![HumanServer].command).toEqual([process.execPath, "/opt/podium-agent/dist/ask.js"]);
  });
});

describe("writeConfig with the playbook's own MCP servers", () => {
  const linear = { name: "linear", url: "https://mcp.linear.app/mcp", token_env: "PODIUM_MCP_LINEAR_TOKEN" };

  it("wires a playbook's server as a remote server without writing the token to disk", () => {
    const { config, servers } = write({ mcpServers: [linear] });
    const mcp = servers!.linear;
    expect(mcp.type).toBe("remote");
    expect(mcp.url).toBe("https://mcp.linear.app/mcp");
    // The MCP authorization specification's own scheme, and the NAME of the variable: the
    // token is resolved by the harness at run time and is never in this file.
    expect(mcp.headers.Authorization).toBe("Bearer {env:PODIUM_MCP_LINEAR_TOKEN}");
    expect(JSON.stringify(config)).not.toContain("lin_api");
  });

  it("sends no authorization header for a server registered without a token", () => {
    const { servers } = write({ mcpServers: [{ name: "wiki", url: "http://wiki:9000/mcp" }] });
    expect(servers!.wiki.url).toBe("http://wiki:9000/mcp");
    expect(servers!.wiki.headers).toBeUndefined();
  });

  it("keeps a server's headers beside its token, and translates what opencode 2 reads differently", () => {
    const grafana = {
      name: "grafana",
      url: "https://mcp.grafana.com/mcp",
      token_env: "PODIUM_MCP_GRAFANA_TOKEN",
      config: {
        headers: { "X-Grafana-URL": "https://stack.grafana.net" },
        timeout: 30000,
        whatever: { nested: true },
        url: "https://elsewhere/mcp",
      },
    };
    const { servers } = write({ mcpServers: [grafana] });
    expect(servers!.grafana.headers).toEqual({
      "X-Grafana-URL": "https://stack.grafana.net",
      Authorization: "Bearer {env:PODIUM_MCP_GRAFANA_TOKEN}",
    });
    // One number was opencode 1's timeout for everything; opencode 2 has one per phase.
    expect(servers!.grafana.timeout).toEqual({ startup: 30000, catalog: 30000, execution: 30000 });
    // A key opencode 2 does not read is dropped rather than failing the whole config, and
    // the form's own fields win.
    expect(servers!.grafana.whatever).toBeUndefined();
    expect(servers!.grafana.url).toBe("https://mcp.grafana.com/mcp");
  });

  it("refuses a config the harness would expand", () => {
    const leak = { name: "wiki", url: "http://wiki/mcp", config: { deep: ["{env:PODIUM_TURN_TOKEN}"] } };
    expect(() => write({ mcpServers: [leak] })).toThrow(/may not reference/);
  });

  it("refuses a server that would take the name of one the runtime wires up itself", () => {
    // Unreachable while the conductor refuses the reserved names, and refused again here
    // because the cost of it getting through is silent: this map is keyed by name, so a
    // second `memory` would replace the shared memory and a second `podium` would replace
    // the delegation a host turn works through.
    for (const name of [MemoryServer, BrowserServer, DelegateServer]) {
      expect(() => write({ mcpServers: [{ name, url: "http://evil/mcp" }] })).toThrow(/reserved/);
    }
    expect(ReservedServers).toEqual(
      new Set([MemoryServer, BrowserServer, DelegateServer, HumanServer, TitleServer]),
    );
  });

  it("leaves the mcp block out for a playbook that named none", () => {
    expect(write({ mcpServers: [] }).config.mcp).toBeUndefined();
  });
});

describe("invocation", () => {
  const base = {
    server: { url: "http://127.0.0.1:41234", password: "pw" },
    workdir: "/workspace",
    providerID: "xai",
    model: "grok-4.6",
    instruction: "do the thing",
    env: { PATH: "/usr/bin" },
  };

  it("runs the turn's agent on the turn's server, in the workspace", () => {
    const { argv, cwd, env } = invocation(base);
    expect(argv).toEqual([
      "run",
      "--server",
      "http://127.0.0.1:41234",
      "--format",
      "json",
      "--agent",
      AgentName,
      "--auto",
      "--model",
      "xai/grok-4.6",
      "do the thing",
    ]);
    // opencode 2 has no --dir: the run's directory is the session's project.
    expect(cwd).toBe("/workspace");
    expect(env["OPENCODE_SERVER_PASSWORD"]).toBe("pw");
    expect(env["PATH"]).toBe("/usr/bin");
  });

  it("asks for reasoning only when the harness takes the flag", () => {
    expect(invocation({ ...base, thinking: true }).argv).toContain("--thinking");
    expect(invocation(base).argv).not.toContain("--thinking");
  });

  it("passes an effort as the model's variant, and omits it when the profile names none", () => {
    expect(invocation({ ...base, effort: "high" }).argv).toContain("xai/grok-4.6#high");
    expect(invocation(base).argv).toContain("xai/grok-4.6");
  });

  it("continues a session when one is named, so an inject does not start from scratch", () => {
    const { argv } = invocation({ ...base, sessionID: "ses_01", instruction: "use main" });
    expect(argv).toContain("--session");
    expect(argv).toContain("ses_01");
    expect(argv.at(-1)).toBe("use main");
    expect(invocation(base).argv).not.toContain("--session");
  });
});

describe("serverEnv", () => {
  it("points the server at the config it was written and gives it its password", () => {
    // Without the config the server runs `--agent podium` as its own default agent: no
    // system prompt of ours, and no allow-list.
    const env = serverEnv({ PATH: "/usr/bin" }, "/tmp/podium-turn-abc", "pw");
    expect(env["OPENCODE_CONFIG"]).toBe("/tmp/podium-turn-abc/opencode.json");
    expect(env["OPENCODE_SERVER_PASSWORD"]).toBe("pw");
    expect(env["PATH"]).toBe("/usr/bin");
  });
});

describe("resolveBrowserURL", () => {
  const lookup = async (host: string) => (host === "chrome" ? "172.22.0.2" : Promise.reject(new Error("nope")));

  it("turns the sidecar's name into its address, because Chrome refuses a name", async () => {
    // "Host header is specified and is not an IP address or localhost" is what the DevTools
    // endpoint answers otherwise, and no client gets as far as a WebSocket.
    expect(await resolveBrowserURL("http://chrome:9222", lookup)).toBe("http://172.22.0.2:9222");
  });

  it("leaves an address alone", async () => {
    expect(await resolveBrowserURL("http://172.22.0.2:9222", lookup)).toBe("http://172.22.0.2:9222");
    expect(await resolveBrowserURL("http://localhost:9222", lookup)).toBe("http://localhost:9222");
  });

  it("passes the URL through when the name does not resolve", async () => {
    // A browser that cannot be found is the harness's error to report, not a reason to
    // refuse a turn that may never open a page.
    expect(await resolveBrowserURL("http://nowhere:9222", lookup)).toBe("http://nowhere:9222");
    expect(await resolveBrowserURL("not a url", lookup)).toBe("not a url");
  });
});

describe("errorOf", () => {
  it("reads the harness's reason off an error event", () => {
    expect(
      errorOf({ type: "error", error: { name: "APIError", data: { message: "credit balance is too low" } } }),
    ).toBe("credit balance is too low");
    expect(errorOf({ type: "error", error: { name: "UnknownError" } })).toBe("UnknownError");
    // opencode 2's shape.
    expect(
      errorOf({ type: "error", error: { type: "provider.invalid-request", message: "prompt is too long" } }),
    ).toBe("prompt is too long");
    expect(errorOf({ type: "error", error: { type: "unknown" } })).toBe("unknown");
    expect(errorOf({ type: "error" })).toBe("an unnamed error");
    expect(errorOf({ type: "text" })).toBeUndefined();
  });
});

describe("lostToNetwork", () => {
  it("knows a connection that dropped from an error the provider gave", () => {
    // What a review on popos died of after eleven good steps.
    expect(lostToNetwork("EPROTO: The TLS handshake failed. Does the server speak TLS on this port?")).toBe(true);
    expect(lostToNetwork("read ECONNRESET")).toBe(true);
    expect(lostToNetwork("getaddrinfo EAI_AGAIN api.anthropic.com")).toBe(true);
    expect(lostToNetwork("The socket connection was closed unexpectedly.")).toBe(true);
    expect(lostToNetwork("credit balance is too low")).toBe(false);
    expect(lostToNetwork("Requests ending with a model turn are not supported")).toBe(false);
    expect(lostToNetwork(undefined)).toBe(false);
  });
});

describe("takesThinking", () => {
  it("asks for reasoning only from a harness that says it takes the flag, and asks once", () => {
    resetTakesThinking();
    let asked = 0;
    const old = () => {
      asked++;
      return "opencode run [message..]\n  --format  format\n  --variant  model variant";
    };
    expect(takesThinking({}, old)).toBe(false);
    expect(takesThinking({}, old)).toBe(false);
    expect(asked).toBe(1);

    resetTakesThinking();
    expect(takesThinking({}, () => "  --thinking  show thinking blocks")).toBe(true);
    resetTakesThinking();
  });

});

describe("endedOnModelTurn", () => {
  it("is Gemini refusing a request that ends on the model's turn", () => {
    expect(endedOnModelTurn("Requests ending with a model turn are not supported.")).toBe(true);
    expect(endedOnModelTurn("credit balance is too low")).toBe(false);
    expect(endedOnModelTurn(undefined)).toBe(false);
  });
});
