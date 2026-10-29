import { beforeEach, describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { RoleAdmin, RoleMember, RoleOwner } from "../lib/rbac";
import { Header } from "./Header";
import { NAV_ICON_PATHS, type NavIconName } from "./NavIcon";

const base: Viewer = {
  login: "local",
  displayName: "",
  kind: IdentityKind.LOCAL_TOKEN,
  agentEnabled: false,
  serverVersion: "dev",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
};

function mount(who: Viewer | undefined, path = "/") {
  return render(
    <ViewerContext value={who}>
      <MemoryRouter initialEntries={[path]}>
        <Header />
      </MemoryRouter>
    </ViewerContext>,
  );
}

const human: Viewer = {
  ...base,
  agentEnabled: true,
  login: "bob@acme.com",
  displayName: "Bob",
  kind: IdentityKind.USER,
  roles: [RoleMember],
  claimed: true,
  googleAuthEnabled: true,
  hostedDomain: "acme.com",
};

function groupLinks(name: string) {
  return within(screen.getByRole("group", { name }))
    .getAllByRole("link")
    .map((link) => link.getAttribute("aria-label"));
}

describe("Header", () => {
  beforeEach(() => localStorage.clear());

  it("always offers the screens every control plane has", () => {
    mount(base);
    expect(screen.getByRole("link", { name: "Tasks" })).toHaveAttribute("href", "/tasks");
    for (const label of ["Nodes", "Secrets", "Registries", "Sandboxes"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
    expect(screen.getByRole("link", { name: "Members" })).toHaveAttribute("href", "/agent/settings/users");
    // Settings is not a page any more: each of its sections is an entry in its group.
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
  });

  it("groups the pages by what an operator is asking", () => {
    mount({ ...base, agentEnabled: true });
    const toggles = screen
      .getAllByRole("button", { expanded: true })
      .map((button) => button.textContent);
    expect(toggles).toEqual(["Work", "Agents", "Access", "Compute", "Organization"]);
    expect(groupLinks("Work")).toEqual(["Chat", "Tasks", "Sessions"]);
    expect(groupLinks("Agents")).toEqual(["Assistants", "Playbooks", "Skills", "Memory", "MCP servers"]);
    expect(groupLinks("Access")).toEqual(["Connections", "Channels", "Secrets"]);
    expect(groupLinks("Compute")).toEqual(["Models", "Nodes", "Backend", "Sandboxes", "Snapshots", "Registries"]);
    expect(groupLinks("Organization")).toEqual(["Members", "Usage"]);
  });

  it("links each former settings section to its own address", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Connections" })).toHaveAttribute("href", "/agent/settings/connections");
    expect(screen.getByRole("link", { name: "Models" })).toHaveAttribute("href", "/agent/settings/models");
    expect(screen.getByRole("link", { name: "Backend" })).toHaveAttribute("href", "/agent/settings/runs-on");
  });

  // A control plane with no conductor has no Agent screen worth reaching, and WhoAmI is what
  // says so. A server built before agent_enabled existed sends nothing and reads as false.
  it("drops the conductor's pages, and the Agents group, when the server has none", () => {
    mount(base);
    expect(screen.queryByRole("group", { name: "Agents" })).toBeNull();
    for (const label of ["Chat", "Sessions", "Assistants", "Playbooks", "Models", "Backend", "Connections", "Channels", "Usage"]) {
      expect(screen.queryByRole("link", { name: label })).toBeNull();
    }
    expect(groupLinks("Work")).toEqual(["Tasks"]);
  });

  it("shows nothing before WhoAmI has answered at all", () => {
    mount(undefined);
    expect(screen.queryByRole("link", { name: "Tasks" })).toBeNull();
    expect(screen.queryByRole("searchbox", { name: "Search pages" })).toBeNull();
  });

  it("keeps Connections from a member and gives it to an admin and an owner", () => {
    const member = mount(human);
    expect(screen.getByRole("link", { name: "Assistants" })).toHaveAttribute("href", "/agent/assistants");
    expect(screen.getByRole("link", { name: "Models" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Connections" })).toBeNull();
    member.unmount();

    const admin = mount({ ...human, roles: [RoleAdmin] });
    expect(screen.getByRole("link", { name: "Connections" })).toBeInTheDocument();
    admin.unmount();

    mount({ ...human, roles: [RoleOwner] });
    expect(screen.getByRole("link", { name: "Connections" })).toBeInTheDocument();
  });

  it("lights each page on its own route and nothing else", () => {
    const who = { ...base, agentEnabled: true };
    const cases: [string, string][] = [
      ["/agent/chat", "Chat"],
      ["/agent/chat/chat_01abc", "Chat"],
      ["/agent/chat/a/pers_01", "Chat"],
      ["/agent/assistants", "Assistants"],
      ["/agent/playbooks", "Playbooks"],
      ["/agent/mcp", "MCP servers"],
      ["/agent/channels", "Channels"],
      ["/agent/settings/models", "Models"],
      ["/agent/settings/users", "Members"],
      ["/agent/settings/runs-on", "Backend"],
    ];
    for (const [path, label] of cases) {
      const view = mount(who, path);
      const current = screen.getAllByRole("link").filter((link) => link.getAttribute("aria-current") === "page");
      expect(current.map((link) => link.getAttribute("aria-label")), path).toEqual([label]);
      view.unmount();
    }
  });

  it("closes a group, remembers it, and opens it again for a page inside it", async () => {
    const first = mount({ ...base, agentEnabled: true }, "/tasks");
    const agents = screen.getByRole("button", { name: "Agents" });
    await userEvent.click(agents);
    expect(agents).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("link", { name: "Playbooks", hidden: true })).not.toBeVisible();
    first.unmount();

    const again = mount({ ...base, agentEnabled: true }, "/tasks");
    expect(screen.getByRole("button", { name: "Agents" })).toHaveAttribute("aria-expanded", "false");
    again.unmount();

    mount({ ...base, agentEnabled: true }, "/agent/skills");
    expect(screen.getByRole("button", { name: "Agents" })).toHaveAttribute("aria-expanded", "true");
  });

  it("filters the pages by name and purpose, and says when nothing matches", async () => {
    mount({ ...base, agentEnabled: true });
    const search = screen.getByRole("searchbox", { name: "Search pages" });

    await userEvent.type(search, "github");
    expect(screen.getAllByRole("link").map((link) => link.getAttribute("aria-label"))).toEqual(["Connections"]);
    expect(screen.queryByRole("group", { name: "Work" })).toBeNull();

    await userEvent.clear(search);
    await userEvent.type(search, "sec");
    expect(screen.getByRole("link", { name: "Secrets" }).querySelector("mark")).toHaveTextContent("Sec");

    await userEvent.clear(search);
    await userEvent.type(search, "zzz");
    expect(screen.getByRole("status")).toHaveTextContent("No pages match “zzz”.");
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(search).toHaveValue("");
    expect(screen.getByRole("link", { name: "Chat" })).toBeInTheDocument();
  });

  it("opens every group with a hit while searching", async () => {
    mount({ ...base, agentEnabled: true });
    await userEvent.click(screen.getByRole("button", { name: "Compute" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search pages" }), "nodes");
    expect(screen.getByRole("button", { name: "Compute" })).toHaveAttribute("aria-expanded", "true");
  });

  // Search opens every group with a hit. Folding one of those is for that search only: the
  // fold the operator saved before searching is what comes back when the search clears.
  it("keeps the remembered folds when a group is toggled during a search", async () => {
    const first = mount({ ...base, agentEnabled: true });
    await userEvent.click(screen.getByRole("button", { name: "Compute" }));
    const search = screen.getByRole("searchbox", { name: "Search pages" });
    await userEvent.type(search, "nodes");
    const compute = screen.getByRole("button", { name: "Compute" });
    expect(compute).toHaveAttribute("aria-expanded", "true");

    await userEvent.click(compute);
    expect(compute).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Nodes" })).toBeNull();

    await userEvent.clear(search);
    expect(screen.getByRole("button", { name: "Compute" })).toHaveAttribute("aria-expanded", "false");
    first.unmount();

    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("button", { name: "Compute" })).toHaveAttribute("aria-expanded", "false");
  });

  it("goes to the first hit on Enter and clears the search", async () => {
    mount({ ...base, agentEnabled: true });
    const search = screen.getByRole("searchbox", { name: "Search pages" });
    await userEvent.type(search, "llm{Enter}");
    expect(search).toHaveValue("");
    expect(screen.getByRole("link", { name: "Models" })).toHaveAttribute("aria-current", "page");
  });

  it("focuses the search on Ctrl K", async () => {
    mount({ ...base, agentEnabled: true });
    await userEvent.keyboard("{Control>}k{/Control}");
    // Focus moves with the shortcut, so the letters typed right after it are the search.
    await userEvent.keyboard("git");
    expect(screen.getByRole("searchbox", { name: "Search pages" })).toHaveFocus();
    expect(screen.getByRole("searchbox", { name: "Search pages" })).toHaveValue("git");
  });

  it("moves between pages with the arrow keys", async () => {
    mount({ ...base, agentEnabled: true });
    const search = screen.getByRole("searchbox", { name: "Search pages" });
    await userEvent.type(search, "s");
    await userEvent.keyboard("{ArrowDown}");
    const first = screen.getAllByRole("link")[0];
    expect(first).toHaveFocus();
    await userEvent.keyboard("{ArrowUp}");
    expect(search).toHaveFocus();
  });

  it("offers your account and Sign out for a Google Workspace session", async () => {
    mount({
      ...base,
      login: "alice@acme.com",
      displayName: "Alice",
      kind: IdentityKind.USER,
      googleAuthEnabled: true,
      hostedDomain: "acme.com",
    });
    await userEvent.click(screen.getByRole("button", { name: "Alice" }));
    expect(screen.getByRole("menuitem", { name: "Your account" })).toHaveAttribute(
      "href",
      "/agent/settings/account",
    );
    expect(screen.getByRole("menuitem", { name: "Sign out" })).toHaveAttribute(
      "href",
      "/auth/logout",
    );
  });

  it("does not offer Sign out under the local token", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.queryByRole("button", { name: "local" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Sign out" })).toBeNull();
  });

  it("shows the Google profile photo when WhoAmI has one", () => {
    mount({
      ...base,
      login: "alice@acme.com",
      displayName: "Alice",
      kind: IdentityKind.USER,
      googleAuthEnabled: true,
      pictureUrl: "https://lh3.googleusercontent.com/a/alice",
    });
    const img = screen.getByRole("presentation");
    expect(img).toHaveAttribute("src", "/auth/picture");
  });

  it("draws the design side-menu icons", () => {
    mount({ ...base, agentEnabled: true });
    const pages: [string, NavIconName][] = [
      ["Chat", "chat"],
      ["Tasks", "tasks"],
      ["Sessions", "sessions"],
      ["Assistants", "assistants"],
      ["Playbooks", "playbooks"],
      ["Skills", "skills"],
      ["Memory", "memory"],
      ["MCP servers", "mcp"],
      ["Connections", "connections"],
      ["Channels", "channels"],
      ["Secrets", "secrets"],
      ["Models", "models"],
      ["Nodes", "nodes"],
      ["Backend", "backend"],
      ["Sandboxes", "sandboxes"],
      ["Snapshots", "snapshots"],
      ["Registries", "registries"],
      ["Members", "members"],
      ["Usage", "usage"],
    ];
    for (const [label, name] of pages) {
      const icon = screen.getByRole("link", { name: label }).querySelector("svg");
      expect(icon).toHaveAttribute("stroke-width", "1.8");
      expect(icon?.querySelector("path")).toHaveAttribute("d", NAV_ICON_PATHS[name]);
    }
    const collapse = screen.getByRole("button", { name: "Collapse sidebar" }).querySelector("svg");
    expect(collapse?.querySelector("rect")).toHaveAttribute("width", "18");
    expect(collapse?.querySelector("path")).toHaveAttribute("d", "M9 4v16m7-11-3 3 3 3");
  });
});
