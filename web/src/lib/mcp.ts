/**
 * The MCP servers worth offering as a starting point. It is a convenience and not a
 * catalogue: the hard part of adding a well-known server is remembering its endpoint, and a
 * wrong URL produces a turn that fails on a tool call rather than a form that says no.
 *
 * Nothing here is fetched, checked or ranked, and a preset only fills in a form — the URL an
 * operator ends up saving is the one this list would have given them, or their own.
 */
export type McpPreset = {
  /** label is the product name on the picker. */
  label: string;
  /** name becomes the registration's name and the prefix its tools carry. */
  name: string;
  url: string;
  description: string;
  /** tokenPlaceholder is the product's own key shape, shown in the token field. */
  tokenPlaceholder: string;
  /** tokenHint is how to get that key, and what format it has. */
  tokenHint: string;
};

const GENERIC_TOKEN_HINT =
  "Sent as Authorization: Bearer. Stored as a Podium secret; only the last four characters are ever read back. Leave it empty for a server that needs no credential, or to sign in later.";

export const MCP_PRESETS: McpPreset[] = [
  {
    label: "Linear",
    name: "linear",
    url: "https://mcp.linear.app/mcp",
    description: "Issues, projects and cycles.",
    tokenPlaceholder: "lin_api_…",
    tokenHint:
      "A Linear personal API key from Settings → Security & access. It starts with lin_api_. Leave empty to sign in later.",
  },
  {
    label: "Notion",
    name: "notion",
    url: "https://mcp.notion.com/mcp",
    description: "Pages and databases.",
    tokenPlaceholder: "ntn_…",
    tokenHint:
      "A Notion internal integration token from notion.so/my-integrations. It starts with ntn_ (older ones, secret_). Leave empty to sign in later.",
  },
  {
    label: "Sentry",
    name: "sentry",
    url: "https://mcp.sentry.dev/mcp",
    description: "Issues and stack traces.",
    tokenPlaceholder: "sntryu_…",
    tokenHint:
      "A Sentry User Auth Token from Settings → Account → API → Auth Tokens. Organization tokens start with sntrys_. Leave empty to sign in later.",
  },
  {
    label: "GitHub",
    name: "github",
    url: "https://api.githubcopilot.com/mcp/",
    description: "Repositories, issues and pull requests.",
    tokenPlaceholder: "ghp_…",
    tokenHint:
      "A GitHub personal access token. Classic tokens start with ghp_, fine-grained with github_pat_. Leave empty to sign in later.",
  },
  {
    label: "Slack",
    name: "slack",
    url: "https://mcp.slack.com/mcp",
    description: "Channels, messages and search.",
    tokenPlaceholder: "xoxb-…",
    tokenHint:
      "A Slack bot token from api.slack.com/apps. It starts with xoxb- (bot) or xoxp- (user). Leave empty to sign in later.",
  },
  {
    label: "Stripe",
    name: "stripe",
    url: "https://mcp.stripe.com",
    description: "Customers, payments and invoices.",
    tokenPlaceholder: "sk_live_…",
    tokenHint:
      "A Stripe secret key from Dashboard → Developers → API keys. It starts with sk_live_ or sk_test_. Leave empty to sign in later.",
  },
  {
    label: "Figma",
    name: "figma",
    url: "https://mcp.figma.com/mcp",
    description: "Files, components and design context.",
    tokenPlaceholder: "figd_…",
    tokenHint:
      "A Figma personal access token from Settings → Security. It starts with figd_. Leave empty to sign in later.",
  },
];

/** tokenHelp is the placeholder and hint for a picked product, or the generic custom ones. */
export function tokenHelp(preset?: McpPreset): { placeholder: string; hint: string } {
  if (!preset) {
    return { placeholder: "", hint: GENERIC_TOKEN_HINT };
  }
  return { placeholder: preset.tokenPlaceholder, hint: preset.tokenHint };
}

function hostnameOf(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return "";
  }
}

/**
 * presetFor matches a registration to a known product, by name first and then by the
 * endpoint's host. Used to keep the token field's hint in the product's own format after
 * the server already exists.
 */
export function presetFor(server: { name?: string; url?: string }): McpPreset | undefined {
  const name = (server.name ?? "").trim().toLowerCase();
  if (name) {
    const byName = MCP_PRESETS.find((p) => p.name === name);
    if (byName) return byName;
  }
  const host = hostnameOf(server.url ?? "");
  if (!host) return undefined;
  return MCP_PRESETS.find((p) => hostnameOf(p.url) === host);
}

/**
 * MCP_CALLBACK_PATH is the route an authorization server redirects back to, and the
 * conductor accepts no other: it validates the redirect_uri the browser sends against this
 * exact path. Change it here and in internal/agent/api/mcpoauth.go together.
 *
 * It is a route in THIS app rather than an endpoint on podium-server, and that is the whole
 * design: an OAuth redirect is a plain browser GET with no bearer token, so a server route
 * would have to sit outside the identity middleware. Landing in the SPA instead means the
 * code reaches the conductor over the ordinary authenticated API.
 */
export const MCP_CALLBACK_PATH = "/agent/mcp/callback";

/** callbackURL is this install's own callback, which is what gets registered dynamically. */
export function callbackURL(): string {
  return window.location.origin + MCP_CALLBACK_PATH;
}

/**
 * PENDING_KEY is where a started sign-in is parked while the browser is away at the
 * authorization server.
 *
 * sessionStorage and not memory: the operator leaves this origin entirely and comes back to
 * a freshly loaded page, so nothing in React survives the trip. It holds a flow id and a
 * state — names for a sign-in, not credentials — and the verifier that would make them
 * usable never left the conductor.
 */
export const PENDING_KEY = "podium.mcp.oauth.pending";

export type PendingMcpOAuth = {
  flowId: string;
  state: string;
  name: string;
  issuer: string;
};

export function rememberPending(p: PendingMcpOAuth) {
  try {
    sessionStorage.setItem(PENDING_KEY, JSON.stringify(p));
  } catch {
    // A browser with storage blocked still completes the sign-in: the callback carries the
    // state, and the conductor's own copy is the comparison that counts. What is lost is
    // only the server's name for the "signing in to…" line.
  }
}

export function takePending(): PendingMcpOAuth | undefined {
  try {
    const raw = sessionStorage.getItem(PENDING_KEY);
    sessionStorage.removeItem(PENDING_KEY);
    return raw ? (JSON.parse(raw) as PendingMcpOAuth) : undefined;
  } catch {
    return undefined;
  }
}
