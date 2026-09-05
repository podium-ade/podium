import { create } from "@bufbuild/protobuf";
import { AgentBackendSchema, type AgentBackend } from "../gen/podium/agent/v1/agent_pb";

/**
 * catalogue is a two-backend ListAgents answer, cut down to what a picker test needs: one
 * ready backend and one that has no credential, and a model on each whose effort levels
 * differ — which is the case the picker has to get right.
 */
export function catalogue(): AgentBackend[] {
  return [
    create(AgentBackendSchema, {
      id: "claude",
      displayName: "Claude",
      provider: "anthropic",
      note: "The Claude Agent SDK against Anthropic's API.",
      defaultModel: "claude-opus-5",
      ready: true,
      models: [
        {
          id: "claude-opus-5",
          displayName: "Claude Opus 5",
          note: "The default.",
          contextTokens: 1_000_000,
          efforts: ["low", "medium", "high", "xhigh", "max"],
        },
        {
          id: "claude-sonnet-5",
          displayName: "Claude Sonnet 5",
          note: "Cheaper.",
          contextTokens: 1_000_000,
          efforts: ["low", "medium", "high", "xhigh", "max"],
        },
      ],
    }),
    create(AgentBackendSchema, {
      id: "grok",
      displayName: "Grok",
      provider: "xai",
      note: "The same harness, pointed at xAI.",
      defaultModel: "grok-4.6",
      ready: false,
      models: [
        {
          id: "grok-4.6",
          displayName: "Grok 4.6",
          note: "The default.",
          contextTokens: 500_000,
          efforts: ["low", "medium", "high", "xhigh"],
        },
        {
          id: "grok-4.5",
          displayName: "Grok 4.5",
          note: "xhigh is treated as high, so it is not offered.",
          contextTokens: 500_000,
          efforts: ["low", "medium", "high"],
        },
      ],
    }),
  ];
}
