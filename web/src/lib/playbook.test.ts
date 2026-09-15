import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { PlaybookDefinitionSchema } from "../gen/podium/agent/v1/agent_pb";
import {
  definitionToYaml,
  docToDraft,
  draftToYaml,
  parsePlaybookYaml,
  type PlaybookDraft,
} from "./playbook";

const draft: PlaybookDraft = {
  name: "reporter",
  image: "ghcr.io/example/reporter:v1",
  systemPrompt: "Write the weekly report.",
  allowedTools: ["read", "bash"],
  maxTurns: 50,
  timeout: "30m",
  model: "",
  agent: "",
  effort: "",
  labels: ["linux"],
  priority: 0,
  resources: { cpu: 0, memoryMb: 0, pids: 0 },
  secrets: [{ name: "podium.agent.github_token", target: "env", key: "GITHUB_TOKEN" }],
  repos: [],
  slackChannels: [],
  linear: false,
  interactive: true,
  skills: ["pr-review"],
  mcpServers: ["linear"],
  env: { CI: "true" },
  docker: false,
  browser: false,
};

describe("parsePlaybookYaml", () => {
  it("decodes a playbooks/<name>.yaml", () => {
    const { doc, problems } = parsePlaybookYaml(`
image: ghcr.io/example/reporter:v1
system_prompt: Write the weekly report.
allowed_tools: [read, bash]
max_turns: 50
timeout: 30m
skills: [pr-review]
mcp_servers: [linear]
interactive: true
env:
  CI: "true"
`);
    expect(problems).toEqual([]);
    expect(doc?.image).toBe("ghcr.io/example/reporter:v1");
    expect(doc?.allowed_tools).toEqual(["read", "bash"]);
    expect(doc?.interactive).toBe(true);
    expect(doc?.skills).toEqual(["pr-review"]);
  });

  it("rejects a field the schema does not have rather than dropping it", () => {
    const { doc, problems, issues } = parsePlaybookYaml(
      "system_prompt: hi\nallowed_tools: [read]\nsytem_prompt: oops\n",
    );
    expect(doc).toBeUndefined();
    expect(problems.join("\n")).toContain("sytem_prompt");
    expect(problems.join("\n")).toContain("is not a playbook field");
    expect(issues[0]?.line).toBe(3);
  });

  it("refuses a tool the harness does not have", () => {
    const { problems } = parsePlaybookYaml(
      "system_prompt: hi\nallowed_tools: [read, notepad]\n",
    );
    expect(problems.join("\n")).toContain("notepad");
  });

  it("round-trips a draft through YAML", () => {
    const text = draftToYaml(draft);
    const { doc, problems } = parsePlaybookYaml(text);
    expect(problems).toEqual([]);
    expect(docToDraft("reporter", doc!).interactive).toBe(true);
    expect(docToDraft("reporter", doc!).skills).toEqual(["pr-review"]);
    expect(docToDraft("reporter", doc!).env).toEqual({ CI: "true" });
  });
});

describe("definitionToYaml", () => {
  it("renders a GetProfile playbook in the file shape", () => {
    const pb = create(PlaybookDefinitionSchema, {
      name: "general",
      image: "podium-agent-runtime:dev",
      systemPrompt: "Answer the question.",
      allowedTools: ["read", "grep"],
      maxTurns: 50,
      timeout: "15m",
    });
    const text = definitionToYaml(pb);
    expect(text).toContain("image: podium-agent-runtime:dev");
    expect(text).toContain("allowed_tools:");
    const { problems } = parsePlaybookYaml(text);
    expect(problems).toEqual([]);
  });
});
