/**
 * The vocabulary the agent screens share: what a backend choice is, and which providers
 * Settings offers a card for.
 *
 * It lives beside the components rather than in them so that a constant and a component are
 * never exported from the same module — which is what keeps fast refresh working — and so
 * that a test can build a choice without importing a picker.
 */

/** AgentChoice is the triple a playbook or a profile holds. "" means "inherit". */
export type AgentChoice = { agent: string; model: string; effort: string };

/** The empty choice, spelled once. */
export const INHERIT: AgentChoice = { agent: "", model: "", effort: "" };

/** Provider describes one row of Settings. It is data, not behaviour. */
export type Provider = {
  /** id is the wire value SetProviderKey takes. */
  id: string;
  /** name is the company. */
  name: string;
  /** backendID picks the glyph, and names the agent backend this credential runs. */
  backendID: string;
  /** models is what a human calls the thing this credential buys: "Claude", "Grok". */
  models: string;
  /** how is the one line under the name: how you connect, not where the secret lives. */
  how: string;
  /** secretName is the Podium secret the credential is stored as. */
  secretName: string;
  keyPlaceholder: string;
  /** consoleURL is where a human gets a key. */
  consoleURL: string;
  /**
   * subscription is the sign-in offer, or undefined for a provider that has none. hint is said
   * to a person signing in their own; globalHint to an admin signing one in for everyone.
   * personalOnly is a sign-in this row never takes as the global credential: a global Claude
   * subscription is a row of its own, CLAUDE_SUBSCRIPTION, kept beside the global key.
   */
  subscription?: { label: string; hint: string; globalHint?: string; personalOnly?: boolean };
};

export const ANTHROPIC: Provider = {
  id: "anthropic",
  name: "Anthropic",
  backendID: "claude",
  models: "Claude",
  how: "API key",
  secretName: "podium.agent.anthropic_api_key",
  keyPlaceholder: "sk-ant-…",
  consoleURL: "https://console.anthropic.com/settings/keys",
  subscription: {
    label: "Sign in with a Claude Pro or Max subscription",
    hint: "Your chats with the assistant run on your Claude plan, through Claude Code. Delegated tasks keep using the global key.",
    personalOnly: true,
  },
};

/**
 * CLAUDE_SUBSCRIPTION is the global Claude sign-in's row. It is not a provider of its own: it
 * is Anthropic's, kept beside the global API key, because only the assistant's chats can spend
 * a Claude plan. Its id names the row; the RPCs are Anthropic's.
 */
export const CLAUDE_SUBSCRIPTION: Provider = {
  ...ANTHROPIC,
  id: "claude-subscription",
  models: "Claude subscription",
  how: "Pro or Max, for chats",
  subscription: {
    label: "Sign in with a Claude Pro or Max subscription",
    hint: "",
    globalHint:
      "Chats with the assistant run on this Claude plan, through Claude Code, for everyone without an Anthropic credential of their own. Delegated tasks keep using the global API key.",
  },
};

export const XAI: Provider = {
  id: "xai",
  name: "xAI",
  backendID: "grok",
  models: "Grok",
  how: "API key or SuperGrok",
  secretName: "podium.agent.xai_api_key",
  keyPlaceholder: "xai-…",
  consoleURL: "https://console.x.ai",
  subscription: {
    label: "Sign in with a SuperGrok or X Premium+ subscription",
    hint: "Uses your subscription instead of pay-as-you-go API credit.",
    globalHint:
      "Every Grok turn without a credential of its own runs on this subscription, instead of pay-as-you-go API credit. It replaces the global API key.",
  },
};

export const OPENAI: Provider = {
  id: "openai",
  name: "OpenAI",
  backendID: "openai",
  models: "GPT",
  how: "API key or ChatGPT",
  secretName: "podium.agent.openai_api_key",
  keyPlaceholder: "sk-…",
  consoleURL: "https://platform.openai.com/api-keys",
  subscription: {
    label: "Sign in with a ChatGPT or Codex subscription",
    hint: "Uses your ChatGPT Plus, Pro, or Codex plan instead of pay-as-you-go API credit.",
    globalHint:
      "Every GPT turn without a credential of its own runs on this ChatGPT plan, instead of pay-as-you-go API credit. It replaces the global API key.",
  },
};

export const GOOGLE: Provider = {
  id: "google",
  name: "Google",
  backendID: "gemini",
  models: "Gemini",
  how: "API key",
  secretName: "podium.agent.google_api_key",
  keyPlaceholder: "AIza…",
  consoleURL: "https://aistudio.google.com/apikey",
};

/** providerName is the company for a wire id, for a sentence that already has the backend. */
export function providerName(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.name ?? id;
}

/** The providers Settings shows, in order. */
export const PROVIDERS: Provider[] = [ANTHROPIC, XAI, OPENAI, GOOGLE];
