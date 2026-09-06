import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { AgentName, KnownTools, MemoryServer, writeConfig } from "./opencode.js";

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
    // The ones the skill did not name are explicitly off. Omitting them would leave the
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

  it("enables the memory tools when the host has memory, whatever the skill listed", () => {
    // A skill cannot opt out of memory: the conductor decides whether a turn gets one.
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
});
