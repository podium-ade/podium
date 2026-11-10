import type { NavIconName } from "../components/NavIcon";
import type { Viewer } from "./identity";

/**
 * NavPage is one sidebar entry. `desc` and `keys` are what the sidebar search matches
 * besides the label, so "github" finds Connections and "llm" finds Models.
 */
export type NavPage = {
  id: string;
  label: string;
  to: string;
  icon: NavIconName;
  desc: string;
  keys: string;
  /** Exact match only, so /tasks/:id does not light Tasks. */
  end?: boolean;
  /** Whether this viewer gets the entry. Omitted means always. */
  show?: (who: Viewer) => boolean;
};

export type NavGroup = { id: string; label: string; pages: NavPage[] };

const agent = (who: Viewer) => who.agentEnabled;

/**
 * NAV_GROUPS is the sidebar, grouped by the question an operator asks: what am I working
 * on, who are the agents, what can they reach, what do they run on, and who uses Podium.
 * Settings is not a page of its own any more; each of its sections sits in its group.
 */
export const NAV_GROUPS: NavGroup[] = [
  {
    id: "work",
    label: "Work",
    pages: [
      { id: "chat", label: "Chat", to: "/agent/chat", icon: "chat", desc: "", keys: "conversation messages talk", show: agent },
      { id: "routines", label: "Routines", to: "/agent/routines", icon: "routines", desc: "Jobs an assistant does on a schedule.", keys: "schedule cron recurring routine automation timer", show: agent },
      { id: "tasks", label: "Tasks", to: "/tasks", end: true, icon: "tasks", desc: "", keys: "jobs runs work queue" },
      // Hidden from the sidebar for now and maybe removed later. The entry stays so the
      // page, still reachable by URL, keeps its group in the top bar.
      { id: "sessions", label: "Sessions", to: "/agent/sessions", icon: "sessions", desc: "", keys: "history runs workspace", show: () => false },
    ],
  },
  {
    id: "agents",
    label: "Agents",
    pages: [
      { id: "assistants", label: "Assistants", to: "/agent/assistants", icon: "assistants", desc: "Define each assistant and the playbook it answers with.", keys: "agents bots persona", show: agent },
      { id: "playbooks", label: "Playbooks", to: "/agent/playbooks", icon: "playbooks", desc: "Steps, tools and MCP servers an assistant may use.", keys: "workflow steps procedures", show: agent },
      { id: "skills", label: "Skills", to: "/agent/skills", icon: "skills", desc: "Reusable instructions assistants load on demand.", keys: "instructions abilities", show: agent },
      { id: "memory", label: "Memory", to: "/agent/memory", icon: "memory", desc: "What assistants keep between chats.", keys: "remember context knowledge", show: agent },
      { id: "mcp", label: "MCP servers", to: "/agent/mcp", icon: "mcp", desc: "Endpoints a playbook can name for tools.", keys: "tools endpoints servers", show: agent },
    ],
  },
  {
    id: "access",
    label: "Access",
    pages: [
      { id: "channels", label: "Channels", to: "/agent/channels", icon: "channels", desc: "Where assistants listen and reply.", keys: "slack listen reply", show: agent },
      { id: "secrets", label: "Secrets", to: "/secrets", icon: "secrets", desc: "Credentials tasks and registries receive.", keys: "credentials keys tokens passwords vault" },
    ],
  },
  {
    id: "compute",
    label: "Compute",
    pages: [
      { id: "models", label: "Models", to: "/agent/settings/models", icon: "models", desc: "Model providers chat can spend on.", keys: "llm provider claude anthropic openai gpt grok api key", show: agent },
      { id: "nodes", label: "Nodes", to: "/nodes", icon: "nodes", desc: "Workers enrolled with this control plane.", keys: "workers machines enroll drain" },
      { id: "sandboxes", label: "Sandboxes", to: "/sandboxes", icon: "sandboxes", desc: "Sandbox providers sessions and tasks can run in.", keys: "modal daytona e2b provider capacity" },
      { id: "snapshots", label: "Snapshots", to: "/snapshots", icon: "snapshots", desc: "Saved conversation workspaces, and when they are deleted.", keys: "workspace storage disk retention cleanup s3 bucket" },
      { id: "registries", label: "Registries", to: "/registries", icon: "registries", desc: "Image registries workers pull from.", keys: "images docker containers" },
    ],
  },
  {
    id: "organization",
    label: "Organization",
    pages: [
      // Cost is recorded per agent turn, so with no conductor there is nothing to report.
      { id: "usage", label: "Usage", to: "/usage", icon: "usage", desc: "Spend and activity across the organization.", keys: "cost spend billing tokens", show: agent },
      // Everything set once for the whole organization: its name, Members, Connections, the
      // global model keys, the default backend and the routines policy. Each of those used to
      // be an entry of its own, and Routines appeared twice. See pages/OrgSettingsPage.
      { id: "org-settings", label: "Settings", to: "/org/settings", icon: "settings", desc: "Organization name, members, connections, global keys, backend and routines policy.", keys: "organization name rename brand deployment members users roles team permissions owner admin connections github slack app webhook global keys backend runs on sandbox routines limits policy" },
    ],
  },
];

/** The groups this viewer sees, without the pages they do not get, and without empty groups. */
export function navGroupsFor(who: Viewer | undefined): NavGroup[] {
  if (!who) return [];
  return NAV_GROUPS.map((g) => ({ ...g, pages: g.pages.filter((p) => !p.show || p.show(who)) })).filter(
    (g) => g.pages.length > 0,
  );
}

export function pageActive(page: NavPage, pathname: string): boolean {
  if (page.end) return pathname === page.to;
  return pathname === page.to || pathname.startsWith(`${page.to}/`);
}

/** Whether a page matches a lowercased, trimmed search query. */
export function pageMatches(page: NavPage, query: string): boolean {
  if (!query) return true;
  return [page.label, page.desc, page.keys].join(" ").toLowerCase().includes(query);
}

/** Routes that belong to a page without sitting under its address. */
const ALIASES: [prefix: string, page: string][] = [
  ["/agent/profile", "assistants"],
  ["/submit", "tasks"],
  ["/tasks/", "tasks"],
  ["/users", "org-settings"],
];

function pageFor(pathname: string): { group: NavGroup; page: NavPage } | undefined {
  for (const group of NAV_GROUPS) {
    for (const page of group.pages) {
      if (pageActive(page, pathname)) return { group, page };
    }
  }
  const alias = ALIASES.find(([prefix]) => pathname === prefix || pathname.startsWith(prefix));
  if (!alias) return undefined;
  for (const group of NAV_GROUPS) {
    const page = group.pages.find((p) => p.id === alias[1]);
    if (page) return { group, page };
  }
  return undefined;
}

/** The sidebar group of the page at pathname, which the top bar names above the title. */
export function pageCategory(pathname: string): string {
  if (!pathname) return "";
  if (pathname.startsWith("/notifications")) return "Activity";
  if (pathname.startsWith("/agent/settings/account")) return "Personal";
  if (pathname === "/agent") return "Work";
  return pageFor(pathname)?.group.label ?? "";
}

/** The id of the group holding the page at pathname, so the sidebar opens it. */
export function groupOf(pathname: string): string | undefined {
  return pageFor(pathname)?.group.id;
}
