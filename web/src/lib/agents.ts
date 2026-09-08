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
  /** secretName is the Podium secret the credential is stored as. */
  secretName: string;
  keyPlaceholder: string;
  /** consoleURL is where a human gets a key. */
  consoleURL: string;
  /** subscription is the sign-in offer, or undefined for a provider that has none. */
  subscription?: { label: string; hint: string };
};

export const ANTHROPIC: Provider = {
  id: "anthropic",
  name: "Anthropic",
  backendID: "claude",
  models: "Claude",
  secretName: "podium.agent.anthropic_api_key",
  keyPlaceholder: "sk-ant-…",
  consoleURL: "https://console.anthropic.com/settings/keys",
};

export const XAI: Provider = {
  id: "xai",
  name: "xAI",
  backendID: "grok",
  models: "Grok",
  secretName: "podium.agent.xai_api_key",
  keyPlaceholder: "xai-…",
  consoleURL: "https://console.x.ai",
  subscription: {
    label: "Sign in with a SuperGrok or X Premium+ subscription",
    hint: "Uses your subscription instead of pay-as-you-go API credit.",
  },
};

/** The providers Settings shows, in order. */
export const PROVIDERS: Provider[] = [ANTHROPIC, XAI];
