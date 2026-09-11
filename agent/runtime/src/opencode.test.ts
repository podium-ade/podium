import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import {
  AgentName,
  BrowserBinary,
  BrowserServer,
  DelegateServer,
  HumanServer,
  invocation,
  KnownTools,
  MemoryServer,
  ReservedServers,
  resolveBrowserURL,
  skillPermission,
  writeConfig,
} from "./opencode.js";

function write(over: Partial<Parameters<typeof writeConfig>[0]> = {}) {
  const dir = mkdtempSync(join(tmpdir(), "octest-"));
  writeConfig({
    dir,
    systemPrompt: "be direct",
    tools: ["read", "bash"],
    providerID: "anthropic",
    ...over,
  });
  return {
    dir,
    config: JSON.parse(readFileSync(join(dir, "opencode.json"), "utf8")) as Record<string, any>,
    prompt: readFileSync(join(dir, "system.md"), "utf8"),
  };
}

describe("writeConfig", () => {
  it("puts the operating contract in its own file, verbatim", () => {
    // The prompt is the whole contract for the turn and runs to kilobytes. Inlining it into
    // JSON is how somebody eventually breaks it on an escape.
    const contract = 'Answer directly.\n\n"Quoted", \\backslashed\\, and \ttabbed.\n';
    const { config, prompt } = write({ systemPrompt: contract });
    expect(prompt).toBe(contract);
    expect(config.agent[AgentName].prompt).toBe("{file:./system.md}");
  });

  it("is an allow-list: a tool not named is disabled, not left to the harness's default", () => {
    const { config } = write({ tools: ["read", "grep"] });
    const tools = config.agent[AgentName].tools as Record<string, boolean>;
    expect(tools.read).toBe(true);
    expect(tools.grep).toBe(true);
    // The ones the playbook did not name are explicitly off. Omitting them would leave the
    // harness's own defaults in place, which is the opposite of an allow-list.
    expect(tools.bash).toBe(false);
    expect(tools.write).toBe(false);
    expect(tools.edit).toBe(false);
    // Every tool the harness has is decided one way or the other.
    for (const t of KnownTools) {
      expect(tools).toHaveProperty(t);
    }
  });

  it("omits the provider block when there is no endpoint opinion", () => {
    // An empty baseURL would override the harness's own default with nothing.
    expect(write().config.provider).toBeUndefined();
  });

  it("overrides the endpoint when one is given, under that provider's id", () => {
    const { config } = write({ providerID: "xai", baseURL: "https://xai.proxy.internal" });
    expect(config.provider.xai.options.baseURL).toBe("https://xai.proxy.internal");
  });

  it("wires memory as a remote MCP server without writing the token to disk", () => {
    const { config } = write({
      memory: { url: "http://host.docker.internal:8888/mcp/podium/", apiKeyEnv: "PODIUM_MEMORY_API_KEY" },
    });
    const mcp = config.mcp[MemoryServer];
    expect(mcp.type).toBe("remote");
    expect(mcp.enabled).toBe(true);
    // {env:...} is resolved by the harness at run time. The config file on disk holds the
    // NAME of the variable and never its value.
    expect(mcp.headers.Authorization).toBe("Bearer {env:PODIUM_MEMORY_API_KEY}");
    expect(JSON.stringify(config)).not.toContain("PODIUM_MEMORY_API_KEY=");
  });

  it("enables the memory tools when the host has memory, whatever the playbook listed", () => {
    // A playbook cannot opt out of memory: the conductor decides whether a turn gets one.
    const { config } = write({
      tools: ["read"],
      memory: { url: "http://x/mcp", apiKeyEnv: "PODIUM_MEMORY_API_KEY" },
    });
    expect(config.agent[AgentName].tools[`${MemoryServer}*`]).toBe(true);
  });

  it("leaves memory out entirely on a host that has none", () => {
    const { config } = write();
    expect(config.mcp).toBeUndefined();
    expect(config.agent[AgentName].tools[`${MemoryServer}*`]).toBeUndefined();
  });

  it("denies every skill when the playbook named none", () => {
    // Not an omission: with everything denied the harness drops the `skill` tool from the
    // agent, so the skills built into the harness itself cannot be loaded either.
    expect(write().config.permission).toEqual({ skill: { "*": "deny" } });
  });

  it("denies every skill and allows the ones the playbook named, in that order", () => {
    const { config } = write({ skills: ["pr-review", "release-notes"] });
    expect(config.permission.skill).toEqual({
      "*": "deny",
      "pr-review": "allow",
      "release-notes": "allow",
    });
    // The harness evaluates the LAST matching rule, so the wildcard has to be written
    // first or it denies the skills that follow it.
    expect(Object.keys(config.permission.skill)[0]).toBe("*");
  });
});

describe("skillPermission", () => {
  it("is a deny-by-default map with the wildcard first", () => {
    expect(skillPermission([])).toEqual({ "*": "deny" });
    expect(Object.keys(skillPermission(["a", "b"]))).toEqual(["*", "a", "b"]);
  });
});

describe("writeConfig with a browser", () => {
  it("drives the sidecar browser with a local server that attaches over CDP", () => {
    const { config } = write({ browser: { cdpURL: "http://chrome:9222" } });
    const mcp = config.mcp[BrowserServer];
    // Local, not remote: the server runs in this container and the browser it drives is
    // the sidecar. That split is why no Chromium is installed here.
    expect(mcp.type).toBe("local");
    expect(mcp.enabled).toBe(true);
    expect(mcp.command).toEqual([BrowserBinary, "--browserUrl", "http://chrome:9222"]);
  });

  it("enables the browser tools when the turn has a browser, whatever the playbook listed", () => {
    const { config } = write({ tools: ["read"], browser: { cdpURL: "http://chrome:9222" } });
    expect(config.agent[AgentName].tools[`${BrowserServer}*`]).toBe(true);
  });

  it("leaves the browser out entirely for a playbook that did not ask for one", () => {
    const { config } = write();
    expect(config.agent[AgentName].tools[`${BrowserServer}*`]).toBeUndefined();
  });

  it("carries memory and a browser together, each keyed by its own server name", () => {
    const { config } = write({
      memory: { url: "http://x/mcp", apiKeyEnv: "PODIUM_MEMORY_API_KEY" },
      browser: { cdpURL: "http://chrome:9222" },
    });
    expect(Object.keys(config.mcp).sort()).toEqual([BrowserServer, MemoryServer].sort());
  });

  // The combination the QA playbook uses, and the one nothing else covers: a browser puts
  // an MCP server and a `browser*` tool entry in the config, skills put a `permission.skill`
  // map in it, and each is written where the other is not looking.
  it("carries a browser and Agent Skills together, keyed separately", () => {
    const { config } = write({
      browser: { cdpURL: "http://chrome:9222" },
      skills: ["pr-review"],
    });
    expect(Object.keys(config.mcp)).toEqual([BrowserServer]);
    expect(config.mcp[BrowserServer].command).toEqual([BrowserBinary, "--browserUrl", "http://chrome:9222"]);
    expect(config.agent[AgentName].tools[`${BrowserServer}*`]).toBe(true);
    expect(config.permission.skill).toEqual({ "*": "deny", "pr-review": "allow" });
  });
});

describe("writeConfig with delegation", () => {
  const delegation = { url: "http://127.0.0.1:8090", entry: "/opt/podium-agent/dist/mcp.js" };

  it("runs this runtime's own second entrypoint, with the address as an argument", () => {
    const { config } = write({ delegation });
    expect(config.mcp.podium.type).toBe("local");
    // node, the entrypoint, then --url and the address. The TOKEN is not here: an argument
    // is visible in a process list, so it travels in the environment.
    expect(config.mcp.podium.command).toEqual([
      process.execPath,
      "/opt/podium-agent/dist/mcp.js",
      "--url",
      "http://127.0.0.1:8090",
    ]);
    expect(config.mcp.podium.enabled).toBe(true);
    expect(JSON.stringify(config)).not.toContain("PODIUM_TURN_TOKEN");
  });

  it("turns the podium tools on, because a host turn's short tool list depends on them", () => {
    const { config } = write({ delegation, tools: ["webfetch"] });
    expect(config.agent.podium.tools["podium*"]).toBe(true);
    expect(config.agent.podium.tools.webfetch).toBe(true);
    expect(config.agent.podium.tools.bash).toBe(false);
  });

  it("writes no server and no tools when the turn cannot delegate", () => {
    const { config } = write({});
    expect(config.mcp).toBeUndefined();
    expect(config.agent.podium.tools["podium*"]).toBeUndefined();
  });

  it("sits beside memory and the browser rather than replacing either", () => {
    const { config } = write({
      delegation,
      memory: { url: "http://memory/mcp/", apiKeyEnv: "K" },
      browser: { cdpURL: "http://127.0.0.1:9222" },
    });
    expect(Object.keys(config.mcp).sort()).toEqual(["browser", "memory", "podium"]);
    expect(config.agent.podium.tools["memory*"]).toBe(true);
    expect(config.agent.podium.tools["browser*"]).toBe(true);
    expect(config.agent.podium.tools["podium*"]).toBe(true);
  });
});

describe("writeConfig with interactive", () => {
  it("runs the ask entrypoint as a local MCP server and enables its tools", () => {
    const { config } = write({ interactive: { entry: "/opt/podium-agent/dist/ask.js" } });
    expect(config.mcp[HumanServer].type).toBe("local");
    expect(config.mcp[HumanServer].command).toEqual([process.execPath, "/opt/podium-agent/dist/ask.js"]);
    expect(config.agent[AgentName].tools[`${HumanServer}*`]).toBe(true);
  });

  it("writes no server when the playbook is not interactive", () => {
    const { config } = write({});
    expect(config.mcp).toBeUndefined();
    expect(config.agent[AgentName].tools[`${HumanServer}*`]).toBeUndefined();
  });
});

describe("writeConfig with the playbook's own MCP servers", () => {
  const linear = { name: "linear", url: "https://mcp.linear.app/mcp", token_env: "PODIUM_MCP_LINEAR_TOKEN" };

  it("wires a playbook's server as a remote server without writing the token to disk", () => {
    const { config } = write({ mcpServers: [linear] });
    const mcp = config.mcp.linear;
    expect(mcp.type).toBe("remote");
    expect(mcp.enabled).toBe(true);
    expect(mcp.url).toBe("https://mcp.linear.app/mcp");
    // The MCP authorization specification's own scheme, and the NAME of the variable: the
    // token is resolved by the harness at run time and is never in this file.
    expect(mcp.headers.Authorization).toBe("Bearer {env:PODIUM_MCP_LINEAR_TOKEN}");
    expect(JSON.stringify(config)).not.toContain("lin_api");
  });

  it("sends no authorization header for a server registered without a token", () => {
    const { config } = write({ mcpServers: [{ name: "wiki", url: "http://wiki:9000/mcp" }] });
    expect(config.mcp.wiki.url).toBe("http://wiki:9000/mcp");
    expect(config.mcp.wiki.headers).toBeUndefined();
  });

  it("enables the server's tools wholesale, whatever the playbook listed", () => {
    // Naming the server in mcp_servers is what granting it means. A playbook cannot
    // describe individual tools of a server whose tool list only exists once it is
    // connected to.
    const { config } = write({ tools: ["read"], mcpServers: [linear] });
    expect(config.agent[AgentName].tools["linear*"]).toBe(true);
    expect(config.agent[AgentName].tools.bash).toBe(false);
  });

  it("carries memory, a browser and the playbook's servers together, each under its own key", () => {
    const { config } = write({
      memory: { url: "http://x/mcp", apiKeyEnv: "PODIUM_MEMORY_API_KEY" },
      browser: { cdpURL: "http://chrome:9222" },
      mcpServers: [linear],
    });
    expect(Object.keys(config.mcp).sort()).toEqual([BrowserServer, MemoryServer, "linear"].sort());
    expect(config.mcp[MemoryServer].url).toBe("http://x/mcp");
  });

  it("refuses a server that would take the name of one the runtime wires up itself", () => {
    // Unreachable while the conductor refuses the reserved names, and refused again here
    // because the cost of it getting through is silent: this map is keyed by name, so a
    // second `memory` would replace the shared memory and a second `podium` would replace
    // the delegation a host turn works through.
    for (const name of [MemoryServer, BrowserServer, DelegateServer]) {
      expect(() => write({ mcpServers: [{ name, url: "http://evil/mcp" }] })).toThrow(/reserved/);
    }
    expect(ReservedServers).toEqual(new Set([MemoryServer, BrowserServer, DelegateServer, HumanServer]));
  });

  it("sits beside the delegation server rather than replacing it", () => {
    const { config } = write({
      delegation: { url: "http://127.0.0.1:8090", entry: "/opt/podium-agent/dist/mcp.js" },
      mcpServers: [linear],
    });
    expect(Object.keys(config.mcp).sort()).toEqual([DelegateServer, "linear"].sort());
    expect(config.agent[AgentName].tools[`${DelegateServer}*`]).toBe(true);
    expect(config.agent[AgentName].tools["linear*"]).toBe(true);
  });

  it("leaves the mcp block out for a playbook that named none", () => {
    const { config } = write({ mcpServers: [] });
    expect(config.mcp).toBeUndefined();
  });
});

describe("invocation", () => {
  const base = {
    configDir: "/tmp/podium-turn-abc",
    workdir: "/workspace",
    providerID: "xai",
    model: "grok-4.6",
    instruction: "do the thing",
    env: { PATH: "/usr/bin" },
  };

  it("points the harness at the config it just wrote", () => {
    // Without this the harness looks for a config in the project --dir names, finds none,
    // and runs `--agent podium` as its own default agent: no system prompt, no allow-list.
    const { env } = invocation(base);
    expect(env["OPENCODE_CONFIG"]).toBe("/tmp/podium-turn-abc/opencode.json");
    expect(env["PATH"]).toBe("/usr/bin");
  });

  it("runs the turn's agent against the workspace", () => {
    const { argv, cwd } = invocation(base);
    expect(argv).toEqual([
      "run",
      "--format",
      "json",
      "--agent",
      AgentName,
      "--auto",
      "--model",
      "xai/grok-4.6",
      "--dir",
      "/workspace",
      "do the thing",
    ]);
    expect(cwd).toBe("/tmp/podium-turn-abc");
  });

  it("passes an effort through as the variant, and omits it when the profile names none", () => {
    expect(invocation({ ...base, effort: "high" }).argv).toContain("--variant");
    expect(invocation({ ...base, effort: "high" }).argv).toContain("high");
    expect(invocation(base).argv).not.toContain("--variant");
  });

  it("continues a session when one is named, so an inject does not start from scratch", () => {
    const { argv } = invocation({ ...base, sessionID: "ses_01", instruction: "use main" });
    expect(argv).toContain("--session");
    expect(argv).toContain("ses_01");
    expect(argv.at(-1)).toBe("use main");
    expect(invocation(base).argv).not.toContain("--session");
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
