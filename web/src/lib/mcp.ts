/**
 * The MCP servers worth offering as a starting point. It is a convenience and not a
 * catalogue: the hard part of adding a well-known server is remembering its endpoint, and a
 * wrong URL produces a turn that fails on a tool call rather than a form that says no.
 *
 * Nothing here is fetched, checked or ranked, and a preset only fills in a form — the URL an
 * operator ends up saving is the one this list would have given them, or their own.
 */
export type McpPreset = {
  /** label is what the button says. */
  label: string;
  /** name becomes the registration's name and the prefix its tools carry. */
  name: string;
  url: string;
  description: string;
};

export const MCP_PRESETS: McpPreset[] = [
  {
    label: "Linear",
    name: "linear",
    url: "https://mcp.linear.app/mcp",
    description: "Issues, projects and cycles.",
  },
  {
    label: "Notion",
    name: "notion",
    url: "https://mcp.notion.com/mcp",
    description: "Pages and databases.",
  },
  {
    label: "Sentry",
    name: "sentry",
    url: "https://mcp.sentry.dev/mcp",
    description: "Issues and stack traces.",
  },
  {
    label: "GitHub",
    name: "github",
    url: "https://api.githubcopilot.com/mcp/",
    description: "Repositories, issues and pull requests.",
  },
];

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
